package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultUsageFallbackDelay       = 2 * time.Second
	usageFallbackNativeRecentWindow = 750 * time.Millisecond
	usageFallbackLateNativeWindow   = 30 * time.Second
)

var (
	usageFallbackRecordDelay = defaultUsageFallbackDelay
	usageFallbacks           = newUsageFallbackCoordinator()
	authIndexes              = newAuthIndexLearner()
)

// authIndexLearner remembers the CPA-computed auth index for each auth ID.
// Native usage records carry both fields, while interceptor metadata only
// carries the auth ID; reusing the learned index keeps fallback records in
// the same credential group as native ones on the dashboard.
type authIndexLearner struct {
	mu      sync.RWMutex
	indexes map[string]string
}

const maxLearnedAuthIndexes = 4096

func newAuthIndexLearner() *authIndexLearner {
	return &authIndexLearner{indexes: make(map[string]string)}
}

func (l *authIndexLearner) Learn(authID, authIndex string) {
	if l == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(authID))
	value := strings.TrimSpace(authIndex)
	if key == "" || value == "" || value == safeCredentialIdentity(authID) {
		return
	}
	l.mu.Lock()
	if existing, ok := l.indexes[key]; !ok || existing != value {
		if len(l.indexes) < maxLearnedAuthIndexes || l.indexes[key] != "" {
			l.indexes[key] = value
		}
	}
	l.mu.Unlock()
}

func (l *authIndexLearner) Lookup(authID string) string {
	if l == nil {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(authID))
	if key == "" {
		return ""
	}
	l.mu.RLock()
	value := l.indexes[key]
	l.mu.RUnlock()
	return value
}

type ResponseInterceptRequest struct {
	SourceFormat    string
	Model           string
	RequestedModel  string
	Stream          bool
	RequestHeaders  map[string][]string
	ResponseHeaders map[string][]string
	OriginalRequest []byte
	RequestBody     []byte
	Body            []byte
	StatusCode      int
	Metadata        map[string]any
}

func (r *ResponseInterceptRequest) UnmarshalJSON(data []byte) error {
	return unmarshalResponseInterceptRequest(data, r)
}

type ResponseStreamChunkRequest struct {
	ResponseInterceptRequest
	HistoryChunks [][]byte
	ChunkIndex    int
}

func (r *ResponseStreamChunkRequest) UnmarshalJSON(data []byte) error {
	if err := unmarshalResponseInterceptRequest(data, &r.ResponseInterceptRequest); err != nil {
		return err
	}
	var wire struct {
		HistoryChunks      [][]byte `json:"HistoryChunks"`
		HistoryChunksSnake [][]byte `json:"history_chunks"`
		ChunkIndex         int      `json:"ChunkIndex"`
		ChunkIndexSnake    int      `json:"chunk_index"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.HistoryChunks = wire.HistoryChunks
	if len(r.HistoryChunks) == 0 {
		r.HistoryChunks = wire.HistoryChunksSnake
	}
	r.ChunkIndex = wire.ChunkIndex
	if r.ChunkIndex == 0 {
		r.ChunkIndex = wire.ChunkIndexSnake
	}
	return nil
}

func unmarshalResponseInterceptRequest(data []byte, r *ResponseInterceptRequest) error {
	var wire struct {
		SourceFormat         string              `json:"SourceFormat"`
		SourceFormatSnake    string              `json:"source_format"`
		Model                string              `json:"Model"`
		ModelSnake           string              `json:"model"`
		RequestedModel       string              `json:"RequestedModel"`
		RequestedModelSnake  string              `json:"requested_model"`
		Stream               bool                `json:"Stream"`
		StreamSnake          bool                `json:"stream"`
		RequestHeaders       map[string][]string `json:"RequestHeaders"`
		RequestHeadersSnake  map[string][]string `json:"request_headers"`
		ResponseHeaders      map[string][]string `json:"ResponseHeaders"`
		ResponseHeadersSnake map[string][]string `json:"response_headers"`
		OriginalRequest      []byte              `json:"OriginalRequest"`
		OriginalRequestSnake []byte              `json:"original_request"`
		RequestBody          []byte              `json:"RequestBody"`
		RequestBodySnake     []byte              `json:"request_body"`
		Body                 []byte              `json:"Body"`
		BodySnake            []byte              `json:"body"`
		StatusCode           int                 `json:"StatusCode"`
		StatusCodeSnake      int                 `json:"status_code"`
		Metadata             map[string]any      `json:"Metadata"`
		MetadataSnake        map[string]any      `json:"metadata"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.SourceFormat = firstNonEmpty(wire.SourceFormat, wire.SourceFormatSnake)
	r.Model = firstNonEmpty(wire.Model, wire.ModelSnake)
	r.RequestedModel = firstNonEmpty(wire.RequestedModel, wire.RequestedModelSnake)
	r.Stream = wire.Stream || wire.StreamSnake
	r.RequestHeaders = firstHeaderMap(wire.RequestHeaders, wire.RequestHeadersSnake)
	r.ResponseHeaders = firstHeaderMap(wire.ResponseHeaders, wire.ResponseHeadersSnake)
	r.OriginalRequest = firstBytes(wire.OriginalRequest, wire.OriginalRequestSnake)
	r.RequestBody = firstBytes(wire.RequestBody, wire.RequestBodySnake)
	r.Body = firstBytes(wire.Body, wire.BodySnake)
	r.StatusCode = wire.StatusCode
	if r.StatusCode == 0 {
		r.StatusCode = wire.StatusCodeSnake
	}
	r.Metadata = wire.Metadata
	if r.Metadata == nil {
		r.Metadata = wire.MetadataSnake
	}
	return nil
}

func handleResponseIntercept(requestBody []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return nil, fmt.Errorf("failed to parse response intercept request: %w", err)
	}
	if record, ok := usageRecordFromResponseIntercept(req); ok && usageFallbacks != nil {
		usageFallbacks.Schedule(record)
	}
	return okEnvelopeJSON("{}")
}

func handleResponseStreamChunk(requestBody []byte) ([]byte, error) {
	var req ResponseStreamChunkRequest
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return nil, fmt.Errorf("failed to parse response stream chunk request: %w", err)
	}
	if record, ok := usageRecordFromResponseStreamChunk(req); ok && usageFallbacks != nil {
		usageFallbacks.Supersede(supersededStreamUsageFingerprints(req))
		usageFallbacks.Schedule(record)
	}
	return okEnvelopeJSON("{}")
}

// supersededStreamUsageFingerprints returns dedup fingerprints for usage
// payloads carried by earlier chunks of the same stream. A later usage chunk
// (e.g. providers that attach running totals to every chunk, or Codex emitting
// usage on multiple response events) supersedes those pending fallbacks so
// only the most recent usage snapshot of the stream is committed.
func supersededStreamUsageFingerprints(req ResponseStreamChunkRequest) []string {
	if len(req.HistoryChunks) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, 2)
	keys := make([]string, 0, 2)
	for _, chunk := range req.HistoryChunks {
		if len(bytes.TrimSpace(chunk)) == 0 {
			continue
		}
		record, ok := usageRecordFromStreamValues(req.ResponseInterceptRequest, responseJSONValues(chunk))
		if !ok {
			continue
		}
		key := usageRecordFingerprint(record)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func usageRecordFromResponseIntercept(req ResponseInterceptRequest) (UsageRecord, bool) {
	if req.Stream || req.StatusCode < 200 || req.StatusCode >= 300 || len(bytes.TrimSpace(req.Body)) == 0 {
		return UsageRecord{}, false
	}
	responseValues := responseJSONValues(req.Body)
	return usageRecordFromResponseValues(req, responseValues)
}

func usageRecordFromResponseStreamChunk(req ResponseStreamChunkRequest) (UsageRecord, bool) {
	if req.StatusCode != 0 && (req.StatusCode < 200 || req.StatusCode >= 300) {
		return UsageRecord{}, false
	}
	if len(bytes.TrimSpace(req.Body)) == 0 {
		return UsageRecord{}, false
	}
	return usageRecordFromStreamValues(req.ResponseInterceptRequest, responseJSONValues(req.Body))
}

func usageRecordFromResponseValues(req ResponseInterceptRequest, responseValues []any) (UsageRecord, bool) {
	return usageRecordFromValues(req, responseValues, usageDetailPaths)
}

// usageRecordFromStreamValues mirrors usageRecordFromResponseValues but skips
// the message_start-style "message.usage" path: in Claude streams that node
// only carries the pre-generation input snapshot and would schedule a phantom
// fallback that never matches the final usage of the request.
func usageRecordFromStreamValues(req ResponseInterceptRequest, responseValues []any) (UsageRecord, bool) {
	return usageRecordFromValues(req, responseValues, usageDetailStreamPaths)
}

func usageRecordFromValues(req ResponseInterceptRequest, responseValues []any, detailPaths []string) (UsageRecord, bool) {
	detail, ok := usageDetailFromResponseValues(responseValues, detailPaths)
	if !ok {
		return UsageRecord{}, false
	}
	requestRoot, _ := decodeJSONValue(firstBytes(req.RequestBody, req.OriginalRequest))
	authID := firstNonEmpty(metadataString(req.Metadata, "selected_auth_id"), metadataString(req.Metadata, "pinned_auth_id"))
	// selected_auth_id is what CPA's conductor actually publishes and encodes
	// the upstream kind unambiguously; the plain provider metadata keys are
	// speculative and must not override it (a generic value like "claude"
	// would flip both grouping and cache accounting for compat upstreams).
	provider := firstNonEmpty(
		providerFromSelectedAuthID(req.Metadata),
		metadataString(req.Metadata, "upstream_provider", "provider", "selected_provider"),
		fallbackUsageProvider(req),
	)
	if responseUsesAnthropicUsageAccounting(req) {
		detail = normalizeAnthropicUsageDetail(detail, usageProviderFamily(provider))
	}
	model := firstNonEmpty(
		jsonStringPathFromValues(responseValues, "model", "response.model", "message.model"),
		req.Model,
		req.RequestedModel,
		jsonStringPath(requestRoot, "model"),
		"unknown",
	)
	requestedModel := firstNonEmpty(
		req.RequestedModel,
		metadataString(req.Metadata, "requested_model"),
		jsonStringPath(requestRoot, "model"),
		model,
	)
	return UsageRecord{
		Provider:        provider,
		ExecutorType:    "ResponseInterceptorFallback",
		Model:           model,
		Alias:           requestedModel,
		APIKey:          apiKeyFromHeaders(req.RequestHeaders),
		AuthID:          authID,
		AuthIndex:       fallbackAuthIndex(req.Metadata, authID),
		AuthType:        fallbackAuthType(req.Metadata, authID),
		ReasoningEffort: metadataString(req.Metadata, "reasoning_effort"),
		ServiceTier:     firstNonEmpty(metadataString(req.Metadata, "service_tier"), jsonStringPath(requestRoot, "service_tier")),
		RequestedAt:     time.Now(),
		Detail:          detail,
		BaseURL:         metadataString(req.Metadata, "upstream_base_url", "provider_base_url", "base_url", "baseURL"),
		Source:          metadataString(req.Metadata, "upstream_source", "provider_source", "selected_source"),
		ResponseHeaders: req.ResponseHeaders,
	}, true
}

func responseUsesAnthropicUsageAccounting(req ResponseInterceptRequest) bool {
	value := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	return strings.Contains(value, "anthropic") || strings.Contains(value, "claude")
}

// normalizeAnthropicUsageDetail aligns a Claude-shaped usage payload
// (input_tokens excludes cache reads/creations) with the accounting of the
// native usage record CPA produces for the same request, so the dedup
// fingerprints line up:
//   - Claude-family upstreams keep the exclusive input and count cache into
//     the total, mirroring CPA's native Claude usage parser.
//   - Every other upstream (openai-compatible, codex, ...) reports
//     prompt_tokens with cache included, so cache is folded into input.
func normalizeAnthropicUsageDetail(detail UsageDetail, providerFamily string) UsageDetail {
	cacheInput := detail.CacheReadTokens + detail.CacheCreationTokens
	if cacheInput <= 0 {
		return detail
	}
	if providerFamily == "claude" {
		expanded := detail.InputTokens + detail.OutputTokens + cacheInput
		if detail.TotalTokens < expanded {
			detail.TotalTokens = expanded
		}
		return detail
	}
	detail.InputTokens += cacheInput
	if detail.TotalTokens != 0 && detail.TotalTokens < detail.InputTokens+detail.OutputTokens {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	}
	return detail
}

func responseJSONValues(body []byte) []any {
	if root, ok := decodeJSONValue(body); ok {
		return []any{root}
	}
	return decodeSSEJSONValues(body)
}

func decodeSSEJSONValues(body []byte) []any {
	lines := strings.Split(string(body), "\n")
	values := make([]any, 0)
	var data strings.Builder
	flush := func() {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" || raw == "[DONE]" {
			return
		}
		if value, ok := decodeJSONValue([]byte(raw)); ok {
			values = append(values, value)
			return
		}
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "[DONE]" {
				continue
			}
			if value, ok := decodeJSONValue([]byte(line)); ok {
				values = append(values, value)
			}
		}
	}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	flush()
	return values
}

func usageDetailFromResponseValues(values []any, detailPaths []string) (UsageDetail, bool) {
	var best UsageDetail
	var found bool
	for _, value := range values {
		detail, ok := usageDetailFromResponseRoot(value, detailPaths)
		if !ok {
			continue
		}
		if !found || usageDetailCompleteness(detail) >= usageDetailCompleteness(best) {
			best = detail
			found = true
		}
	}
	return best, found
}

func usageDetailCompleteness(detail UsageDetail) int64 {
	return absInt64(detail.TotalTokens) +
		absInt64(detail.InputTokens) +
		absInt64(detail.OutputTokens) +
		absInt64(detail.ReasoningTokens) +
		absInt64(detail.CachedTokens) +
		absInt64(detail.CacheReadTokens) +
		absInt64(detail.CacheCreationTokens)
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// usageDetailPaths lists the JSON paths probed for usage payloads in complete
// (non-stream) response bodies.
var usageDetailPaths = []string{
	"usage",
	"response.usage",
	"message.usage",
	"total_usage",
	"metadata.usage",
	"metadata.total_usage",
	"usageMetadata",
	"usage_metadata",
	"response.usageMetadata",
	"response.usage_metadata",
}

// usageDetailStreamPaths is usageDetailPaths without "message.usage": in a
// Claude SSE stream that path only appears on message_start, whose usage is a
// pre-generation snapshot rather than the request's final usage.
var usageDetailStreamPaths = []string{
	"usage",
	"response.usage",
	"total_usage",
	"metadata.usage",
	"metadata.total_usage",
	"usageMetadata",
	"usage_metadata",
	"response.usageMetadata",
	"response.usage_metadata",
}

func usageDetailFromResponseRoot(root any, detailPaths []string) (UsageDetail, bool) {
	for _, path := range detailPaths {
		if node, ok := jsonValuePath(root, path); ok {
			if detail, ok := usageDetailFromValue(node); ok {
				return detail, true
			}
		}
	}
	return UsageDetail{}, false
}

func usageDetailFromValue(value any) (UsageDetail, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return UsageDetail{}, false
	}
	var detail UsageDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return UsageDetail{}, false
	}
	if m, ok := value.(map[string]any); ok {
		if detail.InputTokens == 0 {
			detail.InputTokens = firstJSONInt(m, "promptTokenCount", "inputTokenCount", "input_tokens", "prompt_tokens", "total_input_tokens")
		}
		if detail.OutputTokens == 0 {
			detail.OutputTokens = firstJSONInt(m, "candidatesTokenCount", "outputTokenCount", "output_tokens", "completion_tokens", "total_output_tokens")
		}
		if detail.ReasoningTokens == 0 {
			detail.ReasoningTokens = firstJSONInt(m, "thoughtsTokenCount", "reasoning_tokens", "total_thought_tokens")
		}
		if detail.ReasoningTokens == 0 {
			detail.ReasoningTokens = firstNestedJSONInt(m, "reasoning_tokens", "completion_tokens_details", "output_tokens_details")
		}
		if detail.CachedTokens == 0 {
			detail.CachedTokens = firstJSONInt(m, "cachedContentTokenCount", "cached_tokens", "total_cached_tokens")
		}
		if detail.CachedTokens == 0 {
			detail.CachedTokens = firstNestedJSONInt(m, "cached_tokens", "prompt_tokens_details", "input_tokens_details")
		}
		if detail.CacheReadTokens == 0 {
			detail.CacheReadTokens = firstJSONInt(m, "cache_read_tokens", "cacheReadTokens", "cache_read_input_tokens")
		}
		if detail.CacheCreationTokens == 0 {
			detail.CacheCreationTokens = firstJSONInt(m, "cache_creation_tokens", "cacheCreationTokens", "cache_creation_input_tokens")
		}
		if detail.TotalTokens == 0 {
			detail.TotalTokens = firstJSONInt(m, "totalTokenCount", "total_tokens")
		}
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	}
	if !usageDetailHasTokens(detail) {
		return UsageDetail{}, false
	}
	return detail, true
}

func usageDetailHasTokens(detail UsageDetail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}

type usageFallbackCoordinator struct {
	mu             sync.Mutex
	pending        map[string][]*pendingUsageFallback
	nativeRecent   map[string][]usageFallbackOccurrence
	fallbackRecent map[string][]usageFallbackOccurrence
	closed         bool
}

type pendingUsageFallback struct {
	key       string
	record    UsageRecord
	requestAt time.Time
	timer     *time.Timer
	cancelled bool
}

type usageFallbackOccurrence struct {
	requestAt  time.Time
	observedAt time.Time
}

func newUsageFallbackCoordinator() *usageFallbackCoordinator {
	return &usageFallbackCoordinator{
		pending:        make(map[string][]*pendingUsageFallback),
		nativeRecent:   make(map[string][]usageFallbackOccurrence),
		fallbackRecent: make(map[string][]usageFallbackOccurrence),
	}
}

func (c *usageFallbackCoordinator) Schedule(record UsageRecord) {
	if c == nil {
		return
	}
	key := usageRecordFingerprint(record)
	if key == "" {
		return
	}
	requestAt := record.RequestedAt
	if requestAt.IsZero() {
		requestAt = time.Now()
		record.RequestedAt = requestAt
	}
	delay := usageFallbackRecordDelay
	if delay < 0 {
		delay = 0
	}
	pending := &pendingUsageFallback{key: key, record: record, requestAt: requestAt}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	c.cleanupLocked(now)
	if c.consumeNativeRecentLocked(key, requestAt, now) {
		c.mu.Unlock()
		return
	}
	c.pending[key] = append(c.pending[key], pending)
	pending.timer = time.AfterFunc(delay, func() {
		c.commit(pending)
	})
	c.mu.Unlock()
}

func (c *usageFallbackCoordinator) HandleNative(record UsageRecord) bool {
	if c == nil {
		return true
	}
	key := usageRecordFingerprint(record)
	if key == "" {
		return true
	}
	requestAt := record.RequestedAt
	if requestAt.IsZero() {
		requestAt = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.cleanupLocked(now)
	if pending := c.popPendingLocked(key); pending != nil {
		pending.cancelled = true
		if pending.timer != nil {
			pending.timer.Stop()
		}
		return true
	}
	if c.matchesFallbackRecentLocked(key, requestAt, now) {
		return false
	}
	c.nativeRecent[key] = append(c.nativeRecent[key], usageFallbackOccurrence{requestAt: requestAt, observedAt: now})
	return true
}

// Supersede cancels pending fallbacks whose fingerprints were derived from
// earlier usage-bearing chunks of the same stream; the caller schedules a
// fresher snapshot right after. Fallbacks already committed cannot be
// retracted — late native records still reconcile through fallbackRecent.
func (c *usageFallbackCoordinator) Supersede(keys []string) {
	if c == nil || len(keys) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if pending := c.popPendingLocked(key); pending != nil {
			pending.cancelled = true
			if pending.timer != nil {
				pending.timer.Stop()
			}
		}
	}
}

func (c *usageFallbackCoordinator) Flush() {
	if c == nil {
		return
	}
	var records []UsageRecord
	c.mu.Lock()
	c.closed = true
	for key, pending := range c.pending {
		for _, item := range pending {
			if item == nil || item.cancelled {
				continue
			}
			item.cancelled = true
			if item.timer != nil {
				item.timer.Stop()
			}
			records = append(records, item.record)
		}
		delete(c.pending, key)
	}
	c.mu.Unlock()
	for _, record := range records {
		stats.Record(record)
	}
}

func (c *usageFallbackCoordinator) commit(pending *pendingUsageFallback) {
	if c == nil || pending == nil {
		return
	}
	c.mu.Lock()
	if c.closed || pending.cancelled {
		c.removePendingLocked(pending)
		c.mu.Unlock()
		return
	}
	pending.cancelled = true
	c.removePendingLocked(pending)
	now := time.Now()
	c.fallbackRecent[pending.key] = append(c.fallbackRecent[pending.key], usageFallbackOccurrence{
		requestAt:  pending.requestAt,
		observedAt: now,
	})
	c.cleanupLocked(now)
	record := pending.record
	c.mu.Unlock()
	// A native record for the same credential may have arrived while this
	// fallback was waiting; prefer the CPA auth index it taught us.
	if record.AuthID != "" && record.AuthIndex == safeCredentialIdentity(record.AuthID) {
		if learned := authIndexes.Lookup(record.AuthID); learned != "" {
			record.AuthIndex = learned
		}
	}
	stats.Record(record)
}

func (c *usageFallbackCoordinator) popPendingLocked(key string) *pendingUsageFallback {
	items := c.pending[key]
	for i, item := range items {
		if item == nil || item.cancelled {
			continue
		}
		c.pending[key] = append(items[:i], items[i+1:]...)
		if len(c.pending[key]) == 0 {
			delete(c.pending, key)
		}
		return item
	}
	if len(items) == 0 {
		delete(c.pending, key)
	}
	return nil
}

func (c *usageFallbackCoordinator) removePendingLocked(pending *pendingUsageFallback) {
	if pending == nil {
		return
	}
	items := c.pending[pending.key]
	for i, item := range items {
		if item == pending {
			c.pending[pending.key] = append(items[:i], items[i+1:]...)
			if len(c.pending[pending.key]) == 0 {
				delete(c.pending, pending.key)
			}
			return
		}
	}
}

func (c *usageFallbackCoordinator) consumeNativeRecentLocked(key string, requestAt time.Time, now time.Time) bool {
	items := c.nativeRecent[key]
	for i, item := range items {
		if now.Sub(item.observedAt) > usageFallbackNativeRecentWindow {
			continue
		}
		if !item.requestAt.IsZero() && item.requestAt.After(requestAt.Add(time.Second)) {
			continue
		}
		c.nativeRecent[key] = append(items[:i], items[i+1:]...)
		if len(c.nativeRecent[key]) == 0 {
			delete(c.nativeRecent, key)
		}
		return true
	}
	return false
}

func (c *usageFallbackCoordinator) matchesFallbackRecentLocked(key string, requestAt time.Time, now time.Time) bool {
	items := c.fallbackRecent[key]
	for i, item := range items {
		if now.Sub(item.observedAt) > usageFallbackLateNativeWindow {
			continue
		}
		if requestAt.IsZero() || !requestAt.After(item.requestAt.Add(time.Second)) {
			c.fallbackRecent[key] = append(items[:i], items[i+1:]...)
			if len(c.fallbackRecent[key]) == 0 {
				delete(c.fallbackRecent, key)
			}
			return true
		}
	}
	return false
}

func (c *usageFallbackCoordinator) cleanupLocked(now time.Time) {
	for key, items := range c.nativeRecent {
		kept := items[:0]
		for _, item := range items {
			if now.Sub(item.observedAt) <= usageFallbackNativeRecentWindow {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(c.nativeRecent, key)
		} else {
			c.nativeRecent[key] = kept
		}
	}
	for key, items := range c.fallbackRecent {
		kept := items[:0]
		for _, item := range items {
			if now.Sub(item.observedAt) <= usageFallbackLateNativeWindow {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(c.fallbackRecent, key)
		} else {
			c.fallbackRecent[key] = kept
		}
	}
	for key, items := range c.pending {
		kept := items[:0]
		for _, item := range items {
			if item != nil && !item.cancelled {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(c.pending, key)
		} else {
			c.pending[key] = kept
		}
	}
}

// usageRecordFingerprint keys native/fallback dedup. Provider is collapsed to
// its family for recognized auth IDs so a fallback still matches legacy native
// records that omitted auth_id. When a file-backed auth uses a custom filename
// whose provider cannot be inferred by the interceptor, auth ID becomes the
// upstream identity; both modern native records and interceptor metadata carry
// that scheduler-selected ID. This keeps custom file auths deduplicated without
// regressing older records. A fallback that only knows the generic
// "openai-compatible" upstream still matches the native record's specific
// "openai-compatible-<name>" provider. Token counts are canonicalized to
// cache-inclusive input with a recomputed total: Claude-family records keep
// input exclusive of cache reads/creations (both CPA's native parser and the
// fallback's Claude-format normalization do), while every other family
// already folds cache into input — adding the cache fields for Claude-family
// records makes the same request produce one fingerprint no matter which
// side, protocol shape, or total_tokens convention reported it. Reasoning
// effort and service tier are deliberately excluded: the two sides derive
// them from different sources and the token triple already discriminates
// requests.
func usageRecordFingerprint(record UsageRecord) string {
	if !usageDetailHasTokens(record.Detail) {
		return ""
	}
	upstreamIdentity := usageProviderFamily(record.Provider)
	if authID := strings.ToLower(strings.TrimSpace(record.AuthID)); authID != "" && providerFromAuthID(authID) == "" {
		upstreamIdentity = "auth:" + authID
	}
	inputTokens := record.Detail.InputTokens
	if usageProviderFamily(record.Provider) == "claude" {
		inputTokens += record.Detail.CacheReadTokens + record.Detail.CacheCreationTokens
	}
	outputTokens := record.Detail.OutputTokens
	parts := []string{
		upstreamIdentity,
		strings.ToLower(strings.TrimSpace(firstNonEmpty(record.Model, record.Alias))),
		canonicalClientAPIKey(record.APIKey),
		fmt.Sprintf("%d", inputTokens),
		fmt.Sprintf("%d", outputTokens),
		fmt.Sprintf("%d", inputTokens+outputTokens),
	}
	return strings.Join(parts, "\x00")
}

func usageProviderFamily(provider string) string {
	value := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case value == "":
		return ""
	case strings.HasPrefix(value, "openai-compatible") || strings.HasPrefix(value, "openai-compatibility"):
		return "openai-compatible"
	case value == "anthropic" || strings.HasPrefix(value, "anthropic-"):
		return "claude"
	case value == "claude" || strings.HasPrefix(value, "claude-"):
		return "claude"
	default:
		return value
	}
}

func fallbackUsageProvider(req ResponseInterceptRequest) string {
	source := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	switch {
	case source == "openai" || source == "openai-response" || source == "openai-responses":
		return "openai-compatible"
	case strings.Contains(source, "gemini"):
		return "gemini"
	case strings.Contains(source, "claude") || strings.Contains(source, "anthropic"):
		return "claude"
	case source != "":
		return strings.TrimSpace(req.SourceFormat)
	default:
		return "openai-compatible"
	}
}

func providerFromSelectedAuthID(meta map[string]any) string {
	authID := metadataString(meta, "selected_auth_id", "pinned_auth_id")
	return providerFromAuthID(authID)
}

func providerFromAuthID(authID string) string {
	value := strings.TrimSpace(authID)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	parts := strings.Split(value, ":")
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "openai-compatibility":
		if len(parts) < 2 {
			return "openai-compatible"
		}
		name := strings.TrimSpace(parts[1])
		if name != "" {
			return "openai-compatible-" + name
		}
	case "codex":
		return "codex"
	}
	if strings.HasPrefix(lower, "codex-") || strings.HasPrefix(lower, "codex_") || strings.HasPrefix(lower, "codex.") {
		return "codex"
	}
	// File-backed auth IDs are normally their JSON filenames. Generated OAuth
	// and Vertex credentials use a provider prefix, and nested auth directories
	// may leave a relative path in the ID. Recognize every native file-backed
	// provider registered by CPA instead of falling back to the client protocol.
	fileID := lower
	if index := strings.LastIndexAny(fileID, "/\\"); index >= 0 {
		fileID = fileID[index+1:]
	}
	switch {
	case authIDHasProviderPrefix(fileID, "claude"), authIDHasProviderPrefix(fileID, "anthropic"):
		return "claude"
	case authIDHasProviderPrefix(fileID, "kimi"):
		return "kimi"
	case authIDHasProviderPrefix(fileID, "xai"), authIDHasProviderPrefix(fileID, "grok"):
		return "xai"
	case authIDHasProviderPrefix(fileID, "vertex"):
		return "vertex"
	case authIDHasProviderPrefix(fileID, "aistudio"):
		return "aistudio"
	case authIDHasProviderPrefix(fileID, "antigravity"):
		return "antigravity"
	case authIDHasProviderPrefix(fileID, "gemini"), authIDHasProviderPrefix(fileID, "geminicli"):
		return "gemini"
	}
	return ""
}

// authIDHasProviderPrefix checks whether authID starts with provider followed by a
// recognised separator (-, _, ., :). Note: '@' is deliberately not included as a
// separator because CPA-generated file auth IDs always use '-' (e.g.
// claude-user@example.com.json). An auth ID like claude@example.com.json (without a
// trailing '-' after the provider) will not match — this is safe by construction.
func authIDHasProviderPrefix(authID, provider string) bool {
	if authID == provider {
		return true
	}
	if !strings.HasPrefix(authID, provider) || len(authID) <= len(provider) {
		return false
	}
	switch authID[len(provider)] {
	case '-', '_', '.', ':':
		return true
	default:
		return false
	}
}

func fallbackAuthIndex(meta map[string]any, authID string) string {
	if index := metadataString(meta, "auth_index", "selected_auth_index", "pinned_auth_index"); index != "" {
		return index
	}
	if learned := authIndexes.Lookup(authID); learned != "" {
		return learned
	}
	return safeCredentialIdentity(authID)
}

func fallbackAuthType(meta map[string]any, authID string) string {
	if authType := metadataString(meta, "auth_type", "selected_auth_type", "pinned_auth_type"); authType != "" {
		return authType
	}
	value := strings.TrimSpace(authID)
	parts := strings.Split(value, ":")
	if len(parts) == 0 {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "openai-compatibility":
		return "apikey"
	case "codex":
		if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[1]), "apikey") {
			return "apikey"
		}
		return "oauth"
	default:
		provider := providerFromAuthID(authID)
		switch provider {
		case "claude", "codex", "kimi", "xai", "aistudio", "antigravity", "gemini", "vertex":
			return "oauth"
		}
		return ""
	}
}

func apiKeyFromHeaders(headers map[string][]string) string {
	auth := headerValue(headers, "Authorization")
	if auth != "" {
		fields := strings.Fields(auth)
		if len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
			return strings.TrimSpace(fields[1])
		}
		return strings.TrimSpace(auth)
	}
	return firstNonEmpty(
		headerValue(headers, "X-API-Key"),
		headerValue(headers, "X-Api-Key"),
		headerValue(headers, "X-Goog-Api-Key"),
	)
}

func headerValue(headers map[string][]string, name string) string {
	if len(headers) == 0 {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func decodeJSONValue(data []byte) (any, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

func jsonValuePath(root any, path string) (any, bool) {
	if root == nil || strings.TrimSpace(path) == "" {
		return nil, false
	}
	current := root
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func jsonStringPath(root any, path string) string {
	value, ok := jsonValuePath(root, path)
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func jsonStringPathFromValues(values []any, paths ...string) string {
	for _, value := range values {
		for _, path := range paths {
			if got := jsonStringPath(value, path); got != "" {
				return got
			}
		}
	}
	return ""
}

func metadataString(meta map[string]any, keys ...string) string {
	if len(meta) == 0 {
		return ""
	}
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		if value := metadataValueString(value); value != "" {
			return value
		}
	}
	return ""
}

func metadataValueString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	case json.Number:
		return strings.TrimSpace(v.String())
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func firstHeaderMap(values ...map[string][]string) map[string][]string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstBytes(values ...[]byte) []byte {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstJSONInt(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if n := jsonInt(value); n != 0 {
				return n
			}
		}
	}
	return 0
}

// firstNestedJSONInt reads key from the first of the given child objects that
// carries it, e.g. usage.prompt_tokens_details.cached_tokens.
func firstNestedJSONInt(m map[string]any, key string, parents ...string) int64 {
	for _, parent := range parents {
		child, ok := m[parent].(map[string]any)
		if !ok {
			continue
		}
		if n := firstJSONInt(child, key); n != 0 {
			return n
		}
	}
	return 0
}

func jsonInt(value any) int64 {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}
