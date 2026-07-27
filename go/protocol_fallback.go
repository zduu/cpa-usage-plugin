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
// by callback scheduling time. Keep the window tight so an anonymous protocol
// fallback cannot consume an unrelated native request merely because its token
// counts happen to match.
const protocolFallbackCompletionTolerance = time.Second

func isAnonymousOpenAIProtocolFallback(record UsageRecord) bool {
	return strings.EqualFold(strings.TrimSpace(record.Provider), "openai-compatible") &&
		strings.TrimSpace(record.AuthID) == "" &&
		strings.TrimSpace(record.AuthIndex) == "" &&
		record.Latency <= 0 &&
		record.TTFT <= 0 &&
		isOpenAIProtocolFallbackEndpoint(record.Endpoint)
}

func isNativeProtocolCorrelationRecord(record UsageRecord) bool {
	return !isAnonymousOpenAIProtocolFallback(record) &&
		usageProviderFamily(record.Provider) == "codex" &&
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
		usageProviderFamily(detail.Provider) == "codex" &&
		(strings.TrimSpace(detail.AuthID) != "" || strings.TrimSpace(detail.AuthIndex) != "") &&
		detail.LatencyMs > 0
}

func isOpenAIProtocolFallbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(endpoint)), "/")
	return endpoint == "/v1/chat/completions" || endpoint == "/v1/responses"
}

func usageProtocolCorrelationKey(record UsageRecord) string {
	if !isAnonymousOpenAIProtocolFallback(record) && !isNativeProtocolCorrelationRecord(record) {
		return ""
	}
	cacheRead := maxInt64(nonNegativeInt64(record.Detail.CachedTokens), nonNegativeInt64(record.Detail.CacheReadTokens))
	cacheWrite := nonNegativeInt64(record.Detail.CacheCreationTokens)
	return protocolCorrelationKey(
		firstNonEmpty(record.Alias, record.Model),
		canonicalClientAPIKey(record.APIKey),
		record.Failed,
		record.Failure.StatusCode,
		nonNegativeInt64(record.Detail.InputTokens),
		nonNegativeInt64(record.Detail.OutputTokens),
		nonNegativeInt64(record.Detail.ReasoningTokens),
		cacheRead,
		cacheRead+cacheWrite,
		cacheWrite,
		usageDetailTotalTokens(record.Detail, record.Provider),
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
	return protocolCorrelationKey(
		detailModel(modelName, detail),
		clientIdentity,
		detail.Failed,
		detail.StatusCode,
		nonNegativeInt64(detail.Tokens.InputTokens),
		nonNegativeInt64(detail.Tokens.OutputTokens),
		nonNegativeInt64(detail.Tokens.ReasoningTokens),
		nonNegativeInt64(detail.Tokens.CachedTokens),
		nonNegativeInt64(detail.Tokens.CacheTokens),
		nonNegativeInt64(detail.Tokens.CacheWriteTokens),
		detailTotalTokensForRequest(detail),
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
	fallbackAt := fallback.RequestedAt
	nativeCompletedAt := native.RequestedAt.Add(native.Latency)
	if fallbackAt.IsZero() || native.RequestedAt.IsZero() {
		return 0, false
	}
	distance := fallbackAt.Sub(nativeCompletedAt)
	if distance < 0 {
		distance = -distance
	}
	return distance, distance <= protocolFallbackCompletionTolerance
}

func protocolEndpointsCompatible(fallbackEndpoint, nativeEndpoint string) bool {
	nativeEndpoint = strings.TrimRight(strings.ToLower(strings.TrimSpace(nativeEndpoint)), "/")
	return nativeEndpoint == "" || nativeEndpoint == strings.TrimRight(strings.ToLower(strings.TrimSpace(fallbackEndpoint)), "/")
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
		fallbackIndex, nativeIndex := 0, 0
		for fallbackIndex < len(fallbacks) && nativeIndex < len(natives) {
			fallbackRef := refs[fallbacks[fallbackIndex]]
			nativeRef := refs[natives[nativeIndex]]
			fallbackAt := fallbackRef.detail.Timestamp
			nativeAt := nativeRef.detail.Timestamp.Add(time.Duration(nativeRef.detail.LatencyMs) * time.Millisecond)
			if fallbackAt.IsZero() {
				fallbackIndex++
				continue
			}
			if nativeRef.detail.Timestamp.IsZero() {
				nativeIndex++
				continue
			}
			delta := fallbackAt.Sub(nativeAt)
			if delta < -protocolFallbackCompletionTolerance {
				fallbackIndex++
				continue
			}
			if delta > protocolFallbackCompletionTolerance {
				nativeIndex++
				continue
			}
			if !protocolEndpointsCompatible(fallbackRef.detail.Endpoint, nativeRef.detail.Endpoint) {
				if delta <= 0 {
					fallbackIndex++
				} else {
					nativeIndex++
				}
				continue
			}
			distance := delta
			if distance < 0 {
				distance = -distance
			}
			pairs = append(pairs, protocolFallbackDetailPair{
				fallback: fallbackRef, native: nativeRef, distance: distance,
			})
			fallbackIndex++
			nativeIndex++
		}
	}
	return pairs
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
