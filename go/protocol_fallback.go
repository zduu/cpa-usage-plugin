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
)

func protocolFallbackTolerance(latency time.Duration) time.Duration {
	if latency <= 0 {
		return protocolFallbackCompletionTolerance
	}
	tolerance := protocolFallbackCompletionTolerance + latency/protocolFallbackCompletionToleranceRatio
	if tolerance > protocolFallbackCompletionToleranceMax {
		return protocolFallbackCompletionToleranceMax
	}
	return tolerance
}

func isAnonymousOpenAIProtocolFallback(record UsageRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Provider), "openai-compatible") &&
		strings.TrimSpace(record.AuthID) == "" &&
		strings.TrimSpace(record.AuthIndex) == "" &&
		record.Latency <= 0 &&
		record.TTFT <= 0 &&
		isOpenAIProtocolFallbackEndpoint(record.Endpoint)
}

// isNativeProtocolCorrelationRecord accepts a native record of ANY upstream
// family as a correlation candidate. It used to require the codex family, on
// the assumption that an OpenAI-protocol client request could only have been
// served by Codex — but CPA routes /v1/chat/completions and /v1/responses to
// whichever upstream serves the model, Claude-protocol upstreams included.
// The anonymous fallback carries no upstream identity at all (CPA's conductor
// publishes selected_auth_id into a *cloned* metadata map, so the response
// interceptor never sees it), so its provider is guessed from the client
// protocol as "openai-compatible". Restricting the rescue to codex left every
// Claude-upstream fallback both misattributed and double counted.
func isNativeProtocolCorrelationRecord(record UsageRecord) bool {
	return !isAnonymousOpenAIProtocolFallback(record) &&
		(strings.TrimSpace(record.AuthID) != "" || strings.TrimSpace(record.AuthIndex) != "") &&
		record.Latency > 0
}

func isAnonymousOpenAIProtocolFallbackDetail(detail RequestDetail) bool {
	return strings.EqualFold(strings.TrimSpace(detail.Provider), "openai-compatible") &&
		strings.TrimSpace(detail.AuthID) == "" &&
		strings.TrimSpace(detail.AuthIndex) == "" &&
		detail.LatencyMs <= 0 &&
		detail.TTFTMs <= 0 &&
		isOpenAIProtocolFallbackEndpoint(detail.Endpoint)
}

func isNativeProtocolCorrelationDetail(detail RequestDetail) bool {
	return !isAnonymousOpenAIProtocolFallbackDetail(detail) &&
		(strings.TrimSpace(detail.AuthID) != "" || strings.TrimSpace(detail.AuthIndex) != "") &&
		detail.LatencyMs > 0
}

func isOpenAIProtocolFallbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(endpoint)), "/")
	return endpoint == "/v1/chat/completions" || endpoint == "/v1/responses"
}

// usageProtocolCorrelationKey keys an anonymous fallback against the native
// record of the same request. The two sides count cache differently — a
// Claude-family native keeps input exclusive of cache reads/creations, while
// the fallback parses an OpenAI-shaped body whose prompt tokens already
// include cache — so the raw token fields cannot be compared directly. Both
// sides do agree on the request's true total and on output, so the key uses
// the convention-neutral pair (total - output, output): cache is folded in
// wherever it was reported. The individual cache fields are deliberately
// excluded; a translated response body may drop prompt_tokens_details
// entirely, and requiring them to match would reject legitimate pairs.
func usageProtocolCorrelationKey(record UsageRecord) string {
	if !isAnonymousOpenAIProtocolFallback(record) && !isNativeProtocolCorrelationRecord(record) {
		return ""
	}
	totalTokens := usageDetailTotalTokens(record.Detail, record.Provider)
	outputTokens := nonNegativeInt64(record.Detail.OutputTokens)
	return protocolCorrelationKey(
		firstNonEmpty(record.Alias, record.Model),
		canonicalClientAPIKey(record.APIKey),
		record.Failed,
		record.Failure.StatusCode,
		maxInt64(totalTokens-outputTokens, 0),
		outputTokens,
		nonNegativeInt64(record.Detail.ReasoningTokens),
	)
}

func detailProtocolCorrelationKey(modelName string, detail RequestDetail) string {
	if !isAnonymousOpenAIProtocolFallbackDetail(detail) && !isNativeProtocolCorrelationDetail(detail) {
		return ""
	}
	clientIdentity := strings.TrimSpace(detail.APIKeyHash)
	if clientIdentity == "" {
		clientIdentity = strings.TrimSpace(detail.APIKey)
	}
	totalTokens := detailTotalTokensForRequest(detail)
	outputTokens := nonNegativeInt64(detail.Tokens.OutputTokens)
	return protocolCorrelationKey(
		detailModel(modelName, detail),
		clientIdentity,
		detail.Failed,
		detail.StatusCode,
		maxInt64(totalTokens-outputTokens, 0),
		outputTokens,
		nonNegativeInt64(detail.Tokens.ReasoningTokens),
	)
}

func protocolCorrelationKey(model, clientIdentity string, failed bool, statusCode int, tokens ...int64) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(model)),
		strings.ToLower(strings.TrimSpace(clientIdentity)),
		strconv.FormatBool(failed),
		strconv.Itoa(statusCode),
	}
	for _, value := range tokens {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, "\x00")
}

func protocolUsageCompletionDistance(fallback, native UsageRecord) (time.Duration, bool) {
	if !isAnonymousOpenAIProtocolFallback(fallback) || !isNativeProtocolCorrelationRecord(native) {
		return 0, false
	}
	if usageProtocolCorrelationKey(fallback) == "" || usageProtocolCorrelationKey(fallback) != usageProtocolCorrelationKey(native) {
		return 0, false
	}
	if !protocolEndpointsCompatible(fallback.Endpoint, native.Endpoint) {
		return 0, false
	}
	fallbackCacheRead, _, _ := usageDetailCacheTokenParts(fallback.Detail, fallback.Provider)
	nativeCacheRead, _, _ := usageDetailCacheTokenParts(native.Detail, native.Provider)
	if !protocolCacheReadsCompatible(fallbackCacheRead, nativeCacheRead) {
		return 0, false
	}
	fallbackAt := fallback.RequestedAt
	nativeCompletedAt := native.RequestedAt.Add(native.Latency)
	if fallbackAt.IsZero() || native.RequestedAt.IsZero() {
		return 0, false
	}
	distance := fallbackAt.Sub(nativeCompletedAt)
	if distance < 0 {
		distance = -distance
	}
	return distance, distance <= protocolFallbackTolerance(native.Latency)
}

// protocolEndpointsCompatible guards against pairing a /v1/chat/completions
// fallback with a /v1/responses native record. The two fields are only
// comparable when both describe the same protocol: the fallback endpoint is
// derived from the CLIENT protocol, while a native record carries the UPSTREAM
// path. They coincide for Codex (both /v1/responses) but not for a Claude
// upstream serving an OpenAI-protocol client, where the native side reports
// /v1/messages. Vetoing on that difference would reject exactly the pairs this
// correlation exists to find, so the check only applies when the native
// endpoint is itself an OpenAI-protocol path.
func protocolEndpointsCompatible(fallbackEndpoint, nativeEndpoint string) bool {
	nativeEndpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(nativeEndpoint)), "/")
	if nativeEndpoint == "" || !isOpenAIProtocolFallbackEndpoint(nativeEndpoint) {
		return true
	}
	return nativeEndpoint == strings.TrimRight(strings.ToLower(strings.TrimSpace(fallbackEndpoint)), "/")
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

func pairProtocolFallbackDetails(refs []protocolFallbackDetailRef) []protocolFallbackDetailPair {
	byKeyFallback := make(map[string][]int)
	byKeyNative := make(map[string][]int)
	for i, ref := range refs {
		key := detailProtocolCorrelationKey(ref.modelName, ref.detail)
		if key == "" {
			continue
		}
		switch {
		case isAnonymousOpenAIProtocolFallbackDetail(ref.detail):
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
	result.APIs = make(map[string]APISnapshot, len(snapshot.APIs))
	refs := make([]protocolFallbackDetailRef, 0)
	for apiName, apiSnapshot := range snapshot.APIs {
		apiCopy := apiSnapshot
		apiCopy.Models = make(map[string]ModelSnapshot, len(apiSnapshot.Models))
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCopy := modelSnapshot
			modelCopy.Providers = append([]ModelProviderStat(nil), modelSnapshot.Providers...)
			modelCopy.Details = append([]RequestDetail(nil), modelSnapshot.Details...)
			for i, detail := range modelCopy.Details {
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
		return snapshot, 0
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
	result.CostByDay = nil
	result.CostByHour = nil
	return result, len(pairs)
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
	*totalRequests = maxInt64(*totalRequests-1, 0)
	if detail.Failed {
		*failureCount = maxInt64(*failureCount-1, 0)
	} else {
		*successCount = maxInt64(*successCount-1, 0)
	}
	*totalTokens = maxInt64(*totalTokens-totals.totalTokens, 0)
	*inputTokens = maxInt64(*inputTokens-totals.inputTokens, 0)
	*outputTokens = maxInt64(*outputTokens-totals.outputTokens, 0)
	*cachedTokens = maxInt64(*cachedTokens-totals.cachedTokens, 0)
	*cacheWriteTokens = maxInt64(*cacheWriteTokens-totals.cacheWriteTokens, 0)
	*reasoningTokens = maxInt64(*reasoningTokens-totals.reasoningTokens, 0)
}

func decrementSnapshotSeriesValue(values map[string]int64, key string, delta int64) {
	if values == nil {
		return
	}
	values[key] = maxInt64(values[key]-delta, 0)
	if values[key] == 0 {
		delete(values, key)
	}
}

func (s *RequestStatistics) reconcileRecordedProtocolFallbacksLocked(now time.Time) int {
	if s == nil {
		return 0
	}
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
