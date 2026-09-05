package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// A response-interceptor fallback is timestamped when the response usage is
// observed, while native CPA usage is timestamped at request start and carries
// the request latency. In practice the two completion timestamps differ only
// by callback scheduling time, so the base window stays tight: an anonymous
// protocol fallback must not consume an unrelated native request merely
// because its token counts happen to match.
//
// The drift does grow with the request though — it accumulates over streaming
// delivery and the post-response callback hop. Real data: a 730.3s Codex
// request completed 1.028s before its fallback was observed, which a flat 1s
// window rejected, leaving both records on the dashboard. The window therefore
// scales with the native latency and is capped so it cannot widen without
// bound.
const (
	protocolFallbackCompletionTolerance      = time.Second
	protocolFallbackCompletionToleranceMax   = 5 * time.Second
	protocolFallbackCompletionToleranceRatio = 200 // +0.5% of the native latency

	protocolCorrelationSchemaVersion = 1

	protocolCorrelationKnownInput uint16 = 1 << iota
	protocolCorrelationKnownOutput
	protocolCorrelationKnownReasoning
	protocolCorrelationKnownCacheRead
	protocolCorrelationKnownCacheWrite
	protocolCorrelationKnownCacheCombined
	protocolCorrelationKnownTotal

	protocolCorrelationKnownAll = protocolCorrelationKnownInput |
		protocolCorrelationKnownOutput |
		protocolCorrelationKnownReasoning |
		protocolCorrelationKnownCacheRead |
		protocolCorrelationKnownCacheWrite |
		protocolCorrelationKnownCacheCombined |
		protocolCorrelationKnownTotal

	protocolInputModeCacheSubset           = "cache_subset"
	protocolInputModeCacheSubsetNormalized = "cache_subset_normalized"
	protocolInputModeCacheIndependent      = "cache_independent"
	protocolInputModeAmbiguous             = "ambiguous"
	protocolOutputModeReasoningSubset      = "reasoning_subset"
	protocolOutputModeReasoningIndependent = "reasoning_independent"
	protocolOutputModeAmbiguous            = "ambiguous"
	protocolCacheModeSplit                 = "split"
	protocolCacheModeCombined              = "combined"
	protocolCacheModeAmbiguous             = "ambiguous"
)

type protocolCorrelationRole uint8

const (
	protocolCorrelationRoleUnknown protocolCorrelationRole = iota
	protocolCorrelationRoleFallback
	protocolCorrelationRoleNative
)

// protocolTokenShape is a complete interpretation of one response's token
// buckets.  FullInput, FullOutput and Total are intentionally coupled: a
// matcher may not combine the input from one interpretation with the output
// from another.
type protocolTokenShape struct {
	FullInput  int64
	FullOutput int64
	Total      int64
	Strength   uint8
}

type protocolTokenEvidence struct {
	Input         int64
	Output        int64
	Reasoning     int64
	CacheRead     int64
	CacheWrite    int64
	CacheCombined int64
	Total         int64
	KnownFields   uint16
	InputMode     string
	OutputMode    string
	CacheMode     string
	Shapes        []protocolTokenShape
	Invalid       bool
}

type protocolStableOrder struct {
	Timestamp  time.Time
	Completion time.Time
	API        string
	Model      string
	Provider   string
	Auth       string
	Signature  string
	Index      int
}

type protocolCorrelationObservation struct {
	Role           protocolCorrelationRole
	RequestedModel string
	ClientIdentity string
	Failed         bool
	StatusCode     int
	Endpoint       string
	Timestamp      time.Time
	Latency        time.Duration
	SyntheticTime  bool
	Invalid        bool
	Tokens         protocolTokenEvidence
	StableOrder    protocolStableOrder
}

type protocolCorrelationEdgeResult struct {
	Distance      time.Duration
	Strength      int64
	fallbackShape protocolTokenShape
	nativeShape   protocolTokenShape
}

func protocolFallbackTolerance(latency time.Duration) time.Duration {
	if latency <= 0 {
		return protocolFallbackCompletionTolerance
	}
	extra := latency / protocolFallbackCompletionToleranceRatio
	if extra >= protocolFallbackCompletionToleranceMax-protocolFallbackCompletionTolerance {
		return protocolFallbackCompletionToleranceMax
	}
	return protocolFallbackCompletionTolerance + extra
}

func isAnonymousProtocolFallback(record UsageRecord) bool {
	if strings.EqualFold(strings.TrimSpace(record.ExecutorType), responseInterceptorFallbackExecutor) {
		return true
	}
	if strings.TrimSpace(record.AuthID) != "" || strings.TrimSpace(record.AuthIndex) != "" ||
		record.Latency > 0 || record.TTFT > 0 {
		return false
	}
	// Compatibility for records produced before RequestDetail persisted the
	// executor type. Keep the legacy structural signature narrow; all newly
	// written protocol fallbacks carry the explicit executor marker above.
	return strings.EqualFold(strings.TrimSpace(record.Provider), "openai-compatible") &&
		isOpenAIProtocolFallbackEndpoint(record.Endpoint)
}

// isNativeProtocolCorrelationRecord accepts a native record of any upstream
// family. A response fallback may know only the client protocol, while CPA can
// route that request to a provider using a different protocol. Provider names
// therefore cannot be used as a cross-record identity constraint.
func isNativeProtocolCorrelationRecord(record UsageRecord) bool {
	return !isAnonymousProtocolFallback(record) &&
		(strings.TrimSpace(record.AuthID) != "" || strings.TrimSpace(record.AuthIndex) != "") &&
		record.Latency > 0
}

func isAnonymousProtocolFallbackDetail(detail RequestDetail) bool {
	if strings.EqualFold(strings.TrimSpace(detail.ExecutorType), responseInterceptorFallbackExecutor) {
		return true
	}
	if strings.TrimSpace(detail.AuthID) != "" || strings.TrimSpace(detail.AuthIndex) != "" ||
		detail.LatencyMs > 0 || detail.TTFTMs > 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(detail.Provider), "openai-compatible") &&
		isOpenAIProtocolFallbackEndpoint(detail.Endpoint)
}

func isNativeProtocolCorrelationDetail(detail RequestDetail) bool {
	return !isAnonymousProtocolFallbackDetail(detail) &&
		(strings.TrimSpace(detail.AuthID) != "" || strings.TrimSpace(detail.AuthIndex) != "") &&
		detail.LatencyMs > 0
}

func isOpenAIProtocolFallbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(endpoint)), "/")
	return endpoint == "/v1/chat/completions" || endpoint == "/v1/responses"
}

func checkedProtocolAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > int64(^uint64(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}

func saturatingProtocolAdd(left, right int64) int64 {
	if left < 0 {
		left = 0
	}
	if right < 0 {
		right = 0
	}
	if value, ok := checkedProtocolAdd(left, right); ok {
		return value
	}
	return int64(^uint64(0) >> 1)
}

func durationFromMilliseconds(value int64) (time.Duration, bool) {
	if value < 0 {
		return 0, false
	}
	maxMilliseconds := int64(^uint64(0)>>1) / int64(time.Millisecond)
	if value > maxMilliseconds {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

func absoluteProtocolDuration(value time.Duration) time.Duration {
	if value >= 0 {
		return value
	}
	if value == time.Duration(-1<<63) {
		return time.Duration(1<<63 - 1)
	}
	return -value
}

func cloneProtocolCorrelationMeta(meta *ProtocolCorrelationMeta) *ProtocolCorrelationMeta {
	if meta == nil {
		return nil
	}
	copy := *meta
	return &copy
}

func validProtocolCorrelationMeta(meta *ProtocolCorrelationMeta) bool {
	if meta == nil || meta.SchemaVersion != protocolCorrelationSchemaVersion {
		return false
	}
	if meta.KnownFields&^protocolCorrelationKnownAll != 0 {
		return false
	}
	validInput := map[string]bool{
		protocolInputModeCacheSubset:           true,
		protocolInputModeCacheSubsetNormalized: true,
		protocolInputModeCacheIndependent:      true,
		protocolInputModeAmbiguous:             true,
	}
	validOutput := map[string]bool{
		protocolOutputModeReasoningSubset:      true,
		protocolOutputModeReasoningIndependent: true,
		protocolOutputModeAmbiguous:            true,
	}
	validCache := map[string]bool{
		protocolCacheModeSplit:     true,
		protocolCacheModeCombined:  true,
		protocolCacheModeAmbiguous: true,
	}
	return validInput[meta.InputMode] && validOutput[meta.OutputMode] && validCache[meta.CacheMode]
}

func correlationMetaForFamily(family string, meta *ProtocolCorrelationMeta) *ProtocolCorrelationMeta {
	if meta == nil {
		meta = &ProtocolCorrelationMeta{SchemaVersion: protocolCorrelationSchemaVersion}
	}
	if meta.SchemaVersion == 0 {
		meta.SchemaVersion = protocolCorrelationSchemaVersion
	}
	switch usageProviderFamily(family) {
	case "claude":
		if meta.InputMode == "" {
			meta.InputMode = protocolInputModeCacheIndependent
		}
		if meta.OutputMode == "" {
			meta.OutputMode = protocolOutputModeReasoningSubset
		}
		if meta.CacheMode == "" {
			meta.CacheMode = protocolCacheModeSplit
		}
	case "gemini", "vertex", "aistudio":
		if meta.InputMode == "" {
			meta.InputMode = protocolInputModeCacheSubset
		}
		if meta.OutputMode == "" {
			meta.OutputMode = protocolOutputModeReasoningIndependent
		}
		if meta.CacheMode == "" {
			meta.CacheMode = protocolCacheModeCombined
		}
	default:
		if meta.InputMode == "" {
			meta.InputMode = protocolInputModeCacheSubset
		}
		if meta.OutputMode == "" {
			meta.OutputMode = protocolOutputModeReasoningSubset
		}
		if meta.CacheMode == "" {
			meta.CacheMode = protocolCacheModeSplit
		}
	}
	return meta
}

func correlationFamilyForUsageRecord(record UsageRecord) string {
	family := usageProviderFamily(record.Provider)
	if isAnonymousProtocolFallback(record) {
		endpoint := strings.ToLower(strings.TrimSpace(record.Endpoint))
		switch {
		case strings.Contains(endpoint, "messages"):
			return "claude"
		case strings.Contains(endpoint, "generatecontent") || strings.Contains(endpoint, "v1beta"):
			return "gemini"
		case isOpenAIProtocolFallbackEndpoint(endpoint):
			return "openai"
		}
	}
	switch family {
	case "claude":
		return "claude"
	case "gemini", "vertex", "aistudio", "antigravity":
		return "gemini"
	case "codex", "openai-compatible", "openai", "kimi", "xai", "deepseek", "openrouter", "groq", "mistral", "cohere", "perplexity", "cerebras", "alibaba", "moonshotai":
		return "openai"
	default:
		switch strings.ToLower(strings.TrimSpace(record.ExecutorType)) {
		case "openaicompatexecutor", "codexexecutor", "xaiexecutor":
			return "openai"
		case "claudeexecutor", "anthropicexecutor":
			return "claude"
		case "geminiexecutor", "vertexexecutor", "aistudioexecutor":
			return "gemini"
		default:
			return family
		}
	}
}

func correlationFamilyForDetail(detail RequestDetail) string {
	if isAnonymousProtocolFallbackDetail(detail) {
		endpoint := strings.ToLower(strings.TrimSpace(detail.Endpoint))
		switch {
		case strings.Contains(endpoint, "messages"):
			return "claude"
		case strings.Contains(endpoint, "generatecontent") || strings.Contains(endpoint, "v1beta"):
			return "gemini"
		case isOpenAIProtocolFallbackEndpoint(endpoint):
			return "openai"
		}
	}
	family := usageProviderFamily(detail.Provider)
	switch family {
	case "claude":
		return "claude"
	case "gemini", "vertex", "aistudio", "antigravity":
		return "gemini"
	case "codex", "openai-compatible", "openai", "kimi", "xai", "deepseek", "openrouter", "groq", "mistral", "cohere", "perplexity", "cerebras", "alibaba", "moonshotai":
		return "openai"
	default:
		switch strings.ToLower(strings.TrimSpace(detail.ExecutorType)) {
		case "openaicompatexecutor", "codexexecutor", "xaiexecutor":
			return "openai"
		case "claudeexecutor", "anthropicexecutor":
			return "claude"
		case "geminiexecutor", "vertexexecutor", "aistudioexecutor":
			return "gemini"
		default:
			return family
		}
	}
}

// correlationFamilyForProvider returns the protocol accounting family implied
// by a selected upstream provider. Unlike correlationFamilyForUsageRecord it
// has no endpoint/source fallback logic, so it can be compared with the client
// response family while constructing translated-response metadata.
func correlationFamilyForProvider(provider string) string {
	switch usageProviderFamily(provider) {
	case "claude":
		return "claude"
	case "gemini", "vertex", "aistudio", "antigravity":
		return "gemini"
	case "codex", "openai-compatible", "openai", "kimi", "xai", "deepseek", "openrouter", "groq", "mistral", "cohere", "perplexity", "cerebras", "alibaba", "moonshotai":
		return "openai"
	default:
		return ""
	}
}

func legacyProtocolCorrelationMeta(family string, input, output, reasoning, cacheRead, cacheWrite, cacheCombined, total int64) *ProtocolCorrelationMeta {
	meta := &ProtocolCorrelationMeta{SchemaVersion: protocolCorrelationSchemaVersion}
	if input != 0 || output != 0 {
		meta.KnownFields |= protocolCorrelationKnownInput | protocolCorrelationKnownOutput
	}
	if reasoning > 0 {
		meta.KnownFields |= protocolCorrelationKnownReasoning
	}
	if cacheRead > 0 {
		meta.KnownFields |= protocolCorrelationKnownCacheRead
	}
	if cacheWrite > 0 {
		meta.KnownFields |= protocolCorrelationKnownCacheWrite
	}
	if cacheCombined > 0 {
		meta.KnownFields |= protocolCorrelationKnownCacheCombined
	}
	if total > 0 {
		meta.KnownFields |= protocolCorrelationKnownTotal
	}
	meta = correlationMetaForFamily(family, meta)
	if usageProviderFamily(family) == "gemini" && cacheCombined > 0 {
		meta.KnownFields &^= protocolCorrelationKnownCacheRead
		meta.KnownFields |= protocolCorrelationKnownCacheCombined
	}
	if !isKnownProtocolFamily(family) {
		// Legacy records from an unrecognised provider have no trustworthy
		// bucket contract. Keep them visible, but do not use plain integers as
		// a cross-protocol deletion key.
		meta.KnownFields = 0
	}
	// A complete native total equal to the visible buckets is useful evidence
	// that an omitted optional bucket was really zero.  It is not used for
	// fallback records, where translators are allowed to omit cache/reasoning
	// from total.
	return meta
}

func isKnownProtocolFamily(family string) bool {
	switch usageProviderFamily(family) {
	case "claude", "gemini", "vertex", "aistudio", "codex", "openai-compatible", "openai":
		return true
	default:
		return false
	}
}

// cachedTokensAreCacheCreationFallback identifies the compatibility value
// written by CPA's Claude parser when cache_read is zero: CachedTokens is
// populated from cache_creation even though it is not a cache-read bucket.
// Keep the raw values here so correlation can still reject negative inputs.
func cachedTokensAreCacheCreationFallback(provider string, cacheRead, cached, cacheWrite int64) bool {
	return providerUsesExclusiveCacheInput(provider) && cacheRead == 0 && cacheWrite > 0 && cached == cacheWrite
}

func protocolCorrelationCacheRead(provider string, cacheRead, cached, cacheWrite int64) int64 {
	if cacheRead == 0 && cached > 0 && !cachedTokensAreCacheCreationFallback(provider, cacheRead, cached, cacheWrite) {
		return cached
	}
	return cacheRead
}

func protocolCorrelationMetaForUsageRecord(record UsageRecord) *ProtocolCorrelationMeta {
	if record.protocolCorrelation != nil {
		return cloneProtocolCorrelationMeta(record.protocolCorrelation)
	}
	if !isAnonymousProtocolFallback(record) && !isNativeProtocolCorrelationRecord(record) {
		return nil
	}
	detail := record.Detail
	family := correlationFamilyForUsageRecord(record)
	cacheRead := protocolCorrelationCacheRead(family, detail.CacheReadTokens, detail.CachedTokens, detail.CacheCreationTokens)
	cacheCombined := int64(0)
	if family == "gemini" {
		cacheCombined = detail.CachedTokens
		cacheRead = 0
	}
	return legacyProtocolCorrelationMeta(family, detail.InputTokens, detail.OutputTokens,
		detail.ReasoningTokens, cacheRead, detail.CacheCreationTokens, cacheCombined, detail.TotalTokens)
}

func protocolCorrelationMetaForDetail(detail RequestDetail) *ProtocolCorrelationMeta {
	if detail.Correlation != nil {
		return cloneProtocolCorrelationMeta(detail.Correlation)
	}
	if !isAnonymousProtocolFallbackDetail(detail) && !isNativeProtocolCorrelationDetail(detail) {
		return nil
	}
	tokens := detail.Tokens
	family := correlationFamilyForDetail(detail)
	cacheRead := protocolCorrelationCacheRead(family, tokens.CacheReadTokens, tokens.CachedTokens, tokens.CacheWriteTokens)
	cacheCombined := int64(0)
	if family == "gemini" {
		cacheCombined = tokens.CacheTokens
		if cacheCombined == 0 && tokens.CachedTokens > 0 && tokens.CacheWriteTokens > 0 {
			if sum, ok := checkedProtocolAdd(tokens.CachedTokens, tokens.CacheWriteTokens); ok {
				cacheCombined = sum
			}
		}
		cacheRead = 0
	}
	return legacyProtocolCorrelationMeta(family, tokens.InputTokens, tokens.OutputTokens,
		tokens.ReasoningTokens, cacheRead, tokens.CacheWriteTokens, cacheCombined, tokens.TotalTokens)
}

func protocolObservationFromUsageRecord(record UsageRecord) protocolCorrelationObservation {
	role := protocolCorrelationRoleUnknown
	if isAnonymousProtocolFallback(record) {
		role = protocolCorrelationRoleFallback
	} else if isNativeProtocolCorrelationRecord(record) {
		role = protocolCorrelationRoleNative
	}
	meta := protocolCorrelationMetaForUsageRecord(record)
	return protocolCorrelationObservation{
		Role:           role,
		RequestedModel: strings.TrimSpace(firstNonEmpty(record.Alias, record.Model)),
		ClientIdentity: canonicalClientAPIKey(record.APIKey),
		Failed:         record.Failed,
		StatusCode:     record.Failure.StatusCode,
		Endpoint:       record.Endpoint,
		Timestamp:      record.RequestedAt,
		Latency:        record.Latency,
		Invalid:        record.Latency < 0,
		Tokens:         protocolTokenEvidenceFromUsageDetail(record.Detail, meta, correlationFamilyForUsageRecord(record), role),
		StableOrder: protocolStableOrder{
			Timestamp:  record.RequestedAt,
			Completion: record.RequestedAt.Add(record.Latency),
			API:        usageGroupKey(record),
			Model:      strings.TrimSpace(firstNonEmpty(record.Alias, record.Model)),
			Provider:   strings.TrimSpace(record.Provider),
			Auth:       strings.TrimSpace(firstNonEmpty(record.AuthID, record.AuthIndex)),
			Signature:  usageRecordFingerprint(record),
		},
	}
}

func detailCorrelationModel(outerModel string, detail RequestDetail) string {
	return strings.TrimSpace(firstNonEmpty(detail.RequestedModel, detail.Model, outerModel))
}

func protocolObservationFromRequestDetail(outerModel string, detail RequestDetail) protocolCorrelationObservation {
	role := protocolCorrelationRoleUnknown
	if isAnonymousProtocolFallbackDetail(detail) {
		role = protocolCorrelationRoleFallback
	} else if isNativeProtocolCorrelationDetail(detail) {
		role = protocolCorrelationRoleNative
	}
	clientIdentity := strings.TrimSpace(detail.APIKeyHash)
	if clientIdentity == "" {
		clientIdentity = canonicalClientAPIKey(detail.APIKey)
	}
	model := detailCorrelationModel(outerModel, detail)
	latency, latencyOK := durationFromMilliseconds(detail.LatencyMs)
	return protocolCorrelationObservation{
		Role:           role,
		RequestedModel: model,
		ClientIdentity: clientIdentity,
		Failed:         detail.Failed,
		StatusCode:     detail.StatusCode,
		Endpoint:       detail.Endpoint,
		Timestamp:      detail.Timestamp,
		Latency:        latency,
		SyntheticTime:  detail.TimestampSynthetic,
		Invalid:        !latencyOK,
		Tokens:         protocolTokenEvidenceFromTokenStats(detail.Tokens, protocolCorrelationMetaForDetail(detail), correlationFamilyForDetail(detail), role),
		StableOrder: protocolStableOrder{
			Timestamp:  detail.Timestamp,
			Completion: detail.Timestamp.Add(latency),
			API:        strings.TrimSpace(detail.UpstreamAPI),
			Model:      model,
			Provider:   strings.TrimSpace(detail.Provider),
			Auth:       strings.TrimSpace(firstNonEmpty(detail.AuthID, detail.AuthIndex)),
			Signature:  detailCorrelationSignature(outerModel, detail),
		},
	}
}

func detailCorrelationSignature(outerModel string, detail RequestDetail) string {
	parts := []string{
		detailCorrelationModel(outerModel, detail), detail.APIKeyHash, detail.APIKey,
		detail.Provider, detail.AuthID, detail.AuthIndex, detail.Endpoint,
		strconv.FormatInt(detail.Tokens.InputTokens, 10), strconv.FormatInt(detail.Tokens.OutputTokens, 10),
		strconv.FormatInt(detail.Tokens.TotalTokens, 10),
	}
	return strings.Join(parts, "\x00")
}

func protocolTokenEvidenceFromUsageDetail(detail UsageDetail, meta *ProtocolCorrelationMeta, family string, role protocolCorrelationRole) protocolTokenEvidence {
	cacheRead := detail.CacheReadTokens
	cacheCombined := int64(0)
	if meta != nil && meta.KnownFields&protocolCorrelationKnownCacheRead != 0 && cacheRead == 0 {
		cacheRead = protocolCorrelationCacheRead(family, cacheRead, detail.CachedTokens, detail.CacheCreationTokens)
	}
	if meta != nil && meta.KnownFields&protocolCorrelationKnownCacheCombined != 0 {
		cacheCombined = detail.CachedTokens
	}
	if meta == nil {
		meta = legacyProtocolCorrelationMeta(family, detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens,
			cacheRead, detail.CacheCreationTokens, cacheCombined, detail.TotalTokens)
	}
	return buildProtocolTokenEvidence(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens,
		cacheRead, detail.CacheCreationTokens, cacheCombined, detail.TotalTokens, meta, family, role)
}

func protocolTokenEvidenceFromTokenStats(tokens TokenStats, meta *ProtocolCorrelationMeta, family string, role protocolCorrelationRole) protocolTokenEvidence {
	cacheRead := tokens.CacheReadTokens
	if meta != nil && meta.KnownFields&protocolCorrelationKnownCacheRead != 0 && cacheRead == 0 {
		cacheRead = protocolCorrelationCacheRead(family, cacheRead, tokens.CachedTokens, tokens.CacheWriteTokens)
	}
	cacheCombined := tokens.CacheTokens
	if meta == nil {
		meta = legacyProtocolCorrelationMeta(family, tokens.InputTokens, tokens.OutputTokens, tokens.ReasoningTokens,
			cacheRead, tokens.CacheWriteTokens, cacheCombined, tokens.TotalTokens)
	}
	return buildProtocolTokenEvidence(tokens.InputTokens, tokens.OutputTokens, tokens.ReasoningTokens,
		cacheRead, tokens.CacheWriteTokens, cacheCombined, tokens.TotalTokens, meta, family, role)
}

func buildProtocolTokenEvidence(input, output, reasoning, cacheRead, cacheWrite, cacheCombined, total int64,
	meta *ProtocolCorrelationMeta, family string, role protocolCorrelationRole,
) protocolTokenEvidence {
	evidence := protocolTokenEvidence{
		Input: input, Output: output, Reasoning: reasoning, CacheRead: cacheRead,
		CacheWrite: cacheWrite, CacheCombined: cacheCombined, Total: total,
	}
	if input < 0 || output < 0 || reasoning < 0 || cacheRead < 0 || cacheWrite < 0 || cacheCombined < 0 || total < 0 {
		evidence.Invalid = true
		return evidence
	}
	if meta == nil {
		return evidence
	}
	if meta.SchemaVersion != protocolCorrelationSchemaVersion {
		// Schema zero represents details written before the sidecar was
		// versioned, so retain the legacy value-only adapter for that case.
		// A positive unknown version may change field or mode semantics; keep the
		// accounting record visible, but never use it as deletion evidence.
		if meta.SchemaVersion == 0 {
			legacy := legacyProtocolCorrelationMeta(family, input, output, reasoning, cacheRead, cacheWrite, cacheCombined, total)
			return buildProtocolTokenEvidence(input, output, reasoning, cacheRead, cacheWrite, cacheCombined, total, legacy, family, role)
		}
		evidence.Invalid = true
		return evidence
	}
	if !validProtocolCorrelationMeta(meta) {
		evidence.Invalid = true
		return evidence
	}
	evidence.KnownFields = meta.KnownFields
	evidence.InputMode = meta.InputMode
	evidence.OutputMode = meta.OutputMode
	evidence.CacheMode = meta.CacheMode
	if evidence.KnownFields&protocolCorrelationKnownInput == 0 || evidence.KnownFields&protocolCorrelationKnownOutput == 0 {
		// Values from a legacy struct cannot prove an omitted zero.  The only
		// exception is the explicit metadata path above, which has already
		// supplied the presence bits.
		return evidence
	}
	evidence.Shapes = buildProtocolTokenShapes(evidence, family, role)
	return evidence
}

type protocolTokenVariant struct {
	value    int64
	strength uint8
}

func buildProtocolTokenShapes(e protocolTokenEvidence, family string, role protocolCorrelationRole) []protocolTokenShape {
	if e.Invalid || e.KnownFields&protocolCorrelationKnownInput == 0 || e.KnownFields&protocolCorrelationKnownOutput == 0 {
		return nil
	}
	baseInputStrength := uint8(90)
	baseOutputStrength := uint8(90)
	if e.KnownFields&protocolCorrelationKnownTotal != 0 {
		baseInputStrength += 5
		baseOutputStrength += 5
	}

	inputVariants := make([]protocolTokenVariant, 0, 2)
	addInputVariant := func(value int64, strength uint8) {
		if value < 0 {
			return
		}
		for i := range inputVariants {
			if inputVariants[i].value == value {
				if strength > inputVariants[i].strength {
					inputVariants[i].strength = strength
				}
				return
			}
		}
		inputVariants = append(inputVariants, protocolTokenVariant{value: value, strength: strength})
	}
	cacheReadKnown := e.KnownFields&protocolCorrelationKnownCacheRead != 0
	cacheWriteKnown := e.KnownFields&protocolCorrelationKnownCacheWrite != 0
	cacheCombinedKnown := e.KnownFields&protocolCorrelationKnownCacheCombined != 0
	addExpandedInput := func(cache int64, strength uint8) {
		sum, ok := checkedProtocolAdd(e.Input, cache)
		if ok {
			addInputVariant(sum, strength)
		}
	}
	addSplitCacheInput := func(strength uint8) {
		if !cacheReadKnown || !cacheWriteKnown {
			return
		}
		cache, ok := checkedProtocolAdd(e.CacheRead, e.CacheWrite)
		if !ok {
			return
		}
		addExpandedInput(cache, strength)
	}
	addInputVariant(e.Input, baseInputStrength)
	switch e.InputMode {
	case protocolInputModeCacheIndependent:
		// A native independent-cache protocol has exactly one canonical input
		// shape. Keeping the raw/exclusive input as a second native shape would
		// let an inconsistent total or a translated cache bucket create a false
		// match. If the optional buckets are absent, derive the full input from
		// the same total below; do not guess it from an unrelated field.
		inputVariants = inputVariants[:0]
		if e.CacheMode == protocolCacheModeCombined && cacheCombinedKnown {
			addExpandedInput(e.CacheCombined, 105)
		} else if e.CacheMode == protocolCacheModeAmbiguous {
			addSplitCacheInput(105)
			if cacheCombinedKnown {
				addExpandedInput(e.CacheCombined, 100)
			}
		} else {
			addSplitCacheInput(115)
			if len(inputVariants) == 0 && cacheCombinedKnown {
				addExpandedInput(e.CacheCombined, 105)
			}
		}
		if len(inputVariants) == 0 && e.KnownFields&protocolCorrelationKnownTotal == 0 {
			// Claude translations can omit both optional cache buckets and the
			// total. Keep the visible input as a conservative fallback candidate,
			// without claiming the missing cache fields were explicitly zero.
			// The native side must still supply an independently valid full shape.
			if role == protocolCorrelationRoleFallback && !cacheReadKnown && !cacheWriteKnown && !cacheCombinedKnown {
				addInputVariant(e.Input, 70)
			} else {
				return nil
			}
		}
	case protocolInputModeAmbiguous:
		if e.CacheMode == protocolCacheModeCombined && cacheCombinedKnown {
			addExpandedInput(e.CacheCombined, 100)
		} else if e.CacheMode == protocolCacheModeAmbiguous {
			addSplitCacheInput(105)
			if cacheCombinedKnown {
				addExpandedInput(e.CacheCombined, 100)
			}
		} else {
			addSplitCacheInput(105)
			if len(inputVariants) == 1 && cacheCombinedKnown {
				addExpandedInput(e.CacheCombined, 100)
			}
		}
	case protocolInputModeCacheSubset:
		// Gemini's translated response can expose a combined cache bucket even
		// though its ordinary input bucket already includes cache. The client
		// protocol is represented by InputMode; the selected upstream provider
		// passed as family may be Claude or Codex after translation, so using that
		// provider name here would lose the second interpretation and prevent a
		// Gemini-client fallback from matching its native upstream record.
		// Only an anonymous fallback gets this second finite interpretation;
		// native Gemini remains subset-only per its parser contract.
		if role == protocolCorrelationRoleFallback && cacheCombinedKnown {
			addExpandedInput(e.CacheCombined, 95)
		}
	case protocolInputModeCacheSubsetNormalized:
		// The reported input already includes the cache bucket. This mode is
		// emitted for translated responses after accounting normalization; keep
		// the raw input as the sole candidate and retain cache evidence only for
		// compatibility checks.
	default:
		return nil
	}

	outputVariants := make([]protocolTokenVariant, 0, 2)
	addOutputVariant := func(value int64, strength uint8) {
		if value < 0 {
			return
		}
		for i := range outputVariants {
			if outputVariants[i].value == value {
				if strength > outputVariants[i].strength {
					outputVariants[i].strength = strength
				}
				return
			}
		}
		outputVariants = append(outputVariants, protocolTokenVariant{value: value, strength: strength})
	}
	addOutputVariant(e.Output, baseOutputStrength)
	addReasoningOutput := func(strength uint8) {
		if e.KnownFields&protocolCorrelationKnownReasoning == 0 {
			return
		}
		sum, ok := checkedProtocolAdd(e.Output, e.Reasoning)
		if ok {
			addOutputVariant(sum, strength)
		}
	}
	switch e.OutputMode {
	case protocolOutputModeReasoningIndependent:
		outputVariants = outputVariants[:0]
		addReasoningOutput(115)
		if len(outputVariants) == 0 && e.KnownFields&protocolCorrelationKnownTotal == 0 {
			return nil
		}
	case protocolOutputModeAmbiguous:
		addReasoningOutput(105)
	case protocolOutputModeReasoningSubset:
		// The reported output already contains reasoning. The reasoning field
		// remains a comparable detail, never another canonical output shape.
	default:
		return nil
	}

	// Derive a missing independent bucket only from the same reported total.
	// This is deliberately done as complete pairs, not by modifying the
	// independent input/output variant lists independently.
	shapes := make([]protocolTokenShape, 0, len(inputVariants)*len(outputVariants))
	addShape := func(input, output int64, strength uint8) {
		if input < 0 || output < 0 {
			return
		}
		total, ok := checkedProtocolAdd(input, output)
		if !ok {
			return
		}
		for i := range shapes {
			if shapes[i].FullInput == input && shapes[i].FullOutput == output {
				if strength > shapes[i].Strength {
					shapes[i].Strength = strength
				}
				return
			}
		}
		shapes = append(shapes, protocolTokenShape{FullInput: input, FullOutput: output, Total: total, Strength: strength})
	}
	if e.KnownFields&protocolCorrelationKnownTotal != 0 {
		baseTotal, ok := checkedProtocolAdd(e.Input, e.Output)
		if !ok || e.Total < baseTotal {
			return nil
		}
	}

	if e.InputMode == protocolInputModeCacheIndependent && len(inputVariants) == 0 && e.KnownFields&protocolCorrelationKnownTotal != 0 {
		for _, outputVariant := range outputVariants {
			if e.Total < outputVariant.value {
				continue
			}
			input := e.Total - outputVariant.value
			addShape(input, outputVariant.value, 70)
		}
	}
	if e.OutputMode == protocolOutputModeReasoningIndependent && len(outputVariants) == 0 && e.KnownFields&protocolCorrelationKnownTotal != 0 {
		for _, inputVariant := range inputVariants {
			if e.Total < inputVariant.value {
				continue
			}
			output := e.Total - inputVariant.value
			addShape(inputVariant.value, output, 70)
		}
	}
	for _, inputVariant := range inputVariants {
		for _, outputVariant := range outputVariants {
			strength := inputVariant.strength
			if outputVariant.strength < strength {
				strength = outputVariant.strength
			}
			addShape(inputVariant.value, outputVariant.value, strength)
		}
	}
	if role == protocolCorrelationRoleNative && e.KnownFields&protocolCorrelationKnownTotal != 0 {
		filtered := shapes[:0]
		for _, shape := range shapes {
			if shape.Total == e.Total {
				filtered = append(filtered, shape)
			}
		}
		shapes = filtered
	}
	sort.SliceStable(shapes, func(i, j int) bool {
		if shapes[i].Strength != shapes[j].Strength {
			return shapes[i].Strength > shapes[j].Strength
		}
		if shapes[i].FullInput != shapes[j].FullInput {
			return shapes[i].FullInput < shapes[j].FullInput
		}
		return shapes[i].FullOutput < shapes[j].FullOutput
	})
	return shapes
}

func protocolObservationPartitionKey(observation protocolCorrelationObservation) string {
	status := observation.StatusCode
	if !observation.Failed || status < 0 {
		status = 0
	}
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(observation.RequestedModel)),
		strings.ToLower(strings.TrimSpace(observation.ClientIdentity)),
		strconv.FormatBool(observation.Failed), strconv.Itoa(status),
	}, "\x00")
}

func protocolObservationPartitionKeys(observation protocolCorrelationObservation) []string {
	base := []string{
		strings.ToLower(strings.TrimSpace(observation.RequestedModel)),
		strings.ToLower(strings.TrimSpace(observation.ClientIdentity)),
		strconv.FormatBool(observation.Failed),
		// A missing failure status is unknown, not zero. Keep all failed
		// records in one bounded partition and let the edge veto only when
		// both non-zero statuses are known and different.
		"failure-status-wildcard",
	}
	return []string{strings.Join(base, "\x00")}
}

func protocolObservationShapeKeys(observation protocolCorrelationObservation) []string {
	if observation.Role == protocolCorrelationRoleUnknown || observation.Tokens.Invalid ||
		strings.TrimSpace(observation.RequestedModel) == "" || strings.EqualFold(strings.TrimSpace(observation.RequestedModel), "unknown") ||
		strings.TrimSpace(observation.ClientIdentity) == "" {
		return nil
	}
	partitions := protocolObservationPartitionKeys(observation)
	keys := make([]string, 0, len(observation.Tokens.Shapes)*len(partitions))
	seen := make(map[string]struct{}, len(observation.Tokens.Shapes))
	for _, partition := range partitions {
		for _, shape := range observation.Tokens.Shapes {
			key := partition + "\x00" + strconv.FormatInt(shape.FullInput, 10) + "\x00" + strconv.FormatInt(shape.FullOutput, 10)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// usageProtocolCorrelationKey is retained as a compatibility accessor for
// diagnostics and old tests.  The matcher itself uses all shape keys because a
// translated response may have more than one finite interpretation.
func usageProtocolCorrelationKey(record UsageRecord) string {
	keys := protocolObservationShapeKeys(protocolObservationFromUsageRecord(record))
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func detailProtocolCorrelationKey(modelName string, detail RequestDetail) string {
	keys := protocolObservationShapeKeys(protocolObservationFromRequestDetail(modelName, detail))
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func protocolCorrelationKey(model, clientIdentity string, failed bool, statusCode int, tokens ...int64) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(model)), strings.ToLower(strings.TrimSpace(clientIdentity)),
		strconv.FormatBool(failed), strconv.Itoa(statusCode),
	}
	for _, value := range tokens {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, "\x00")
}

func protocolUsageCompletionDistance(fallback, native UsageRecord) (time.Duration, bool) {
	if !isAnonymousProtocolFallback(fallback) || !isNativeProtocolCorrelationRecord(native) {
		return 0, false
	}
	edge, ok := protocolCorrelationEdge(protocolObservationFromUsageRecord(fallback), protocolObservationFromUsageRecord(native))
	if !ok {
		return 0, false
	}
	return edge.Distance, true
}

// protocolEndpointsCompatible compares endpoints only when they describe the
// same client/upstream protocol family. Different families are a normal CPA
// translation route. Within the OpenAI family, however, chat/completions and
// responses are distinct enough to veto an otherwise ambiguous candidate.
func protocolEndpointsCompatible(fallbackEndpoint, nativeEndpoint string) bool {
	fallbackEndpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(fallbackEndpoint)), "/")
	nativeEndpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(nativeEndpoint)), "/")
	// The fallback endpoint describes the client protocol and the native
	// endpoint describes the selected upstream. They are directly comparable
	// only when both are OpenAI-protocol paths; different protocol families are
	// a valid translation route and must not veto an otherwise strong match.
	if nativeEndpoint == "" || fallbackEndpoint == "" ||
		!isOpenAIProtocolFallbackEndpoint(nativeEndpoint) ||
		!isOpenAIProtocolFallbackEndpoint(fallbackEndpoint) {
		return true
	}
	return nativeEndpoint == fallbackEndpoint
}

// protocolExactFingerprintCompatible keeps the old exact-fingerprint path
// usable for native callbacks emitted by older CPA hosts. Those callbacks can
// lack both auth metadata and latency, so they cannot participate in the
// cross-protocol observation matcher. The fingerprint has already established
// the provider family, requested model, client identity and canonical token
// triple; retain the two cheap safety vetoes that do not require correlation
// metadata.
func protocolExactFingerprintCompatible(fallback, native UsageRecord) bool {
	if fallback.Failed != native.Failed {
		return false
	}
	if fallback.Failed && fallback.Failure.StatusCode > 0 && native.Failure.StatusCode > 0 &&
		fallback.Failure.StatusCode != native.Failure.StatusCode {
		return false
	}
	return protocolEndpointsCompatible(fallback.Endpoint, native.Endpoint)
}

// protocolLiveCorrelationEdge uses the full completion-time matcher first.
// Older CPA hosts can stamp native RequestedAt at callback time, making
// RequestedAt+Latency unusable as a completion timestamp. The exact-fingerprint
// fallback therefore compares the two callback timestamps directly and keeps
// that compatibility path inside the original one-second safety window.
func protocolLiveCorrelationEdge(fallback, native UsageRecord) (protocolCorrelationEdgeResult, bool) {
	fallbackObservation := protocolObservationFromUsageRecord(fallback)
	nativeObservation := protocolObservationFromUsageRecord(native)
	if edge, ok := protocolCorrelationEdge(fallbackObservation, nativeObservation); ok {
		return edge, true
	}
	// A matching fingerprint is an exact same-family identity path retained for
	// compatibility with the original coordinator. Some native callbacks put
	// RequestedAt at callback time in older hosts, so applying the completion
	// window here would reject an otherwise exact native/fallback pair. Keep the
	// endpoint and failure-state vetoes even on this path. Cross-provider and
	// shape-only candidates still have to pass the full observation matcher.
	if fallbackFingerprint := usageRecordFingerprint(fallback); fallbackFingerprint != "" &&
		fallbackFingerprint == usageRecordFingerprint(native) &&
		protocolExactFingerprintCompatible(fallback, native) &&
		protocolExactFingerprintTimestampCompatible(fallback, native) {
		if strength, ok := protocolCorrelationEvidence(fallbackObservation, nativeObservation); ok {
			return protocolCorrelationEdgeResult{Strength: strength}, true
		}
		if !isNativeProtocolCorrelationRecord(fallback) && !isNativeProtocolCorrelationRecord(native) {
			return protocolCorrelationEdgeResult{}, true
		}
	}
	if isNativeProtocolCorrelationRecord(fallback) || isNativeProtocolCorrelationRecord(native) {
		return protocolCorrelationEdgeResult{}, false
	}
	if !protocolExactFingerprintCompatible(fallback, native) {
		return protocolCorrelationEdgeResult{}, false
	}
	return protocolCorrelationEdgeResult{}, true
}

func protocolExactFingerprintTimestampCompatible(fallback, native UsageRecord) bool {
	if fallback.RequestedAt.IsZero() || native.RequestedAt.IsZero() {
		return false
	}
	return absoluteProtocolDuration(fallback.RequestedAt.Sub(native.RequestedAt)) <= protocolFallbackCompletionTolerance
}

type protocolFallbackDetailRef struct {
	apiName   string
	modelName string
	index     int
	detail    RequestDetail
}

type protocolFallbackDetailPair struct {
	fallback protocolFallbackDetailRef
	native   protocolFallbackDetailRef
	distance time.Duration
}

func pairProtocolFallbackDetailsLegacy(refs []protocolFallbackDetailRef) []protocolFallbackDetailPair {
	byKeyFallback := make(map[string][]int)
	byKeyNative := make(map[string][]int)
	for i, ref := range refs {
		key := detailProtocolCorrelationKey(ref.modelName, ref.detail)
		if key == "" {
			continue
		}
		switch {
		case isAnonymousProtocolFallbackDetail(ref.detail):
			byKeyFallback[key] = append(byKeyFallback[key], i)
		case isNativeProtocolCorrelationDetail(ref.detail):
			byKeyNative[key] = append(byKeyNative[key], i)
		}
	}

	refLess := func(left, right int, completion bool) bool {
		leftAt := refs[left].detail.Timestamp
		rightAt := refs[right].detail.Timestamp
		if completion {
			leftAt = leftAt.Add(time.Duration(refs[left].detail.LatencyMs) * time.Millisecond)
			rightAt = rightAt.Add(time.Duration(refs[right].detail.LatencyMs) * time.Millisecond)
		}
		if !leftAt.Equal(rightAt) {
			return leftAt.Before(rightAt)
		}
		if refs[left].apiName != refs[right].apiName {
			return refs[left].apiName < refs[right].apiName
		}
		if refs[left].modelName != refs[right].modelName {
			return refs[left].modelName < refs[right].modelName
		}
		return refs[left].index < refs[right].index
	}
	pairs := make([]protocolFallbackDetailPair, 0)
	for key, fallbacks := range byKeyFallback {
		natives := byKeyNative[key]
		if len(natives) == 0 {
			continue
		}
		sort.SliceStable(fallbacks, func(i, j int) bool { return refLess(fallbacks[i], fallbacks[j], false) })
		sort.SliceStable(natives, func(i, j int) bool { return refLess(natives[i], natives[j], true) })

		// 每条 fallback 的可配 native,按完成时刻距离升序。窗口取**该 native 自己**
		// 的(随 latency 变化),与实时路径 protocolUsageCompletionDistance 同口径。
		candidates := make([][]protocolFallbackCandidate, len(fallbacks))
		for fallbackPos, fallbackIdx := range fallbacks {
			fallbackRef := refs[fallbackIdx]
			if fallbackRef.detail.Timestamp.IsZero() {
				continue
			}
			for nativePos, nativeIdx := range natives {
				nativeRef := refs[nativeIdx]
				if nativeRef.detail.Timestamp.IsZero() {
					continue
				}
				latency := time.Duration(nativeRef.detail.LatencyMs) * time.Millisecond
				distance := fallbackRef.detail.Timestamp.Sub(nativeRef.detail.Timestamp.Add(latency))
				if distance < 0 {
					distance = -distance
				}
				if distance > protocolFallbackTolerance(latency) {
					continue
				}
				if !protocolEndpointsCompatible(fallbackRef.detail.Endpoint, nativeRef.detail.Endpoint) {
					continue
				}
				if !protocolCacheReadsCompatible(normalizedCacheReadTokens(fallbackRef.detail.Tokens), normalizedCacheReadTokens(nativeRef.detail.Tokens)) {
					continue
				}
				candidates[fallbackPos] = append(candidates[fallbackPos], protocolFallbackCandidate{nativePos: nativePos, distance: distance})
			}
			sort.SliceStable(candidates[fallbackPos], func(i, j int) bool {
				if candidates[fallbackPos][i].distance != candidates[fallbackPos][j].distance {
					return candidates[fallbackPos][i].distance < candidates[fallbackPos][j].distance
				}
				return candidates[fallbackPos][i].nativePos < candidates[fallbackPos][j].nativePos
			})
		}

		// 增广路匹配(Kuhn)。贪心「各挑最近的」不保证最大匹配:F1 抢走 N1 之后
		// F2 可能谁都够不着,而 F1→N2 / F2→N1 本来成立——结果就是一条本该被消掉的
		// 重复记录留在看板上。候选按距离升序,所以在最大匹配的前提下仍优先配最近的。
		matchedBy := make([]int, len(natives))
		for i := range matchedBy {
			matchedBy[i] = -1
		}
		var augment func(fallbackPos int, visited []bool) bool
		augment = func(fallbackPos int, visited []bool) bool {
			for _, candidate := range candidates[fallbackPos] {
				if visited[candidate.nativePos] {
					continue
				}
				visited[candidate.nativePos] = true
				if matchedBy[candidate.nativePos] == -1 || augment(matchedBy[candidate.nativePos], visited) {
					matchedBy[candidate.nativePos] = fallbackPos
					return true
				}
			}
			return false
		}
		for fallbackPos := range fallbacks {
			augment(fallbackPos, make([]bool, len(natives)))
		}

		matchedNative := make([]int, len(fallbacks))
		for i := range matchedNative {
			matchedNative[i] = -1
		}
		for nativePos, fallbackPos := range matchedBy {
			if fallbackPos >= 0 {
				matchedNative[fallbackPos] = nativePos
			}
		}
		// 按 fallback 排序顺序输出,结果不依赖 map 遍历顺序。
		for fallbackPos, nativePos := range matchedNative {
			if nativePos < 0 {
				continue
			}
			distance := time.Duration(0)
			for _, candidate := range candidates[fallbackPos] {
				if candidate.nativePos == nativePos {
					distance = candidate.distance
					break
				}
			}
			pairs = append(pairs, protocolFallbackDetailPair{
				fallback: refs[fallbacks[fallbackPos]], native: refs[natives[nativePos]], distance: distance,
			})
		}
	}
	return pairs
}

type protocolFallbackCandidate struct {
	nativePos int
	distance  time.Duration
}

func protocolTokenEvidenceCompatible(fallback, native protocolTokenEvidence) (int64, bool) {
	if fallback.Invalid || native.Invalid || len(fallback.Shapes) == 0 || len(native.Shapes) == 0 {
		return 0, false
	}
	best := int64(-1)
	for _, left := range fallback.Shapes {
		for _, right := range native.Shapes {
			if left.FullInput != right.FullInput || left.FullOutput != right.FullOutput {
				continue
			}
			strength := int64(left.Strength) + int64(right.Strength)
			if strength > best {
				best = strength
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	if !protocolReasoningCompatible(fallback, native) || !protocolCacheEvidenceCompatible(fallback, native) {
		return 0, false
	}
	if fallback.KnownFields&protocolCorrelationKnownReasoning != 0 && native.KnownFields&protocolCorrelationKnownReasoning != 0 &&
		fallback.OutputMode == native.OutputMode && fallback.OutputMode != protocolOutputModeAmbiguous {
		best += 20
	}
	if protocolCacheEvidenceKnownAndComparable(fallback, native) {
		best += 20
	}
	return best, true
}

func protocolReasoningCompatible(fallback, native protocolTokenEvidence) bool {
	if fallback.KnownFields&protocolCorrelationKnownReasoning == 0 || native.KnownFields&protocolCorrelationKnownReasoning == 0 {
		return true
	}
	if fallback.OutputMode == protocolOutputModeAmbiguous || native.OutputMode == protocolOutputModeAmbiguous ||
		fallback.OutputMode != native.OutputMode {
		return true
	}
	return fallback.Reasoning == native.Reasoning
}

func protocolCacheEvidenceCompatible(fallback, native protocolTokenEvidence) bool {
	fallbackRead := fallback.KnownFields&protocolCorrelationKnownCacheRead != 0
	nativeRead := native.KnownFields&protocolCorrelationKnownCacheRead != 0
	fallbackWrite := fallback.KnownFields&protocolCorrelationKnownCacheWrite != 0
	nativeWrite := native.KnownFields&protocolCorrelationKnownCacheWrite != 0
	fallbackCombined := fallback.KnownFields&protocolCorrelationKnownCacheCombined != 0
	nativeCombined := native.KnownFields&protocolCorrelationKnownCacheCombined != 0

	if fallbackCombined && nativeCombined && fallback.CacheMode == protocolCacheModeCombined && native.CacheMode == protocolCacheModeCombined {
		return fallback.CacheCombined == native.CacheCombined
	}
	if fallbackCombined && fallback.CacheMode == protocolCacheModeCombined && native.CacheMode == protocolCacheModeSplit && nativeRead && nativeWrite {
		sum, ok := checkedProtocolAdd(native.CacheRead, native.CacheWrite)
		return ok && fallback.CacheCombined == sum
	}
	if nativeCombined && native.CacheMode == protocolCacheModeCombined && fallback.CacheMode == protocolCacheModeSplit && fallbackRead && fallbackWrite {
		sum, ok := checkedProtocolAdd(fallback.CacheRead, fallback.CacheWrite)
		return ok && native.CacheCombined == sum
	}
	if fallback.CacheMode == protocolCacheModeSplit && native.CacheMode == protocolCacheModeSplit {
		if fallbackRead && nativeRead && fallback.CacheRead != native.CacheRead {
			return false
		}
		if fallbackWrite && nativeWrite && fallback.CacheWrite != native.CacheWrite {
			return false
		}
	}
	return true
}

func protocolCacheEvidenceKnownAndComparable(fallback, native protocolTokenEvidence) bool {
	if fallback.CacheMode == protocolCacheModeSplit && native.CacheMode == protocolCacheModeSplit {
		return fallback.KnownFields&protocolCorrelationKnownCacheRead != 0 && native.KnownFields&protocolCorrelationKnownCacheRead != 0
	}
	return fallback.CacheMode == protocolCacheModeCombined && native.CacheMode == protocolCacheModeCombined &&
		fallback.KnownFields&protocolCorrelationKnownCacheCombined != 0 && native.KnownFields&protocolCorrelationKnownCacheCombined != 0
}

func protocolCorrelationEvidence(fallback, native protocolCorrelationObservation) (int64, bool) {
	if fallback.Role != protocolCorrelationRoleFallback || native.Role != protocolCorrelationRoleNative {
		return 0, false
	}
	if fallback.Invalid || native.Invalid {
		return 0, false
	}
	requestedFallback := strings.TrimSpace(fallback.RequestedModel)
	requestedNative := strings.TrimSpace(native.RequestedModel)
	clientFallback := strings.TrimSpace(fallback.ClientIdentity)
	clientNative := strings.TrimSpace(native.ClientIdentity)
	if requestedFallback == "" || requestedNative == "" || strings.EqualFold(requestedFallback, "unknown") ||
		!strings.EqualFold(requestedFallback, requestedNative) || clientFallback == "" || clientNative == "" ||
		!strings.EqualFold(clientFallback, clientNative) {
		return 0, false
	}
	if fallback.Failed != native.Failed {
		return 0, false
	}
	if fallback.Failed && fallback.StatusCode > 0 && native.StatusCode > 0 && fallback.StatusCode != native.StatusCode {
		return 0, false
	}
	if !protocolEndpointsCompatible(fallback.Endpoint, native.Endpoint) || fallback.SyntheticTime || native.SyntheticTime ||
		fallback.Timestamp.IsZero() || native.Timestamp.IsZero() || native.Latency <= 0 {
		return 0, false
	}
	strength, ok := protocolTokenEvidenceCompatible(fallback.Tokens, native.Tokens)
	if !ok {
		return 0, false
	}
	return strength, true
}

func protocolCorrelationEdge(fallback, native protocolCorrelationObservation) (protocolCorrelationEdgeResult, bool) {
	strength, ok := protocolCorrelationEvidence(fallback, native)
	if !ok {
		return protocolCorrelationEdgeResult{}, false
	}
	nativeCompletedAt := native.Timestamp.Add(native.Latency)
	distance := absoluteProtocolDuration(fallback.Timestamp.Sub(nativeCompletedAt))
	if distance > protocolFallbackTolerance(native.Latency) {
		return protocolCorrelationEdgeResult{}, false
	}
	return protocolCorrelationEdgeResult{
		Distance:      distance,
		Strength:      strength,
		fallbackShape: bestProtocolSharedShape(fallback.Tokens, native.Tokens),
		nativeShape:   bestProtocolSharedShape(native.Tokens, fallback.Tokens),
	}, true
}

func bestProtocolSharedShape(left, right protocolTokenEvidence) protocolTokenShape {
	var best protocolTokenShape
	bestStrength := -1
	for _, a := range left.Shapes {
		for _, b := range right.Shapes {
			if a.FullInput != b.FullInput || a.FullOutput != b.FullOutput {
				continue
			}
			strength := int(a.Strength) + int(b.Strength)
			if strength > bestStrength {
				best = a
				bestStrength = strength
			}
		}
	}
	return best
}

func protocolStableOrderLess(left, right protocolStableOrder) bool {
	if !left.Timestamp.Equal(right.Timestamp) {
		return left.Timestamp.Before(right.Timestamp)
	}
	if !left.Completion.Equal(right.Completion) {
		return left.Completion.Before(right.Completion)
	}
	for _, pair := range [][2]string{{left.API, right.API}, {left.Model, right.Model}, {left.Provider, right.Provider}, {left.Auth, right.Auth}, {left.Signature, right.Signature}} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return left.Index < right.Index
}

type protocolFlowCost struct {
	strength int64
	distance int64
	tie      int64
}

func (left protocolFlowCost) add(right protocolFlowCost) protocolFlowCost {
	return protocolFlowCost{strength: left.strength + right.strength, distance: left.distance + right.distance, tie: left.tie + right.tie}
}

func (left protocolFlowCost) neg() protocolFlowCost {
	return protocolFlowCost{strength: -left.strength, distance: -left.distance, tie: -left.tie}
}

func protocolFlowCostLess(left, right protocolFlowCost) bool {
	if left.strength != right.strength {
		return left.strength < right.strength
	}
	if left.distance != right.distance {
		return left.distance < right.distance
	}
	return left.tie < right.tie
}

type protocolFlowEdge struct {
	to        int
	rev       int
	capacity  int
	cost      protocolFlowCost
	nativePos int
}

func addProtocolFlowEdge(graph [][]protocolFlowEdge, from, to int, cost protocolFlowCost, nativePos int) {
	forward := protocolFlowEdge{to: to, rev: len(graph[to]), capacity: 1, cost: cost, nativePos: nativePos}
	reverse := protocolFlowEdge{to: from, rev: len(graph[from]), capacity: 0, cost: cost.neg(), nativePos: -1}
	graph[from] = append(graph[from], forward)
	graph[to] = append(graph[to], reverse)
}

func protocolMinimumCostMatching(fallbacks, natives []int, candidates map[int]map[int]protocolCorrelationEdgeResult, observations map[int]protocolCorrelationObservation, refs []protocolFallbackDetailRef) map[int]int {
	if len(fallbacks) == 0 || len(natives) == 0 {
		return nil
	}
	source := 0
	fallbackStart := 1
	nativeStart := fallbackStart + len(fallbacks)
	sink := nativeStart + len(natives)
	graph := make([][]protocolFlowEdge, sink+1)
	for fallbackPos := range fallbacks {
		addProtocolFlowEdge(graph, source, fallbackStart+fallbackPos, protocolFlowCost{}, -1)
	}
	for nativePos := range natives {
		addProtocolFlowEdge(graph, nativeStart+nativePos, sink, protocolFlowCost{}, -1)
	}
	for fallbackPos, fallbackIdx := range fallbacks {
		nativePositions := make([]int, 0, len(candidates[fallbackIdx]))
		for nativeIdx := range candidates[fallbackIdx] {
			for pos, candidateIdx := range natives {
				if candidateIdx == nativeIdx {
					nativePositions = append(nativePositions, pos)
					break
				}
			}
		}
		sort.Slice(nativePositions, func(i, j int) bool {
			return protocolStableOrderLess(observations[natives[nativePositions[i]]].StableOrder, observations[natives[nativePositions[j]]].StableOrder)
		})
		for rank, nativePos := range nativePositions {
			edge := candidates[fallbackIdx][natives[nativePos]]
			addProtocolFlowEdge(graph, fallbackStart+fallbackPos, nativeStart+nativePos, protocolFlowCost{
				strength: -edge.Strength,
				distance: edge.Distance.Nanoseconds(),
				tie:      int64(rank),
			}, nativePos)
		}
	}

	for {
		dist := make([]protocolFlowCost, len(graph))
		previousNode := make([]int, len(graph))
		previousEdge := make([]int, len(graph))
		haveDistance := make([]bool, len(graph))
		for i := range previousNode {
			previousNode[i] = -1
			previousEdge[i] = -1
		}
		haveDistance[source] = true
		queue := []int{source}
		inQueue := make([]bool, len(graph))
		inQueue[source] = true
		for len(queue) > 0 {
			node := queue[0]
			queue = queue[1:]
			inQueue[node] = false
			for edgeIndex, edge := range graph[node] {
				if edge.capacity <= 0 {
					continue
				}
				candidate := dist[node].add(edge.cost)
				if !haveDistance[edge.to] || protocolFlowCostLess(candidate, dist[edge.to]) {
					dist[edge.to] = candidate
					haveDistance[edge.to] = true
					previousNode[edge.to] = node
					previousEdge[edge.to] = edgeIndex
					if !inQueue[edge.to] {
						queue = append(queue, edge.to)
						inQueue[edge.to] = true
					}
				}
			}
		}
		if !haveDistance[sink] {
			break
		}
		for node := sink; node != source && previousNode[node] >= 0; node = previousNode[node] {
			from := previousNode[node]
			edgeIndex := previousEdge[node]
			edge := &graph[from][edgeIndex]
			edge.capacity--
			graph[node][edge.rev].capacity++
		}
	}

	matched := make(map[int]int)
	for fallbackPos, fallbackIdx := range fallbacks {
		for _, edge := range graph[fallbackStart+fallbackPos] {
			if edge.nativePos < 0 || edge.capacity != 0 {
				continue
			}
			matched[fallbackIdx] = natives[edge.nativePos]
			break
		}
	}
	return matched
}

func pairProtocolFallbackDetails(refs []protocolFallbackDetailRef) []protocolFallbackDetailPair {
	observations := make(map[int]protocolCorrelationObservation, len(refs))
	byShapeFallback := make(map[string][]int)
	byShapeNative := make(map[string][]int)
	for i, ref := range refs {
		observation := protocolObservationFromRequestDetail(ref.modelName, ref.detail)
		observation.StableOrder.API = ref.apiName
		observation.StableOrder.Index = ref.index
		observations[i] = observation
		for _, key := range protocolObservationShapeKeys(observation) {
			switch observation.Role {
			case protocolCorrelationRoleFallback:
				byShapeFallback[key] = append(byShapeFallback[key], i)
			case protocolCorrelationRoleNative:
				byShapeNative[key] = append(byShapeNative[key], i)
			}
		}
	}
	candidatesByPartition := make(map[string]map[int]map[int]protocolCorrelationEdgeResult)
	for key, fallbackIndexes := range byShapeFallback {
		nativeIndexes := byShapeNative[key]
		if len(nativeIndexes) == 0 {
			continue
		}
		// Shape collisions are common for small or repeated requests. Correlation
		// can never cross the five-second completion window, so do not construct
		// the full fallback×native product only to reject almost every edge.
		sort.SliceStable(nativeIndexes, func(i, j int) bool {
			return observations[nativeIndexes[i]].StableOrder.Completion.Before(observations[nativeIndexes[j]].StableOrder.Completion)
		})
		for _, fallbackIdx := range fallbackIndexes {
			fallbackAt := observations[fallbackIdx].Timestamp
			if fallbackAt.IsZero() {
				continue
			}
			windowStart := fallbackAt.Add(-protocolFallbackCompletionToleranceMax)
			windowEnd := fallbackAt.Add(protocolFallbackCompletionToleranceMax)
			start := sort.Search(len(nativeIndexes), func(i int) bool {
				return !observations[nativeIndexes[i]].StableOrder.Completion.Before(windowStart)
			})
			end := sort.Search(len(nativeIndexes), func(i int) bool {
				return observations[nativeIndexes[i]].StableOrder.Completion.After(windowEnd)
			})
			for _, nativeIdx := range nativeIndexes[start:end] {
				edge, ok := protocolCorrelationEdge(observations[fallbackIdx], observations[nativeIdx])
				if !ok {
					continue
				}
				partition := protocolObservationPartitionKeys(observations[fallbackIdx])[0]
				if candidatesByPartition[partition] == nil {
					candidatesByPartition[partition] = make(map[int]map[int]protocolCorrelationEdgeResult)
				}
				if candidatesByPartition[partition][fallbackIdx] == nil {
					candidatesByPartition[partition][fallbackIdx] = make(map[int]protocolCorrelationEdgeResult)
				}
				previous, exists := candidatesByPartition[partition][fallbackIdx][nativeIdx]
				if !exists || edge.Strength > previous.Strength || (edge.Strength == previous.Strength && edge.Distance < previous.Distance) {
					candidatesByPartition[partition][fallbackIdx][nativeIdx] = edge
				}
			}
		}
	}
	partitions := make([]string, 0, len(candidatesByPartition))
	for partition := range candidatesByPartition {
		partitions = append(partitions, partition)
	}
	sort.Strings(partitions)
	pairs := make([]protocolFallbackDetailPair, 0)
	for _, partition := range partitions {
		candidateSet := candidatesByPartition[partition]
		fallbacks := make([]int, 0, len(candidateSet))
		nativeSet := make(map[int]struct{})
		for fallbackIdx, nativeCandidates := range candidateSet {
			fallbacks = append(fallbacks, fallbackIdx)
			for nativeIdx := range nativeCandidates {
				nativeSet[nativeIdx] = struct{}{}
			}
		}
		natives := make([]int, 0, len(nativeSet))
		for nativeIdx := range nativeSet {
			natives = append(natives, nativeIdx)
		}
		sort.Slice(fallbacks, func(i, j int) bool {
			return protocolStableOrderLess(observations[fallbacks[i]].StableOrder, observations[fallbacks[j]].StableOrder)
		})
		sort.Slice(natives, func(i, j int) bool {
			return protocolStableOrderLess(observations[natives[i]].StableOrder, observations[natives[j]].StableOrder)
		})
		matched := protocolMinimumCostMatching(fallbacks, natives, candidateSet, observations, refs)
		for _, fallbackIdx := range fallbacks {
			nativeIdx, ok := matched[fallbackIdx]
			if !ok {
				continue
			}
			edge := candidateSet[fallbackIdx][nativeIdx]
			pairs = append(pairs, protocolFallbackDetailPair{fallback: refs[fallbackIdx], native: refs[nativeIdx], distance: edge.Distance})
		}
	}
	return pairs
}

// protocolCacheReadsCompatible 在两侧都报了非零缓存命中时要求相等。这是判别项而
// 不是关联键的一部分:兜底记录解析的是翻译后的响应体,缺这项时必须保持宽松,否则
// 又会退回「配不上」。两侧口径确实一致——CPA 的 Claude→OpenAI 翻译把
// cache_read_input_tokens 原样写进 prompt_tokens_details.cached_tokens(创建量另走
// cached_creation_tokens),Codex 原生与响应体也相同(真实数据 2432/2432、
// 33920/33920)。命中量可以比,创建量不行:插件不解析 cached_creation_tokens 这个
// 拼写,兜底侧恒为 0。
func protocolCacheReadsCompatible(fallbackCacheRead, nativeCacheRead int64) bool {
	if fallbackCacheRead <= 0 || nativeCacheRead <= 0 {
		return true
	}
	return fallbackCacheRead == nativeCacheRead
}

func reconcilePersistedProtocolFallbacks(records []persistedDetail) ([]persistedDetail, int) {
	refs := make([]protocolFallbackDetailRef, 0, len(records))
	for i, record := range records {
		if record.MetadataOnly {
			continue
		}
		detail := record.Detail
		modelName := normalizeDetailModelName(record.Model, detail.Model)
		detail.Model = modelName
		detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
		detail.Source = cleanImportedDetailSource(detail)
		detail = normalizeStoredClientAPIIdentity(detail)
		refs = append(refs, protocolFallbackDetailRef{
			apiName: strings.TrimSpace(record.API), modelName: modelName, index: i, detail: detail,
		})
	}
	pairs := pairProtocolFallbackDetails(refs)
	if len(pairs) == 0 {
		return records, 0
	}
	result := append([]persistedDetail(nil), records...)
	removed := make(map[int]struct{}, len(pairs))
	for _, pair := range pairs {
		native := result[pair.native.index].Detail
		enrichRequestDetailMetadata(&native, pair.fallback.detail)
		result[pair.native.index].Detail = native
		removed[pair.fallback.index] = struct{}{}
	}
	kept := make([]persistedDetail, 0, len(result)-len(removed))
	for i, record := range result {
		if _, drop := removed[i]; drop {
			continue
		}
		kept = append(kept, record)
	}
	return kept, len(removed)
}

func reconcileProtocolFallbackSnapshot(snapshot StatisticsSnapshot) (StatisticsSnapshot, int) {
	result := snapshot
	result.RequestsByDay = copyStringInt64Map(snapshot.RequestsByDay)
	result.RequestsByHour = copyStringInt64Map(snapshot.RequestsByHour)
	result.TokensByDay = copyStringInt64Map(snapshot.TokensByDay)
	result.TokensByHour = copyStringInt64Map(snapshot.TokensByHour)
	result.CostByDay = copyStringFloat64Map(snapshot.CostByDay)
	result.CostByHour = copyStringFloat64Map(snapshot.CostByHour)
	result.CostTokensByDay = cloneTimeSeriesTokenStatsByDaySnapshot(snapshot.CostTokensByDay)
	result.CostTokensByHour = cloneTimeSeriesTokenStatsByHourSnapshot(snapshot.CostTokensByHour)
	result.APIs = make(map[string]APISnapshot, len(snapshot.APIs))
	refs := make([]protocolFallbackDetailRef, 0)
	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := apiSnapshot
		apiCopy.Models = make(map[string]ModelSnapshot, len(apiSnapshot.Models))
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCopy := modelSnapshot
			modelCopy.Providers = append([]ModelProviderStat(nil), modelSnapshot.Providers...)
			modelCopy.Details = make([]RequestDetail, len(modelSnapshot.Details))
			for i, detail := range modelCopy.Details {
				detail = cloneRequestDetail(modelSnapshot.Details[i])
				detail.Model = normalizeDetailModelName(modelName, detail.Model)
				detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
				modelCopy.Details[i] = detail
				refs = append(refs, protocolFallbackDetailRef{
					apiName: apiName, modelName: modelName, index: i, detail: detail,
				})
			}
			apiCopy.Models[modelName] = modelCopy
		}
		result.APIs[apiName] = apiCopy
	}
	pairs := pairProtocolFallbackDetails(refs)
	if len(pairs) == 0 {
		return result, 0
	}

	daySeries := timeSeriesTokenStatsByDayFromSnapshot(snapshot.CostTokensByDay)
	hourSeries := timeSeriesTokenStatsByHourFromSnapshot(snapshot.CostTokensByHour)
	removeByModel := make(map[string]map[int]struct{})
	for _, pair := range pairs {
		apiSnapshot := result.APIs[pair.native.apiName]
		modelSnapshot := apiSnapshot.Models[pair.native.modelName]
		native := modelSnapshot.Details[pair.native.index]
		enrichRequestDetailMetadata(&native, pair.fallback.detail)
		modelSnapshot.Details[pair.native.index] = native
		apiSnapshot.Models[pair.native.modelName] = modelSnapshot
		result.APIs[pair.native.apiName] = apiSnapshot

		key := pair.fallback.apiName + "\x00" + pair.fallback.modelName
		if removeByModel[key] == nil {
			removeByModel[key] = make(map[int]struct{})
		}
		removeByModel[key][pair.fallback.index] = struct{}{}
		decrementProtocolFallbackSnapshotDetail(&result, daySeries, hourSeries, pair.fallback.apiName, pair.fallback.modelName, pair.fallback.detail)
	}
	for compound, indexes := range removeByModel {
		parts := strings.SplitN(compound, "\x00", 2)
		apiSnapshot := result.APIs[parts[0]]
		modelSnapshot := apiSnapshot.Models[parts[1]]
		kept := make([]RequestDetail, 0, len(modelSnapshot.Details)-len(indexes))
		for i, detail := range modelSnapshot.Details {
			if _, drop := indexes[i]; drop {
				continue
			}
			kept = append(kept, detail)
		}
		modelSnapshot.Details = kept
		if modelSnapshot.TotalRequests <= 0 && len(modelSnapshot.Details) == 0 {
			delete(apiSnapshot.Models, parts[1])
		} else {
			apiSnapshot.Models[parts[1]] = modelSnapshot
		}
		if apiSnapshot.TotalRequests <= 0 && len(apiSnapshot.Models) == 0 {
			delete(result.APIs, parts[0])
		} else {
			result.APIs[parts[0]] = apiSnapshot
		}
	}
	result.CostTokensByDay = timeSeriesTokenStatsByDaySnapshot(daySeries)
	result.CostTokensByHour = timeSeriesTokenStatsByHourSnapshot(hourSeries)
	// This pure snapshot repair has no pricing context with which to subtract
	// dollars safely. Callers that retain the repaired snapshot rebuild these
	// two derived series from CostTokensBy*, while the no-pair path can preserve
	// the original values unchanged.
	result.CostByDay = nil
	result.CostByHour = nil
	return result, len(pairs)
}

// reconcileProtocolFallbackSnapshot may enrich details and subtract the
// fallback's token series. Keep the returned snapshot fully independent even
// when no pair is found; callers commonly retain the input for a later import
// or restore attempt.
func cloneTimeSeriesTokenStatsByDaySnapshot(values map[string][]TimeSeriesTokenStat) map[string][]TimeSeriesTokenStat {
	if values == nil {
		return nil
	}
	cloned := make(map[string][]TimeSeriesTokenStat, len(values))
	for bucket, stats := range values {
		cloned[bucket] = append([]TimeSeriesTokenStat(nil), stats...)
	}
	return cloned
}

func cloneTimeSeriesTokenStatsByHourSnapshot(values map[string][]TimeSeriesTokenStat) map[string][]TimeSeriesTokenStat {
	return cloneTimeSeriesTokenStatsByDaySnapshot(values)
}

func decrementProtocolFallbackSnapshotDetail(snapshot *StatisticsSnapshot, daySeries map[string]map[string]*TimeSeriesTokenStat,
	hourSeries map[int]map[string]*TimeSeriesTokenStat, apiName, modelName string, detail RequestDetail,
) {
	if snapshot == nil {
		return
	}
	totals := detailTotalsFromRequest(detail)
	decrementSnapshotAggregate(&snapshot.TotalRequests, &snapshot.SuccessCount, &snapshot.FailureCount,
		&snapshot.TotalTokens, &snapshot.InputTokens, &snapshot.OutputTokens, &snapshot.CachedTokens,
		&snapshot.CacheWriteTokens, &snapshot.ReasoningTokens, detail, totals)

	apiSnapshot := snapshot.APIs[apiName]
	decrementSnapshotAggregate(&apiSnapshot.TotalRequests, &apiSnapshot.SuccessCount, &apiSnapshot.FailureCount,
		&apiSnapshot.TotalTokens, &apiSnapshot.InputTokens, &apiSnapshot.OutputTokens, &apiSnapshot.CachedTokens,
		&apiSnapshot.CacheWriteTokens, &apiSnapshot.ReasoningTokens, detail, totals)
	modelSnapshot := apiSnapshot.Models[modelName]
	decrementSnapshotAggregate(&modelSnapshot.TotalRequests, &modelSnapshot.SuccessCount, &modelSnapshot.FailureCount,
		&modelSnapshot.TotalTokens, &modelSnapshot.InputTokens, &modelSnapshot.OutputTokens, &modelSnapshot.CachedTokens,
		&modelSnapshot.CacheWriteTokens, &modelSnapshot.ReasoningTokens, detail, totals)
	if len(modelSnapshot.Providers) > 0 {
		providerStats := modelProviderStatsFromSnapshot(modelSnapshot.Providers)
		decrementModelProviderStats(providerStats, detail.Provider, detail.Failed, totals)
		modelSnapshot.Providers = finalizedModelProviderStats(providerStats, modelSnapshot.TotalRequests, modelSnapshot.SuccessCount,
			modelSnapshot.FailureCount, modelSnapshot.TotalTokens, modelSnapshot.InputTokens, modelSnapshot.OutputTokens,
			modelSnapshot.CachedTokens, modelSnapshot.CacheWriteTokens, modelSnapshot.ReasoningTokens)
	}
	apiSnapshot.Models[modelName] = modelSnapshot
	snapshot.APIs[apiName] = apiSnapshot

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := strconv.Itoa(detail.Timestamp.Hour())
	decrementSnapshotSeriesValue(snapshot.RequestsByDay, dayKey, 1)
	decrementSnapshotSeriesValue(snapshot.RequestsByHour, hourKey, 1)
	decrementSnapshotSeriesValue(snapshot.TokensByDay, dayKey, totals.totalTokens)
	decrementSnapshotSeriesValue(snapshot.TokensByHour, hourKey, totals.totalTokens)
	if decrementTimeSeriesTokenStats(daySeries[dayKey], detailModel(modelName, detail), detail.Provider, totals) && len(daySeries[dayKey]) == 0 {
		delete(daySeries, dayKey)
	}
	if decrementTimeSeriesTokenStats(hourSeries[detail.Timestamp.Hour()], detailModel(modelName, detail), detail.Provider, totals) && len(hourSeries[detail.Timestamp.Hour()]) == 0 {
		delete(hourSeries, detail.Timestamp.Hour())
	}
}

func decrementSnapshotAggregate(totalRequests, successCount, failureCount, totalTokens, inputTokens, outputTokens,
	cachedTokens, cacheWriteTokens, reasoningTokens *int64, detail RequestDetail, totals detailTotals,
) {
	*totalRequests = subtractNonNegativeInt64(*totalRequests, 1)
	if detail.Failed {
		*failureCount = subtractNonNegativeInt64(*failureCount, 1)
	} else {
		*successCount = subtractNonNegativeInt64(*successCount, 1)
	}
	*totalTokens = subtractNonNegativeInt64(*totalTokens, totals.totalTokens)
	*inputTokens = subtractNonNegativeInt64(*inputTokens, totals.inputTokens)
	*outputTokens = subtractNonNegativeInt64(*outputTokens, totals.outputTokens)
	*cachedTokens = subtractNonNegativeInt64(*cachedTokens, totals.cachedTokens)
	*cacheWriteTokens = subtractNonNegativeInt64(*cacheWriteTokens, totals.cacheWriteTokens)
	*reasoningTokens = subtractNonNegativeInt64(*reasoningTokens, totals.reasoningTokens)
}

func decrementSnapshotSeriesValue(values map[string]int64, key string, delta int64) {
	if values == nil {
		return
	}
	values[key] = subtractNonNegativeInt64(values[key], delta)
	if values[key] == 0 {
		delete(values, key)
	}
}

func (s *RequestStatistics) reconcileRecordedProtocolFallbacksLocked(now time.Time) int {
	if s == nil {
		return 0
	}
	s.protocolFallbackReconcileDirty = false
	refs := make([]protocolFallbackDetailRef, 0)
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for i, detail := range modelSt.Details {
				refs = append(refs, protocolFallbackDetailRef{
					apiName: apiName, modelName: modelName, index: i, detail: detail,
				})
			}
		}
	}
	pairs := pairProtocolFallbackDetails(refs)
	if len(pairs) == 0 {
		return 0
	}

	for _, pair := range pairs {
		apiSt := s.apis[pair.native.apiName]
		modelSt := apiSt.Models[pair.native.modelName]
		enrichRequestDetailMetadata(&modelSt.Details[pair.native.index], pair.fallback.detail)
	}
	removeByModel := make(map[string][]protocolFallbackDetailRef)
	for _, pair := range pairs {
		key := pair.fallback.apiName + "\x00" + pair.fallback.modelName
		removeByModel[key] = append(removeByModel[key], pair.fallback)
	}
	for compound, removals := range removeByModel {
		parts := strings.SplitN(compound, "\x00", 2)
		apiSt := s.apis[parts[0]]
		modelSt := apiSt.Models[parts[1]]
		sort.Slice(removals, func(i, j int) bool { return removals[i].index > removals[j].index })
		for _, removal := range removals {
			detail := modelSt.Details[removal.index]
			s.decrementCounters(detail, apiSt, modelSt, parts[1])
			modelSt.Details = append(modelSt.Details[:removal.index], modelSt.Details[removal.index+1:]...)
		}
	}
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			delete(s.apis, apiName)
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil || (len(modelSt.Details) == 0 && modelSt.TotalRequests <= 0) {
				delete(apiSt.Models, modelName)
			}
		}
		if len(apiSt.Models) == 0 && apiSt.TotalRequests <= 0 {
			delete(s.apis, apiName)
		}
	}
	s.pruneLocked(now, true)
	s.rebuildSeenLocked(now)
	s.invalidateSummaryLocked()
	return len(pairs)
}

func (s *RequestStatistics) ReconcileProtocolFallbacks() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	corrected := s.reconcileRecordedProtocolFallbacksLocked(time.Now())
	s.mu.Unlock()
	return corrected
}

// ReconciledSnapshot repairs any pairable protocol fallbacks and returns the
// snapshot and detail count under the same lock. Export uses this atomic form
// so a fallback cannot be committed between reconciliation and serialization.
func (s *RequestStatistics) ReconciledSnapshot() (StatisticsSnapshot, int64) {
	if s == nil {
		return StatisticsSnapshot{}, 0
	}
	s.mu.Lock()
	if s.protocolFallbackReconcileDirty {
		s.reconcileRecordedProtocolFallbacksLocked(time.Now())
	}
	snapshot := s.snapshotLocked()
	detailCount := s.countDetailsLocked()
	s.mu.Unlock()
	return snapshot, detailCount
}

func (s *RequestStatistics) RemoveRecordedUsage(record UsageRecord) bool {
	if s == nil || record.RequestedAt.IsZero() {
		return false
	}
	apiName := usageGroupKey(record)
	modelName := firstNonEmpty(record.Model, "unknown")
	target := dedupKey(apiName, modelName, requestDetailFromUsageRecord(record, record.RequestedAt, headerWhitelist{}))
	s.mu.Lock()
	defer s.mu.Unlock()
	apiSt := s.apis[apiName]
	if apiSt == nil || apiSt.Models[modelName] == nil {
		return false
	}
	modelSt := apiSt.Models[modelName]
	for i := len(modelSt.Details) - 1; i >= 0; i-- {
		if dedupKey(apiName, modelName, modelSt.Details[i]) != target {
			continue
		}
		detail := modelSt.Details[i]
		s.decrementCounters(detail, apiSt, modelSt, modelName)
		modelSt.Details = append(modelSt.Details[:i], modelSt.Details[i+1:]...)
		if len(modelSt.Details) == 0 && modelSt.TotalRequests <= 0 {
			delete(apiSt.Models, modelName)
		}
		if len(apiSt.Models) == 0 && apiSt.TotalRequests <= 0 {
			delete(s.apis, apiName)
		}
		s.rebuildSeenLocked(time.Now())
		s.invalidateSummaryLocked()
		return true
	}
	return false
}
