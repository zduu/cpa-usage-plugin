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
	responseInterceptorFallbackExecutor    = "ResponseInterceptorFallback"
	defaultUsageFallbackDelay              = 2 * time.Second
	usageFallbackNativeRecentWindow        = 750 * time.Millisecond
	usageFallbackCorrelationRetentionGrace = time.Second
	usageFallbackLateNativeWindow          = 30 * time.Second
)

var (
	usageFallbackRecordDelay = defaultUsageFallbackDelay
	// Current CPA publishes native usage even after client cancellation. Keep
	// legacy correlation helpers for history repair, but no live coordinator.
	usageFallbacks *usageFallbackCoordinator
	authIndexes    = newAuthIndexLearner()
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
	// The host stream-chunk ABI has no Stream field because this callback is
	// intrinsically streaming. Mark it explicitly for persisted throughput
	// calculations and native/fallback metadata reconciliation.
	r.Stream = true
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
	detail, correlation, ok := usageDetailCorrelationFromResponseValues(responseValues, detailPaths)
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
	correlation = correlationMetaForResponse(req, correlation, provider)
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
		Provider:            provider,
		ExecutorType:        responseInterceptorFallbackExecutor,
		Model:               model,
		Alias:               requestedModel,
		APIKey:              apiKeyFromHeaders(req.RequestHeaders),
		AuthID:              authID,
		AuthIndex:           fallbackAuthIndex(req.Metadata, authID),
		AuthType:            fallbackAuthType(req.Metadata, authID),
		Endpoint:            fallbackRequestEndpoint(req),
		ReasoningEffort:     fallbackReasoningEffort(req, requestRoot),
		ServiceTier:         firstNonEmpty(metadataString(req.Metadata, "service_tier"), jsonStringPath(requestRoot, "service_tier")),
		Stream:              req.Stream,
		RequestedAt:         time.Now(),
		Detail:              detail,
		BaseURL:             metadataString(req.Metadata, "upstream_base_url", "provider_base_url", "base_url", "baseURL"),
		Source:              metadataString(req.Metadata, "upstream_source", "provider_source", "selected_source"),
		ResponseHeaders:     req.ResponseHeaders,
		protocolCorrelation: correlation,
	}, true
}

func fallbackRequestEndpoint(req ResponseInterceptRequest) string {
	if endpoint := metadataString(req.Metadata, "request_path", "endpoint", "request_endpoint"); endpoint != "" {
		return endpoint
	}
	switch strings.ToLower(strings.TrimSpace(req.SourceFormat)) {
	case "openai":
		return "/v1/chat/completions"
	case "openai-response", "openai-responses":
		return "/v1/responses"
	default:
		return ""
	}
}

func fallbackReasoningEffort(req ResponseInterceptRequest, requestRoot any) string {
	return firstNonEmpty(
		metadataString(req.Metadata, "reasoning_effort"),
		jsonStringPath(requestRoot, "reasoning_effort"),
		jsonStringPath(requestRoot, "reasoning.effort"),
		jsonStringPath(requestRoot, "thinking.effort"),
		jsonStringPath(requestRoot, "thinking.level"),
	)
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
	cacheInput, ok := checkedProtocolAdd(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !ok {
		return detail
	}
	if cacheInput <= 0 {
		return detail
	}
	if providerFamily == "claude" {
		expanded, ok := checkedProtocolAdd(detail.InputTokens, detail.OutputTokens)
		if !ok {
			return detail
		}
		expanded, ok = checkedProtocolAdd(expanded, cacheInput)
		if !ok {
			return detail
		}
		if detail.TotalTokens < expanded {
			detail.TotalTokens = expanded
		}
		return detail
	}
	expandedInput, ok := checkedProtocolAdd(detail.InputTokens, cacheInput)
	if !ok {
		return detail
	}
	detail.InputTokens = expandedInput
	inputOutput, ok := checkedProtocolAdd(detail.InputTokens, detail.OutputTokens)
	if detail.TotalTokens != 0 && ok && detail.TotalTokens < inputOutput {
		detail.TotalTokens = inputOutput
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

// usageDetailCorrelationFromResponseValues is the evidence-preserving
// counterpart of usageDetailFromResponseValues.  The latter is kept as a
// small compatibility helper for callers that only need billing counters;
// correlation must select the metadata together with the most complete usage
// node or a later SSE chunk could pair values from one node with presence bits
// from another.
func usageDetailCorrelationFromResponseValues(values []any, detailPaths []string) (UsageDetail, *ProtocolCorrelationMeta, bool) {
	var best UsageDetail
	var bestMeta *ProtocolCorrelationMeta
	var found bool
	var bestCompleteness int64
	for _, value := range values {
		detail, meta, ok := usageDetailCorrelationFromResponseRoot(value, detailPaths)
		if !ok {
			continue
		}
		completeness := usageDetailCompleteness(detail)
		if !found || completeness >= bestCompleteness {
			best = detail
			bestMeta = meta
			bestCompleteness = completeness
			found = true
		}
	}
	return best, bestMeta, found
}

func usageDetailCorrelationFromResponseRoot(root any, detailPaths []string) (UsageDetail, *ProtocolCorrelationMeta, bool) {
	for _, path := range detailPaths {
		if node, ok := jsonValuePath(root, path); ok {
			if detail, meta, ok := usageDetailCorrelationFromValue(node); ok {
				return detail, meta, true
			}
		}
	}
	return UsageDetail{}, nil, false
}

func usageDetailCorrelationFromValue(value any) (UsageDetail, *ProtocolCorrelationMeta, bool) {
	detail, ok := usageDetailFromValue(value)
	meta := protocolCorrelationMetaFromUsageValue(value)
	// A valid usage object containing only explicit zeroes is still a real
	// observation.  The old numeric helper cannot distinguish that case from a
	// missing usage node, but the raw presence bits can.
	if !ok && (meta == nil || meta.KnownFields == 0) {
		return UsageDetail{}, nil, false
	}
	return detail, meta, true
}

func protocolCorrelationMetaFromUsageValue(value any) *ProtocolCorrelationMeta {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	meta := &ProtocolCorrelationMeta{
		SchemaVersion: protocolCorrelationSchemaVersion,
	}
	if jsonFieldPresent(m, "input_tokens", "InputTokens", "prompt_tokens", "PromptTokens", "inputTokenCount", "promptTokenCount", "total_input_tokens") {
		meta.KnownFields |= protocolCorrelationKnownInput
	}
	if jsonFieldPresent(m, "output_tokens", "OutputTokens", "completion_tokens", "CompletionTokens", "outputTokenCount", "candidatesTokenCount", "total_output_tokens") {
		meta.KnownFields |= protocolCorrelationKnownOutput
	}
	if jsonFieldPresent(m, "reasoning_tokens", "ReasoningTokens", "thoughtsTokenCount", "total_thought_tokens") ||
		jsonNestedFieldPresent(m, "reasoning_tokens", "ReasoningTokens", "thoughtsTokenCount") {
		meta.KnownFields |= protocolCorrelationKnownReasoning
	}
	if jsonFieldPresent(m, "cache_read_tokens", "CacheReadTokens", "cacheReadTokens", "cache_read_input_tokens", "CachedTokens") ||
		jsonNestedFieldPresent(m, "cached_tokens", "CachedTokens", "cache_read_tokens") {
		meta.KnownFields |= protocolCorrelationKnownCacheRead
	}
	if jsonFieldPresent(m, "cache_creation_tokens", "CacheCreationTokens", "cacheCreationTokens", "cache_creation_input_tokens", "cache_write_tokens", "CacheWriteTokens") ||
		jsonNestedFieldPresent(m, "cache_creation_tokens", "CacheCreationTokens", "cache_write_tokens", "CacheWriteTokens") {
		meta.KnownFields |= protocolCorrelationKnownCacheWrite
	}
	if jsonFieldPresent(m, "cachedContentTokenCount", "cached_content_token_count", "cache_tokens", "CacheTokens") {
		meta.KnownFields |= protocolCorrelationKnownCacheCombined
	}
	if jsonFieldPresent(m, "total_tokens", "TotalTokens", "totalTokenCount") {
		meta.KnownFields |= protocolCorrelationKnownTotal
	}
	if meta.KnownFields == 0 {
		return nil
	}
	return meta
}

func jsonFieldPresent(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func jsonNestedFieldPresent(m map[string]any, keys ...string) bool {
	for _, value := range m {
		child, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if jsonFieldPresent(child, keys...) {
			return true
		}
	}
	return false
}

func correlationMetaForResponse(req ResponseInterceptRequest, raw *ProtocolCorrelationMeta, provider string) *ProtocolCorrelationMeta {
	meta := cloneProtocolCorrelationMeta(raw)
	if meta == nil {
		meta = &ProtocolCorrelationMeta{SchemaVersion: protocolCorrelationSchemaVersion}
	}
	family := responseProtocolFamily(req, provider)
	switch family {
	case "claude":
		// Claude-shaped responses are normalized into the selected upstream's
		// accounting representation before this metadata is attached. For a
		// non-Claude upstream that normalization already folded split cache
		// buckets into InputTokens; emitting an additional Claude expansion shape
		// would double-expand the same cache and allow a false native match.
		if accountingFamily := correlationFamilyForProvider(provider); accountingFamily != "" && accountingFamily != "claude" {
			meta.InputMode = protocolInputModeCacheSubsetNormalized
		} else if meta.KnownFields&(protocolCorrelationKnownCacheRead|protocolCorrelationKnownCacheWrite|protocolCorrelationKnownCacheCombined) != 0 {
			meta.InputMode = protocolInputModeAmbiguous
		} else {
			meta.InputMode = protocolInputModeCacheIndependent
		}
		meta.OutputMode = protocolOutputModeReasoningSubset
		meta.CacheMode = protocolCacheModeSplit
	case "gemini":
		accountingFamily := correlationFamilyForProvider(provider)
		meta.InputMode = protocolInputModeCacheSubset
		if accountingFamily == "claude" {
			// Claude's native accounting excludes cache from InputTokens, so a
			// Gemini combined bucket must be expanded to form the comparable
			// full-input shape.
			meta.InputMode = protocolInputModeCacheIndependent
		} else if accountingFamily != "" {
			// Gemini promptTokenCount already includes cached content for the
			// non-Claude accounting families. Do not add a second expanded shape.
			meta.InputMode = protocolInputModeCacheSubsetNormalized
		}
		meta.OutputMode = protocolOutputModeAmbiguous
		if meta.KnownFields&protocolCorrelationKnownCacheCombined != 0 {
			meta.KnownFields &^= protocolCorrelationKnownCacheRead
			meta.CacheMode = protocolCacheModeCombined
		} else {
			meta.CacheMode = protocolCacheModeSplit
		}
	case "openai":
		meta.InputMode = protocolInputModeCacheSubset
		meta.OutputMode = protocolOutputModeReasoningSubset
		meta.CacheMode = protocolCacheModeSplit
	default:
		meta.InputMode = protocolInputModeAmbiguous
		meta.OutputMode = protocolOutputModeAmbiguous
		meta.CacheMode = protocolCacheModeAmbiguous
	}
	return meta
}

func responseProtocolFamily(req ResponseInterceptRequest, provider string) string {
	source := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	switch {
	case strings.Contains(source, "claude"), strings.Contains(source, "anthropic"):
		return "claude"
	case strings.Contains(source, "gemini"), strings.Contains(source, "google"):
		return "gemini"
	case strings.Contains(source, "openai"), strings.Contains(source, "codex"):
		return "openai"
	}
	endpoint := strings.ToLower(strings.TrimSpace(fallbackRequestEndpoint(req)))
	if strings.Contains(endpoint, "messages") {
		return "claude"
	}
	if strings.Contains(endpoint, "generatecontent") || strings.Contains(endpoint, "v1beta") {
		return "gemini"
	}
	if isOpenAIProtocolFallbackEndpoint(endpoint) {
		return "openai"
	}
	switch usageProviderFamily(provider) {
	case "claude":
		return "claude"
	case "gemini", "vertex", "aistudio", "antigravity":
		return "gemini"
	case "codex", "openai-compatible", "openai", "kimi", "xai", "deepseek", "openrouter":
		return "openai"
	default:
		return ""
	}
}

func usageDetailCompleteness(detail UsageDetail) int64 {
	completeness := int64(0)
	for _, value := range []int64{
		detail.TotalTokens, detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens,
		detail.CachedTokens, detail.CacheReadTokens, detail.CacheCreationTokens,
	} {
		completeness = saturatingProtocolAdd(completeness, absInt64(value))
	}
	return completeness
}

func absInt64(value int64) int64 {
	if value < 0 {
		if value == -1<<63 {
			return 1<<63 - 1
		}
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
		if detail.CacheCreationTokens == 0 {
			detail.CacheCreationTokens = firstNestedJSONInt(m, "cache_write_tokens", "prompt_tokens_details", "input_tokens_details")
		}
		if detail.CacheCreationTokens == 0 {
			detail.CacheCreationTokens = firstNestedJSONInt(m, "cache_creation_tokens", "prompt_tokens_details", "input_tokens_details")
		}
		if detail.TotalTokens == 0 {
			detail.TotalTokens = firstJSONInt(m, "totalTokenCount", "total_tokens")
		}
	}
	if detail.TotalTokens == 0 {
		if input, ok := jsonValueInt(value, "input_tokens", "InputTokens", "prompt_tokens", "PromptTokens", "promptTokenCount", "inputTokenCount", "total_input_tokens"); ok {
			if output, outputOK := jsonValueInt(value, "output_tokens", "OutputTokens", "completion_tokens", "CompletionTokens", "outputTokenCount", "candidatesTokenCount", "total_output_tokens"); outputOK {
				if total, totalOK := checkedProtocolAdd(input, output); totalOK {
					// An explicitly reported zero is presence, not an omitted
					// total.  Preserve it so the correlation adapter can apply
					// the protocol's own total semantics.
					if !jsonFieldPresentValue(value, "total_tokens", "TotalTokens", "totalTokenCount") {
						detail.TotalTokens = total
					}
				}
			}
		}
	}
	if !usageDetailHasTokens(detail) {
		return UsageDetail{}, false
	}
	return detail, true
}

func jsonFieldPresentValue(value any, keys ...string) bool {
	m, ok := value.(map[string]any)
	return ok && jsonFieldPresent(m, keys...)
}

func jsonValueInt(value any, keys ...string) (int64, bool) {
	m, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	for _, key := range keys {
		candidate, exists := m[key]
		if !exists {
			continue
		}
		return jsonInt(candidate), true
	}
	return 0, false
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
	mu                       sync.Mutex
	pending                  map[string][]*pendingUsageFallback
	pendingCorrelated        map[string][]*pendingUsageFallback
	nativeRecent             map[string][]*usageFallbackOccurrence
	nativeRecentCorrelated   map[string][]*usageFallbackOccurrence
	fallbackRecent           map[string][]*usageFallbackOccurrence
	fallbackRecentCorrelated map[string][]*usageFallbackOccurrence
	closed                   bool
}

type pendingUsageFallback struct {
	key             string
	correlationKey  string
	correlationKeys []string
	record          UsageRecord
	requestAt       time.Time
	timer           *time.Timer
	cancelled       bool
}

type usageFallbackOccurrence struct {
	key             string
	correlationKey  string
	correlationKeys []string
	requestAt       time.Time
	observedAt      time.Time
	record          UsageRecord
}

func newUsageFallbackCoordinator() *usageFallbackCoordinator {
	return &usageFallbackCoordinator{
		pending:                  make(map[string][]*pendingUsageFallback),
		pendingCorrelated:        make(map[string][]*pendingUsageFallback),
		nativeRecent:             make(map[string][]*usageFallbackOccurrence),
		nativeRecentCorrelated:   make(map[string][]*usageFallbackOccurrence),
		fallbackRecent:           make(map[string][]*usageFallbackOccurrence),
		fallbackRecentCorrelated: make(map[string][]*usageFallbackOccurrence),
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
	correlationKeys := []string(nil)
	if isAnonymousProtocolFallback(record) {
		correlationKeys = protocolObservationShapeKeys(protocolObservationFromUsageRecord(record))
	}
	correlationKey := firstNonEmpty(correlationKeys...)
	pending := &pendingUsageFallback{key: key, correlationKey: correlationKey, correlationKeys: correlationKeys, record: record, requestAt: requestAt}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	c.cleanupLocked(now)
	if nativeRecord, ok := c.consumeNativeRecentLocked(key, requestAt, now, record); ok {
		c.mu.Unlock()
		stats.EnrichRecordedUsage(nativeRecord, record)
		return
	}
	if len(correlationKeys) > 0 {
		if nativeRecord, ok := c.consumeCorrelatedNativeRecentLocked(correlationKeys, record, now); ok {
			c.mu.Unlock()
			stats.EnrichRecordedUsage(nativeRecord, record)
			return
		}
	}
	c.pending[key] = append(c.pending[key], pending)
	for _, correlationKey := range correlationKeys {
		if correlationKey != "" {
			c.pendingCorrelated[correlationKey] = append(c.pendingCorrelated[correlationKey], pending)
		}
	}
	pending.timer = time.AfterFunc(delay, func() {
		c.commit(pending)
	})
	c.mu.Unlock()
}

func (c *usageFallbackCoordinator) HandleNative(record UsageRecord) (UsageRecord, bool) {
	if c == nil {
		return record, true
	}
	key := usageRecordFingerprint(record)
	if key == "" {
		return record, true
	}
	requestAt := record.RequestedAt
	if requestAt.IsZero() {
		requestAt = time.Now()
		record.RequestedAt = requestAt
	}
	correlationKeys := []string(nil)
	if isNativeProtocolCorrelationRecord(record) {
		correlationKeys = protocolObservationShapeKeys(protocolObservationFromUsageRecord(record))
	}
	c.mu.Lock()
	now := time.Now()
	c.cleanupLocked(now)
	if pending := c.popPendingForNativeLocked(key, record); pending != nil {
		pending.cancelled = true
		if pending.timer != nil {
			pending.timer.Stop()
		}
		c.mu.Unlock()
		return enrichUsageRecord(record, pending.record), true
	}
	if len(correlationKeys) > 0 {
		if pending := c.popCorrelatedPendingLocked(correlationKeys, record); pending != nil {
			pending.cancelled = true
			if pending.timer != nil {
				pending.timer.Stop()
			}
			c.mu.Unlock()
			return enrichUsageRecord(record, pending.record), true
		}
	}
	if fallbackRecord, ok := c.consumeFallbackRecentLocked(key, requestAt, now, record); ok {
		c.mu.Unlock()
		if stats.RemoveRecordedUsage(fallbackRecord) {
			return enrichUsageRecord(record, fallbackRecord), true
		}
		return record, false
	}
	if len(correlationKeys) > 0 {
		if fallbackRecord, ok := c.consumeCorrelatedFallbackRecentLocked(correlationKeys, record, now); ok {
			c.mu.Unlock()
			if stats.RemoveRecordedUsage(fallbackRecord) {
				return enrichUsageRecord(record, fallbackRecord), true
			}
			return record, false
		}
	}
	occurrence := &usageFallbackOccurrence{
		key: key, correlationKey: firstNonEmpty(correlationKeys...), correlationKeys: correlationKeys,
		requestAt: requestAt, observedAt: now, record: record,
	}
	c.nativeRecent[key] = append(c.nativeRecent[key], occurrence)
	for _, correlationKey := range correlationKeys {
		if correlationKey != "" {
			c.nativeRecentCorrelated[correlationKey] = append(c.nativeRecentCorrelated[correlationKey], occurrence)
		}
	}
	c.mu.Unlock()
	return record, true
}

func enrichUsageRecord(record UsageRecord, enrichment UsageRecord) UsageRecord {
	if strings.TrimSpace(record.Alias) == "" && strings.TrimSpace(enrichment.Alias) != "" {
		record.Alias = strings.TrimSpace(enrichment.Alias)
	}
	if strings.TrimSpace(record.Endpoint) == "" {
		record.Endpoint = strings.TrimSpace(enrichment.Endpoint)
	}
	if strings.TrimSpace(record.ReasoningEffort) == "" {
		record.ReasoningEffort = strings.TrimSpace(enrichment.ReasoningEffort)
	}
	if enrichment.Stream {
		record.Stream = true
	}
	return record
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
	c.pendingCorrelated = make(map[string][]*pendingUsageFallback)
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
	occurrence := &usageFallbackOccurrence{
		key:             pending.key,
		correlationKey:  pending.correlationKey,
		correlationKeys: pending.correlationKeys,
		requestAt:       pending.requestAt,
		observedAt:      now,
		record:          pending.record,
	}
	c.fallbackRecent[pending.key] = append(c.fallbackRecent[pending.key], occurrence)
	for _, correlationKey := range pending.correlationKeys {
		if correlationKey != "" {
			c.fallbackRecentCorrelated[correlationKey] = append(c.fallbackRecentCorrelated[correlationKey], occurrence)
		}
	}
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
		c.removeCorrelatedPendingLocked(item)
		return item
	}
	if len(items) == 0 {
		delete(c.pending, key)
	}
	return nil
}

func (c *usageFallbackCoordinator) popPendingForNativeLocked(key string, native UsageRecord) *pendingUsageFallback {
	items := c.pending[key]
	var selected *pendingUsageFallback
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, item := range items {
		if item == nil || item.cancelled {
			continue
		}
		edge, ok := protocolLiveCorrelationEdge(item.record, native)
		if !ok {
			continue
		}
		if selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance,
			protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance,
			protocolObservationFromUsageRecord(selected.record).StableOrder) {
			continue
		}
		selected = item
		selectedDistance = edge.Distance
		selectedStrength = edge.Strength
	}
	if selected != nil {
		c.removePendingLocked(selected)
	}
	return selected
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
			c.removeCorrelatedPendingLocked(pending)
			return
		}
	}
	c.removeCorrelatedPendingLocked(pending)
}

func (c *usageFallbackCoordinator) removeCorrelatedPendingLocked(pending *pendingUsageFallback) {
	if pending == nil {
		return
	}
	for _, correlationKey := range pending.correlationKeys {
		if correlationKey == "" {
			continue
		}
		items := c.pendingCorrelated[correlationKey]
		for i, item := range items {
			if item != pending {
				continue
			}
			c.pendingCorrelated[correlationKey] = append(items[:i], items[i+1:]...)
			if len(c.pendingCorrelated[correlationKey]) == 0 {
				delete(c.pendingCorrelated, correlationKey)
			}
			break
		}
	}
}

func (c *usageFallbackCoordinator) popCorrelatedPendingLocked(correlationKeys []string, native UsageRecord) *pendingUsageFallback {
	seen := make(map[*pendingUsageFallback]struct{})
	var selected *pendingUsageFallback
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, correlationKey := range correlationKeys {
		for _, item := range c.pendingCorrelated[correlationKey] {
			if item == nil || item.cancelled {
				continue
			}
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			edge, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(item.record), protocolObservationFromUsageRecord(native))
			if !ok || (selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance, protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance, protocolObservationFromUsageRecord(selected.record).StableOrder)) {
				continue
			}
			selected = item
			selectedDistance = edge.Distance
			selectedStrength = edge.Strength
		}
	}
	if selected != nil {
		c.removePendingLocked(selected)
	}
	return selected
}

func (c *usageFallbackCoordinator) consumeNativeRecentLocked(key string, requestAt time.Time, now time.Time, fallback UsageRecord) (UsageRecord, bool) {
	items := c.nativeRecent[key]
	var selected *usageFallbackOccurrence
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, item := range items {
		if item == nil {
			continue
		}
		if now.Sub(item.observedAt) > nativeRecentRetention(item) {
			continue
		}
		edge, ok := protocolLiveCorrelationEdge(fallback, item.record)
		if !ok {
			continue
		}
		if selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance,
			protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance,
			protocolObservationFromUsageRecord(selected.record).StableOrder) {
			continue
		}
		selected = item
		selectedDistance = edge.Distance
		selectedStrength = edge.Strength
	}
	if selected != nil {
		c.removeNativeRecentLocked(selected)
		return selected.record, true
	}
	return UsageRecord{}, false
}

func (c *usageFallbackCoordinator) consumeFallbackRecentLocked(key string, requestAt time.Time, now time.Time, native UsageRecord) (UsageRecord, bool) {
	items := c.fallbackRecent[key]
	var selected *usageFallbackOccurrence
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, item := range items {
		if item == nil {
			continue
		}
		if now.Sub(item.observedAt) > usageFallbackLateNativeWindow {
			continue
		}
		edge, ok := protocolLiveCorrelationEdge(item.record, native)
		if !ok {
			continue
		}
		if selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance,
			protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance,
			protocolObservationFromUsageRecord(selected.record).StableOrder) {
			continue
		}
		selected = item
		selectedDistance = edge.Distance
		selectedStrength = edge.Strength
	}
	if selected != nil {
		c.removeFallbackRecentLocked(selected)
		return selected.record, true
	}
	return UsageRecord{}, false
}

func (c *usageFallbackCoordinator) consumeCorrelatedNativeRecentLocked(correlationKeys []string, fallback UsageRecord, now time.Time) (UsageRecord, bool) {
	seen := make(map[*usageFallbackOccurrence]struct{})
	var selected *usageFallbackOccurrence
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, correlationKey := range correlationKeys {
		for _, item := range c.nativeRecentCorrelated[correlationKey] {
			if item == nil || now.Sub(item.observedAt) > nativeRecentRetention(item) {
				continue
			}
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			edge, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(fallback), protocolObservationFromUsageRecord(item.record))
			if !ok || (selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance, protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance, protocolObservationFromUsageRecord(selected.record).StableOrder)) {
				continue
			}
			selected = item
			selectedDistance = edge.Distance
			selectedStrength = edge.Strength
		}
	}
	if selected == nil {
		return UsageRecord{}, false
	}
	c.removeNativeRecentLocked(selected)
	return selected.record, true
}

func nativeRecentRetention(item *usageFallbackOccurrence) time.Duration {
	if item == nil || item.correlationKey == "" {
		return usageFallbackNativeRecentWindow
	}
	// Derive retention from the same latency-aware policy that validates the
	// pair, then leave a small allowance for callback scheduling. Retention only
	// keeps a candidate reachable; it never expands the accepted time distance.
	return protocolFallbackTolerance(item.record.Latency) + usageFallbackCorrelationRetentionGrace
}

func (c *usageFallbackCoordinator) consumeCorrelatedFallbackRecentLocked(correlationKeys []string, native UsageRecord, now time.Time) (UsageRecord, bool) {
	seen := make(map[*usageFallbackOccurrence]struct{})
	var selected *usageFallbackOccurrence
	var selectedDistance time.Duration
	var selectedStrength int64
	for _, correlationKey := range correlationKeys {
		for _, item := range c.fallbackRecentCorrelated[correlationKey] {
			if item == nil || now.Sub(item.observedAt) > usageFallbackLateNativeWindow {
				continue
			}
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			edge, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(item.record), protocolObservationFromUsageRecord(native))
			if !ok || (selected != nil && !betterProtocolLiveCandidate(edge.Strength, edge.Distance, protocolObservationFromUsageRecord(item.record).StableOrder, selectedStrength, selectedDistance, protocolObservationFromUsageRecord(selected.record).StableOrder)) {
				continue
			}
			selected = item
			selectedStrength = edge.Strength
			selectedDistance = edge.Distance
		}
	}
	if selected == nil {
		return UsageRecord{}, false
	}
	c.removeFallbackRecentLocked(selected)
	return selected.record, true
}

func betterProtocolLiveCandidate(strength int64, distance time.Duration, order protocolStableOrder,
	selectedStrength int64, selectedDistance time.Duration, selectedOrder protocolStableOrder,
) bool {
	if strength != selectedStrength {
		return strength > selectedStrength
	}
	if distance != selectedDistance {
		return distance < selectedDistance
	}
	return protocolStableOrderLess(order, selectedOrder)
}

func (c *usageFallbackCoordinator) removeNativeRecentLocked(target *usageFallbackOccurrence) {
	c.removeOccurrenceLocked(c.nativeRecent, target.key, target)
	for _, correlationKey := range target.correlationKeys {
		if correlationKey != "" {
			c.removeOccurrenceLocked(c.nativeRecentCorrelated, correlationKey, target)
		}
	}
}

func (c *usageFallbackCoordinator) removeFallbackRecentLocked(target *usageFallbackOccurrence) {
	c.removeOccurrenceLocked(c.fallbackRecent, target.key, target)
	for _, correlationKey := range target.correlationKeys {
		if correlationKey != "" {
			c.removeOccurrenceLocked(c.fallbackRecentCorrelated, correlationKey, target)
		}
	}
}

func (c *usageFallbackCoordinator) removeOccurrenceLocked(index map[string][]*usageFallbackOccurrence, key string, target *usageFallbackOccurrence) {
	items := index[key]
	for i, item := range items {
		if item != target {
			continue
		}
		index[key] = append(items[:i], items[i+1:]...)
		if len(index[key]) == 0 {
			delete(index, key)
		}
		return
	}
}

func (c *usageFallbackCoordinator) cleanupLocked(now time.Time) {
	c.nativeRecentCorrelated = make(map[string][]*usageFallbackOccurrence)
	for key, items := range c.nativeRecent {
		kept := items[:0]
		for _, item := range items {
			if item == nil {
				continue
			}
			// Exact provider fingerprints keep their deliberately tight window,
			// while anonymous protocol fallbacks need enough time to use the
			// completion tolerance (up to five seconds). Keeping the occurrence
			// for one extra second only preserves the candidate; the timestamp,
			// endpoint, cache and token checks still decide whether it can match.
			if now.Sub(item.observedAt) <= nativeRecentRetention(item) {
				kept = append(kept, item)
				for _, correlationKey := range item.correlationKeys {
					if correlationKey != "" {
						c.nativeRecentCorrelated[correlationKey] = append(c.nativeRecentCorrelated[correlationKey], item)
					}
				}
			}
		}
		if len(kept) == 0 {
			delete(c.nativeRecent, key)
		} else {
			c.nativeRecent[key] = kept
		}
	}
	c.fallbackRecentCorrelated = make(map[string][]*usageFallbackOccurrence)
	for key, items := range c.fallbackRecent {
		kept := items[:0]
		for _, item := range items {
			if item != nil && now.Sub(item.observedAt) <= usageFallbackLateNativeWindow {
				kept = append(kept, item)
				for _, correlationKey := range item.correlationKeys {
					if correlationKey != "" {
						c.fallbackRecentCorrelated[correlationKey] = append(c.fallbackRecentCorrelated[correlationKey], item)
					}
				}
			}
		}
		if len(kept) == 0 {
			delete(c.fallbackRecent, key)
		} else {
			c.fallbackRecent[key] = kept
		}
	}
	c.pendingCorrelated = make(map[string][]*pendingUsageFallback)
	for key, items := range c.pending {
		kept := items[:0]
		for _, item := range items {
			if item != nil && !item.cancelled {
				kept = append(kept, item)
				for _, correlationKey := range item.correlationKeys {
					if correlationKey != "" {
						c.pendingCorrelated[correlationKey] = append(c.pendingCorrelated[correlationKey], item)
					}
				}
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
// side, protocol shape, or total_tokens convention reported it. The requested
// model alias is preferred over the upstream response model:
// native records carry the client-facing alias while fallback responses may
// expose a routed model name (for example grok-4.5-build-free for a grok-4.5
// request). Using the routed name would let the same request through twice and
// create a mirror dashboard group. Reasoning effort and service tier are
// deliberately excluded: the two sides derive them from different sources and
// the token triple already discriminates requests.
func usageRecordFingerprint(record UsageRecord) string {
	if !usageRecordHasTokenEvidence(record) {
		return ""
	}
	providerFamily := usageProviderFamily(record.Provider)
	upstreamIdentity := providerFamily
	if authID := strings.ToLower(strings.TrimSpace(record.AuthID)); authID != "" && providerFromAuthID(authID) == "" {
		upstreamIdentity = "auth:" + authID
	}
	inputTokens := record.Detail.InputTokens
	if providerFamily == "claude" {
		cacheTokens, ok := checkedProtocolAdd(record.Detail.CacheReadTokens, record.Detail.CacheCreationTokens)
		if !ok {
			return ""
		}
		inputTokens, ok = checkedProtocolAdd(inputTokens, cacheTokens)
		if !ok {
			return ""
		}
	}
	outputTokens := record.Detail.OutputTokens
	totalTokens, ok := checkedProtocolAdd(inputTokens, outputTokens)
	if !ok {
		return ""
	}
	parts := []string{
		upstreamIdentity,
		strings.ToLower(strings.TrimSpace(firstNonEmpty(record.Alias, record.Model))),
		canonicalClientAPIKey(record.APIKey),
		fmt.Sprintf("%d", inputTokens),
		fmt.Sprintf("%d", outputTokens),
		fmt.Sprintf("%d", totalTokens),
	}
	return strings.Join(parts, "\x00")
}

func usageRecordHasTokenEvidence(record UsageRecord) bool {
	meta := record.protocolCorrelation
	if meta != nil && meta.SchemaVersion == protocolCorrelationSchemaVersion &&
		meta.KnownFields&(protocolCorrelationKnownInput|protocolCorrelationKnownOutput) ==
			(protocolCorrelationKnownInput|protocolCorrelationKnownOutput) {
		return true
	}
	// Legacy UsageDetail has no presence bits. A non-zero input or output is
	// enough to establish both legacy buckets (the adapter treats the omitted
	// counterpart as an unknown zero), but a total-only record is too weak for
	// an exact fallback/native identity.
	return record.Detail.InputTokens != 0 || record.Detail.OutputTokens != 0
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
