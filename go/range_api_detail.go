package main

import (
	"strings"
	"time"
	"unsafe"
)

type rangeAPIDetailAccumulator struct {
	summary              APIDetailSummary
	totalEvents          int
	latencySum, latencyN int64
	modelAgg             map[string]*ModelStat
	sourceAgg            map[string]*SourceStat
	errorAgg             map[apiDetailErrorKey]*APIDetailErrorStat
}

func newRangeAPIDetailAccumulator() *rangeAPIDetailAccumulator {
	return &rangeAPIDetailAccumulator{
		modelAgg:  make(map[string]*ModelStat),
		sourceAgg: make(map[string]*SourceStat),
		errorAgg:  make(map[apiDetailErrorKey]*APIDetailErrorStat),
	}
}

func (a *rangeAPIDetailAccumulator) add(modelName string, d RequestDetail, pricer *queryDetailPricer, needsDetailPrices bool) {
	totalTokens := detailTotalTokensForRequest(d)
	inputTokens := nonNegativeInt64(d.Tokens.InputTokens)
	outputTokens := nonNegativeInt64(d.Tokens.OutputTokens)
	reasoningTokens := nonNegativeInt64(d.Tokens.ReasoningTokens)
	cachedTokens := normalizedCacheReadTokens(d.Tokens)
	cacheWriteTokens := nonNegativeInt64(d.Tokens.CacheWriteTokens)

	a.totalEvents++
	a.summary.TotalRequests = addNonNegativeInt64(a.summary.TotalRequests, 1)
	if d.Failed {
		a.summary.FailureCount = addNonNegativeInt64(a.summary.FailureCount, 1)
	} else {
		a.summary.SuccessCount = addNonNegativeInt64(a.summary.SuccessCount, 1)
	}
	a.summary.TotalTokens = addNonNegativeInt64(a.summary.TotalTokens, totalTokens)
	a.summary.InputTokens = addNonNegativeInt64(a.summary.InputTokens, inputTokens)
	a.summary.OutputTokens = addNonNegativeInt64(a.summary.OutputTokens, outputTokens)
	a.summary.CachedTokens = addNonNegativeInt64(a.summary.CachedTokens, cachedTokens)
	a.summary.CacheWriteTokens = addNonNegativeInt64(a.summary.CacheWriteTokens, cacheWriteTokens)
	a.summary.ReasoningTokens = addNonNegativeInt64(a.summary.ReasoningTokens, reasoningTokens)
	if d.LatencyMs > 0 {
		a.latencySum = addNonNegativeInt64(a.latencySum, d.LatencyMs)
		a.latencyN = addNonNegativeInt64(a.latencyN, 1)
	}

	modelLabel := normalizeModelName(modelName)
	if d.Model != "" {
		modelLabel = d.Model
	}
	ms, ok := a.modelAgg[modelLabel]
	if !ok {
		ms = &ModelStat{Model: modelLabel}
		a.modelAgg[modelLabel] = ms
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
	ss, ok := a.sourceAgg[source]
	if !ok {
		ss = &SourceStat{Source: source, Provider: d.Provider}
		a.sourceAgg[source] = ss
	}
	ss.TotalRequests = addNonNegativeInt64(ss.TotalRequests, 1)
	if d.Failed {
		ss.FailureCount = addNonNegativeInt64(ss.FailureCount, 1)
	} else {
		ss.SuccessCount = addNonNegativeInt64(ss.SuccessCount, 1)
	}
	ss.TotalTokens = addNonNegativeInt64(ss.TotalTokens, totalTokens)

	if d.Failed {
		failure := strings.TrimSpace(d.Failure)
		if failure == "" {
			failure = "未返回错误内容"
		}
		key := apiDetailErrorKey{statusCode: d.StatusCode, failure: failure}
		es, ok := a.errorAgg[key]
		if !ok {
			es = &APIDetailErrorStat{StatusCode: d.StatusCode, Failure: failure}
			a.errorAgg[key] = es
		}
		es.Count++
	}

}

func (a *rangeAPIDetailAccumulator) merge(b *rangeAPIDetailAccumulator) {
	a.totalEvents += b.totalEvents
	a.latencySum = addNonNegativeInt64(a.latencySum, b.latencySum)
	a.latencyN = addNonNegativeInt64(a.latencyN, b.latencyN)
	a.summary.TotalRequests = addNonNegativeInt64(a.summary.TotalRequests, b.summary.TotalRequests)
	a.summary.SuccessCount = addNonNegativeInt64(a.summary.SuccessCount, b.summary.SuccessCount)
	a.summary.FailureCount = addNonNegativeInt64(a.summary.FailureCount, b.summary.FailureCount)
	a.summary.TotalTokens = addNonNegativeInt64(a.summary.TotalTokens, b.summary.TotalTokens)
	a.summary.InputTokens = addNonNegativeInt64(a.summary.InputTokens, b.summary.InputTokens)
	a.summary.OutputTokens = addNonNegativeInt64(a.summary.OutputTokens, b.summary.OutputTokens)
	a.summary.CachedTokens = addNonNegativeInt64(a.summary.CachedTokens, b.summary.CachedTokens)
	a.summary.CacheWriteTokens = addNonNegativeInt64(a.summary.CacheWriteTokens, b.summary.CacheWriteTokens)
	a.summary.ReasoningTokens = addNonNegativeInt64(a.summary.ReasoningTokens, b.summary.ReasoningTokens)
	for key, value := range b.modelAgg {
		item := a.modelAgg[key]
		if item == nil {
			item = &ModelStat{Model: value.Model}
			a.modelAgg[key] = item
		}
		item.TotalRequests = addNonNegativeInt64(item.TotalRequests, value.TotalRequests)
		item.SuccessCount = addNonNegativeInt64(item.SuccessCount, value.SuccessCount)
		item.FailureCount = addNonNegativeInt64(item.FailureCount, value.FailureCount)
		item.TotalTokens = addNonNegativeInt64(item.TotalTokens, value.TotalTokens)
		item.InputTokens = addNonNegativeInt64(item.InputTokens, value.InputTokens)
		item.OutputTokens = addNonNegativeInt64(item.OutputTokens, value.OutputTokens)
		item.CachedTokens = addNonNegativeInt64(item.CachedTokens, value.CachedTokens)
		item.CacheWriteTokens = addNonNegativeInt64(item.CacheWriteTokens, value.CacheWriteTokens)
		item.ReasoningTokens = addNonNegativeInt64(item.ReasoningTokens, value.ReasoningTokens)
		item.latencySum = addNonNegativeInt64(item.latencySum, value.latencySum)
		item.latencyN = addNonNegativeInt64(item.latencyN, value.latencyN)
		item.EstimatedCost = addNonNegativeCost(item.EstimatedCost, value.EstimatedCost)
		item.providerStats = mergeModelProviderStats(item.providerStats, value.providerStats)
	}
	for key, value := range b.sourceAgg {
		item := a.sourceAgg[key]
		if item == nil {
			copy := *value
			a.sourceAgg[key] = &copy
			continue
		}
		item.TotalRequests = addNonNegativeInt64(item.TotalRequests, value.TotalRequests)
		item.SuccessCount = addNonNegativeInt64(item.SuccessCount, value.SuccessCount)
		item.FailureCount = addNonNegativeInt64(item.FailureCount, value.FailureCount)
		item.TotalTokens = addNonNegativeInt64(item.TotalTokens, value.TotalTokens)
	}
	for key, value := range b.errorAgg {
		item := a.errorAgg[key]
		if item == nil {
			copy := *value
			a.errorAgg[key] = &copy
		} else {
			item.Count += value.Count
		}
	}
}

func (a *rangeAPIDetailAccumulator) fill(result *APIDetailResponse, aggregateScope bool, errorLimit int) {
	if !aggregateScope {
		result.Summary = a.summary
		result.TotalEvents = a.totalEvents
		if a.latencyN > 0 {
			result.Summary.AvgLatencyMs = float64(a.latencySum) / float64(a.latencyN)
		}
		result.ModelStats = make([]ModelStat, 0, len(a.modelAgg))
		for _, model := range a.modelAgg {
			result.ModelStats = append(result.ModelStats, finalizeModelStat(*model))
		}
		sortDashboardModelStats(result.ModelStats)
		result.SourceStats = make([]SourceStat, 0, len(a.sourceAgg))
		for _, source := range a.sourceAgg {
			result.SourceStats = append(result.SourceStats, *source)
		}
		sortDashboardSourceStats(result.SourceStats)
	}
	result.ErrorStats = make([]APIDetailErrorStat, 0, len(a.errorAgg))
	for _, value := range a.errorAgg {
		result.ErrorStats = append(result.ErrorStats, *value)
	}
	sortAPIDetailErrorStats(result.ErrorStats)
	if len(result.ErrorStats) > errorLimit {
		result.ErrorStats = result.ErrorStats[:errorLimit]
	}
}

func (a *rangeAPIDetailAccumulator) estimatedBytes() int64 {
	n := int64(unsafe.Sizeof(*a)) + 3*256
	for key, model := range a.modelAgg {
		n += 352 + int64(unsafe.Sizeof(*model)) + int64(len(key)+len(model.Model))
		for provider, value := range model.providerStats {
			n += 96 + int64(unsafe.Sizeof(*value)) + int64(len(provider)+len(value.Provider))
		}
	}
	for key, source := range a.sourceAgg {
		n += 96 + int64(unsafe.Sizeof(*source)) + int64(len(key)+len(source.Source)+len(source.Provider))
	}
	for key, value := range a.errorAgg {
		n += 96 + int64(unsafe.Sizeof(key)+unsafe.Sizeof(*value)) + int64(len(key.failure)+len(value.Failure))
	}
	return n
}

func (s *RequestStatistics) rangeAPIDetailLocked(api string, cutoff time.Time, clientAPI string) *rangeAPIDetailAccumulator {
	s.prepareRangeAggregateCacheLocked()
	result := newRangeAPIDetailAccumulator()
	pricer := queryDetailPricer{stats: s}
	needsDetailPrices := s.hasTimeBasedPricesLocked()
	s.forEachRangeBlockLocked(api, true, func(_, model string, block *rangeAggregateBlock, from, end int, read func(int) RequestDetail) {
		scan := func(target *rangeAPIDetailAccumulator) {
			s.rangeAggregates.scannedRows += int64(end - from)
			for i := from; i < end; i++ {
				d := read(i)
				if dashboardEventPastCutoff(&d, cutoff) || !clientAPISelectorMatchesDetail(clientAPI, d) {
					continue
				}
				target.add(model, d, &pricer, needsDetailPrices)
			}
		}
		if block == nil {
			scan(result)
			return
		}
		if !cutoff.IsZero() && block.maximum.Before(cutoff) {
			return
		}
		complete := cutoff.IsZero() || (block.allNonZero && !block.minimum.Before(cutoff))
		if !complete || len(clientAPI) > 4096 {
			scan(result)
			return
		}
		key := rangeAggregateKey{block: block.identity, clientAPI: clientAPI, apiDetail: true}
		if entry, ok := s.rangeAggregates.entries[key]; ok {
			s.rangeAggregates.hits++
			result.merge(entry.apiDetail)
			return
		}
		s.rangeAggregates.misses++
		if clientAPI == "" && block.detailBytes > 0 && !s.rangeAggregates.canRetain(block.detailBytes+128) {
			scan(result)
			return
		}
		data := newRangeAPIDetailAccumulator()
		scan(data)
		entry := rangeAggregateEntry{apiDetail: data}
		estimated := data.estimatedBytes()
		if clientAPI == "" {
			block.detailBytes = estimated
		}
		s.rangeAggregates.retainEntry(key, entry, estimated)
		result.merge(data)
	})
	return result
}
