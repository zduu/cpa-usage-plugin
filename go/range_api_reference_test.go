package main

import (
	"container/heap"
	"sort"
	"strings"
	"time"
)

// Pre-optimization API detail traversal, kept as an independent oracle.
func (s *RequestStatistics) referenceAPIDetailForClientAPIAt(api string, rangeKey string, clientAPI string, recentLimit int, errorLimit int, now time.Time) APIDetailResponse {
	startedAt := time.Now()
	if now.IsZero() {
		now = startedAt
	}
	result := APIDetailResponse{
		API:         api,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}
	if s == nil {
		return result
	}
	recentLimit, errorLimit = normalizeDashboardAPIDetailLimits(recentLimit, errorLimit)

	cutoff := dashboardRangeCutoff(rangeKey, now)

	s.mu.Lock()
	defer s.mu.Unlock()
	generatedAt := s.dashboardQueryGeneratedAtLocked(rangeKey, now).UTC().Format(time.RFC3339)
	result.GeneratedAt = generatedAt
	finish := func(result APIDetailResponse) APIDetailResponse {
		s.attachEventCostsLocked(result.RecentEvents)
		result.Summary.EstimatedCost = s.applyModelEstimatedCostsLocked(result.ModelStats)
		result.dashboardVersion = s.summaryVersion
		s.apiDetailQueries++
		s.lastAPIDetailDuration = time.Since(startedAt)
		s.lastAPIDetailTotal = result.TotalEvents
		return result
	}

	apiSt := s.apis[api]
	aggregateScope := cutoff.IsZero() && strings.TrimSpace(clientAPI) == ""
	if aggregateScope {
		result.Summary = apiDetailSummaryFromAPIStats(apiSt)
		result.ModelStats = apiDetailModelStatsFromAPIStats(apiSt)
		result.SourceStats = apiDetailSourceStatsFromAPIStats(apiSt)
		result.TotalEvents = nonNegativeIntFromInt64(result.Summary.TotalRequests)
	}

	if apiSt == nil {
		return finish(result)
	}

	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*SourceStat)
	errorAgg := make(map[apiDetailErrorKey]*APIDetailErrorStat)
	recentEvents := make(dashboardEventHeap, 0, recentLimit)
	heap.Init(&recentEvents)
	// Select recent visible records newest-first, independently of accounting
	// iteration. Aggregation keeps its original order (including the provider
	// chosen for a shared source), while the heap avoids replacing every entry
	// as an ascending history scan encounters newer requests.
	for modelName, model := range apiSt.Models {
		if model == nil {
			continue
		}
		for i := len(model.Details) - 1; i >= 0; i-- {
			d := &model.Details[i]
			if dashboardEventPastCutoff(d, cutoff) || !clientAPISelectorMatchesDetail(clientAPI, *d) {
				continue
			}
			appendBoundedDashboardEventHeap(&recentEvents, dashboardEventDetail{detail: d, upstreamAPI: api, sortKey: d.Model, modelName: modelName, sequence: int64(i)}, recentLimit)
		}
	}
	var latencySum int64
	var latencyN int64
	// Without time rules finish computes prices from provider-aware totals.
	// Per-record pricing would be discarded by applyModelEstimatedCostsLocked.
	needsDetailPrices := s.hasTimeBasedPricesLocked()
	pricer := queryDetailPricer{stats: s}

	for dm := range apiAccountingEvents(api, apiSt) {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(&d, cutoff) {
			continue
		}
		if !clientAPISelectorMatchesDetail(clientAPI, d) {
			continue
		}
		totalTokens := detailTotalTokensForRequest(d)
		inputTokens := nonNegativeInt64(d.Tokens.InputTokens)
		outputTokens := nonNegativeInt64(d.Tokens.OutputTokens)
		reasoningTokens := nonNegativeInt64(d.Tokens.ReasoningTokens)
		cachedTokens := normalizedCacheReadTokens(d.Tokens)
		cacheWriteTokens := nonNegativeInt64(d.Tokens.CacheWriteTokens)

		if !aggregateScope {
			result.TotalEvents++
			result.Summary.TotalRequests = addNonNegativeInt64(result.Summary.TotalRequests, 1)
			if d.Failed {
				result.Summary.FailureCount = addNonNegativeInt64(result.Summary.FailureCount, 1)
			} else {
				result.Summary.SuccessCount = addNonNegativeInt64(result.Summary.SuccessCount, 1)
			}
			result.Summary.TotalTokens = addNonNegativeInt64(result.Summary.TotalTokens, totalTokens)
			result.Summary.InputTokens = addNonNegativeInt64(result.Summary.InputTokens, inputTokens)
			result.Summary.OutputTokens = addNonNegativeInt64(result.Summary.OutputTokens, outputTokens)
			result.Summary.CachedTokens = addNonNegativeInt64(result.Summary.CachedTokens, cachedTokens)
			result.Summary.CacheWriteTokens = addNonNegativeInt64(result.Summary.CacheWriteTokens, cacheWriteTokens)
			result.Summary.ReasoningTokens = addNonNegativeInt64(result.Summary.ReasoningTokens, reasoningTokens)
			if d.LatencyMs > 0 {
				latencySum = addNonNegativeInt64(latencySum, d.LatencyMs)
				latencyN = addNonNegativeInt64(latencyN, 1)
			}

			modelLabel := normalizeModelName(dm.modelName)
			if d.Model != "" {
				modelLabel = d.Model
			}
			ms, ok := modelAgg[modelLabel]
			if !ok {
				ms = &ModelStat{Model: modelLabel}
				modelAgg[modelLabel] = ms
			}
			ms.TotalRequests = addNonNegativeInt64(ms.TotalRequests, 1)
			if d.Failed {
				ms.FailureCount = addNonNegativeInt64(ms.FailureCount, 1)
			} else {
				ms.SuccessCount = addNonNegativeInt64(ms.SuccessCount, 1)
			}
			ms.TotalTokens = addNonNegativeInt64(ms.TotalTokens, totalTokens)
			if needsDetailPrices {
				ms.EstimatedCost = addNonNegativeCost(ms.EstimatedCost, pricer.cost(modelLabel, d, detailTotals{totalTokens: totalTokens, inputTokens: inputTokens, outputTokens: outputTokens, cachedTokens: cachedTokens, cacheWriteTokens: cacheWriteTokens, reasoningTokens: reasoningTokens}))
			}
			ms.InputTokens = addNonNegativeInt64(ms.InputTokens, inputTokens)
			ms.OutputTokens = addNonNegativeInt64(ms.OutputTokens, outputTokens)
			ms.CachedTokens = addNonNegativeInt64(ms.CachedTokens, cachedTokens)
			ms.CacheWriteTokens = addNonNegativeInt64(ms.CacheWriteTokens, cacheWriteTokens)
			ms.ReasoningTokens = addNonNegativeInt64(ms.ReasoningTokens, reasoningTokens)
			ms.providerStats = incrementModelProviderStats(ms.providerStats, d.Provider, d.Failed, detailTotals{
				totalTokens:      totalTokens,
				inputTokens:      inputTokens,
				outputTokens:     outputTokens,
				cachedTokens:     cachedTokens,
				cacheWriteTokens: cacheWriteTokens,
				reasoningTokens:  reasoningTokens,
			})
			if d.LatencyMs > 0 {
				ms.latencySum = addNonNegativeInt64(ms.latencySum, d.LatencyMs)
				ms.latencyN = addNonNegativeInt64(ms.latencyN, 1)
			}

			source := strings.TrimSpace(d.Source)
			if source == "" {
				source = "未知来源"
			}
			ss, ok := sourceAgg[source]
			if !ok {
				ss = &SourceStat{Source: source, Provider: d.Provider}
				sourceAgg[source] = ss
			}
			ss.TotalRequests = addNonNegativeInt64(ss.TotalRequests, 1)
			if d.Failed {
				ss.FailureCount = addNonNegativeInt64(ss.FailureCount, 1)
			} else {
				ss.SuccessCount = addNonNegativeInt64(ss.SuccessCount, 1)
			}
			ss.TotalTokens = addNonNegativeInt64(ss.TotalTokens, totalTokens)
		}

		if d.Failed {
			failure := strings.TrimSpace(d.Failure)
			if failure == "" {
				failure = "未返回错误内容"
			}
			key := apiDetailErrorKey{statusCode: d.StatusCode, failure: failure}
			es, ok := errorAgg[key]
			if !ok {
				es = &APIDetailErrorStat{StatusCode: d.StatusCode, Failure: failure}
				errorAgg[key] = es
			}
			es.Count++
		}

	}

	if !aggregateScope {
		if latencyN > 0 {
			result.Summary.AvgLatencyMs = float64(latencySum) / float64(latencyN)
		}
		result.ModelStats = make([]ModelStat, 0, len(modelAgg))
		for _, ms := range modelAgg {
			result.ModelStats = append(result.ModelStats, finalizeModelStat(*ms))
		}
		sort.SliceStable(result.ModelStats, func(i, j int) bool {
			return result.ModelStats[i].TotalRequests > result.ModelStats[j].TotalRequests
		})

		result.SourceStats = make([]SourceStat, 0, len(sourceAgg))
		for _, ss := range sourceAgg {
			result.SourceStats = append(result.SourceStats, *ss)
		}
		sort.SliceStable(result.SourceStats, func(i, j int) bool {
			return result.SourceStats[i].TotalRequests > result.SourceStats[j].TotalRequests
		})
	}

	result.ErrorStats = make([]APIDetailErrorStat, 0, len(errorAgg))
	for _, es := range errorAgg {
		result.ErrorStats = append(result.ErrorStats, *es)
	}
	sort.SliceStable(result.ErrorStats, func(i, j int) bool {
		return result.ErrorStats[i].Count > result.ErrorStats[j].Count
	})
	if len(result.ErrorStats) > errorLimit {
		result.ErrorStats = result.ErrorStats[:errorLimit]
	}

	sort.Slice(recentEvents, func(i, j int) bool {
		return dashboardEventBefore(recentEvents[i], recentEvents[j])
	})
	result.RecentEvents = make([]RequestDetail, len(recentEvents))
	for i, dm := range recentEvents {
		result.RecentEvents[i] = cloneRequestDetail(dm.requestDetail())
	}
	result.GeneratedAt = generatedAt
	return finish(result)
}
