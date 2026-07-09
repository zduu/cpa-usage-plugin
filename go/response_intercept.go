package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
)

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

func usageRecordFromResponseIntercept(req ResponseInterceptRequest) (UsageRecord, bool) {
	if req.Stream || req.StatusCode < 200 || req.StatusCode >= 300 || len(bytes.TrimSpace(req.Body)) == 0 {
		return UsageRecord{}, false
	}
	responseRoot, ok := decodeJSONValue(req.Body)
	if !ok {
		return UsageRecord{}, false
	}
	detail, ok := usageDetailFromResponseRoot(responseRoot)
	if !ok {
		return UsageRecord{}, false
	}
	requestRoot, _ := decodeJSONValue(firstBytes(req.RequestBody, req.OriginalRequest))
	model := firstNonEmpty(
		jsonStringPath(responseRoot, "model"),
		jsonStringPath(responseRoot, "response.model"),
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
		Provider:        firstNonEmpty(metadataString(req.Metadata, "upstream_provider", "provider", "selected_provider"), fallbackUsageProvider(req)),
		ExecutorType:    "ResponseInterceptorFallback",
		Model:           model,
		Alias:           requestedModel,
		APIKey:          apiKeyFromHeaders(req.RequestHeaders),
		AuthID:          firstNonEmpty(metadataString(req.Metadata, "selected_auth_id"), metadataString(req.Metadata, "pinned_auth_id")),
		ReasoningEffort: metadataString(req.Metadata, "reasoning_effort"),
		ServiceTier:     firstNonEmpty(metadataString(req.Metadata, "service_tier"), jsonStringPath(requestRoot, "service_tier")),
		RequestedAt:     time.Now(),
		Detail:          detail,
		BaseURL:         metadataString(req.Metadata, "upstream_base_url", "provider_base_url", "base_url", "baseURL"),
		Source:          metadataString(req.Metadata, "upstream_source", "provider_source", "selected_source"),
		ResponseHeaders: req.ResponseHeaders,
	}, true
}

func usageDetailFromResponseRoot(root any) (UsageDetail, bool) {
	for _, path := range []string{
		"usage",
		"response.usage",
		"total_usage",
		"metadata.usage",
		"metadata.total_usage",
		"usageMetadata",
		"usage_metadata",
		"response.usageMetadata",
		"response.usage_metadata",
	} {
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
		if detail.CachedTokens == 0 {
			detail.CachedTokens = firstJSONInt(m, "cachedContentTokenCount", "cached_tokens", "total_cached_tokens")
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

func usageRecordFingerprint(record UsageRecord) string {
	if !usageDetailHasTokens(record.Detail) {
		return ""
	}
	parts := []string{
		usageProviderFamily(record.Provider),
		strings.ToLower(strings.TrimSpace(firstNonEmpty(record.Model, record.Alias))),
		strings.TrimSpace(record.APIKey),
		strings.ToLower(strings.TrimSpace(record.ReasoningEffort)),
		strings.ToLower(strings.TrimSpace(record.ServiceTier)),
		fmt.Sprintf("%d", record.Detail.InputTokens),
		fmt.Sprintf("%d", record.Detail.OutputTokens),
		fmt.Sprintf("%d", usageDetailTotalTokens(record.Detail)),
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
