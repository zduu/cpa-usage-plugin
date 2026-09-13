package main

import (
	"time"
)

// rangeSummaryAccumulator stores exact additive counters before rates and
// client-key display groups are finalized. The same reducer accepts scanned
// boundary records and immutable complete-block aggregates.
type rangeSummaryAccumulator struct {
	totalRequests, successCount, failureCount       int64
	totalTokens, inputTokens, outputTokens          int64
	cachedTokens, cacheWriteTokens, reasoningTokens int64
	latencySum, latencyN                            int64
	days                                            map[summaryDayKey]rangeTimeTotals
	hours                                           [24]rangeTimeTotals
	modelAgg                                        map[string]*ModelStat
	sourceAgg                                       map[string]*sourceStatAccumulator
	credentialAgg                                   map[string]*CredentialStat
	clientAPIAgg                                    map[clientAPIGroupIdentity]*clientAPIStatAccumulator
	apiAgg                                          map[string]*apiRangeAgg
}

type rangeTimeTotals struct {
	requests, tokens int64
	cost             float64
}

func newRangeSummaryAccumulator() *rangeSummaryAccumulator {
	return &rangeSummaryAccumulator{
		days:          make(map[summaryDayKey]rangeTimeTotals),
		modelAgg:      make(map[string]*ModelStat),
		sourceAgg:     make(map[string]*sourceStatAccumulator),
		credentialAgg: make(map[string]*CredentialStat),
		clientAPIAgg:  make(map[clientAPIGroupIdentity]*clientAPIStatAccumulator),
		apiAgg:        make(map[string]*apiRangeAgg),
	}
}

func (a *rangeSummaryAccumulator) add(apiName, modelName string, detail RequestDetail, pricer *queryDetailPricer) {
	totals := detailTotalsFromRequest(detail)
	dModel := detailModel(modelName, detail)

	// Global usage
	a.totalRequests = addNonNegativeInt64(a.totalRequests, 1)
	if detail.Failed {
		a.failureCount = addNonNegativeInt64(a.failureCount, 1)
	} else {
		a.successCount = addNonNegativeInt64(a.successCount, 1)
	}
	a.totalTokens = addNonNegativeInt64(a.totalTokens, totals.totalTokens)
	a.inputTokens = addNonNegativeInt64(a.inputTokens, totals.inputTokens)
	a.outputTokens = addNonNegativeInt64(a.outputTokens, totals.outputTokens)
	a.cachedTokens = addNonNegativeInt64(a.cachedTokens, totals.cachedTokens)
	a.cacheWriteTokens = addNonNegativeInt64(a.cacheWriteTokens, totals.cacheWriteTokens)
	a.reasoningTokens = addNonNegativeInt64(a.reasoningTokens, totals.reasoningTokens)
	if detail.LatencyMs > 0 {
		a.latencySum = addNonNegativeInt64(a.latencySum, detail.LatencyMs)
		a.latencyN = addNonNegativeInt64(a.latencyN, 1)
	}

	// Day/hour time series
	dayKey := newSummaryDayKey(detail.Timestamp)
	hourKey := detail.Timestamp.Hour()
	cost := pricer.cost(modelName, detail, totals)
	day := a.days[dayKey]
	day.requests = addNonNegativeInt64(day.requests, 1)
	day.tokens = addNonNegativeInt64(day.tokens, totals.totalTokens)
	day.cost = addNonNegativeCost(day.cost, cost)
	a.days[dayKey] = day
	hour := &a.hours[hourKey]
	hour.requests = addNonNegativeInt64(hour.requests, 1)
	hour.tokens = addNonNegativeInt64(hour.tokens, totals.totalTokens)
	hour.cost = addNonNegativeCost(hour.cost, cost)

	// Per-API aggregation
	api := getOrCreateAPIRangeAgg(a.apiAgg, apiName)
	api.estimatedCost = addNonNegativeCost(api.estimatedCost, cost)
	api.TotalRequests = addNonNegativeInt64(api.TotalRequests, 1)
	if detail.Failed {
		api.FailureCount = addNonNegativeInt64(api.FailureCount, 1)
	} else {
		api.SuccessCount = addNonNegativeInt64(api.SuccessCount, 1)
	}
	api.TotalTokens = addNonNegativeInt64(api.TotalTokens, totals.totalTokens)
	api.InputTokens = addNonNegativeInt64(api.InputTokens, totals.inputTokens)
	api.OutputTokens = addNonNegativeInt64(api.OutputTokens, totals.outputTokens)
	api.CachedTokens = addNonNegativeInt64(api.CachedTokens, totals.cachedTokens)
	api.CacheWriteTokens = addNonNegativeInt64(api.CacheWriteTokens, totals.cacheWriteTokens)
	api.ReasoningTokens = addNonNegativeInt64(api.ReasoningTokens, totals.reasoningTokens)
	if detail.LatencyMs > 0 {
		api.latencySum = addNonNegativeInt64(api.latencySum, detail.LatencyMs)
		api.latencyN = addNonNegativeInt64(api.latencyN, 1)
	}
	rangeIncrementAPIModel(api, dModel, detail, totals)
	api.models[dModel].estimatedCost = addNonNegativeCost(api.models[dModel].estimatedCost, cost)

	// Model summary stats
	ms, ok := a.modelAgg[dModel]
	if !ok {
		ms = &ModelStat{Model: dModel}
		a.modelAgg[dModel] = ms
	}
	ms.TotalRequests = addNonNegativeInt64(ms.TotalRequests, 1)
	if detail.Failed {
		ms.FailureCount = addNonNegativeInt64(ms.FailureCount, 1)
	} else {
		ms.SuccessCount = addNonNegativeInt64(ms.SuccessCount, 1)
	}
	ms.TotalTokens = addNonNegativeInt64(ms.TotalTokens, totals.totalTokens)
	ms.EstimatedCost = addNonNegativeCost(ms.EstimatedCost, cost)
	ms.InputTokens = addNonNegativeInt64(ms.InputTokens, totals.inputTokens)
	ms.OutputTokens = addNonNegativeInt64(ms.OutputTokens, totals.outputTokens)
	ms.CachedTokens = addNonNegativeInt64(ms.CachedTokens, totals.cachedTokens)
	ms.CacheWriteTokens = addNonNegativeInt64(ms.CacheWriteTokens, totals.cacheWriteTokens)
	ms.ReasoningTokens = addNonNegativeInt64(ms.ReasoningTokens, totals.reasoningTokens)
	ms.providerStats = incrementModelProviderStats(ms.providerStats, detail.Provider, detail.Failed, totals)
	if detail.LatencyMs > 0 {
		ms.latencySum = addNonNegativeInt64(ms.latencySum, detail.LatencyMs)
		ms.latencyN = addNonNegativeInt64(ms.latencyN, 1)
	}

	// Source stats
	source := summarySourceKey(detail)
	src, ok := a.sourceAgg[source]
	if !ok {
		src = &sourceStatAccumulator{
			stat: SourceStat{Source: source, Provider: detail.Provider},
		}
		a.sourceAgg[source] = src
	}
	if src.stat.Provider == "" {
		src.stat.Provider = detail.Provider
	}
	src.stat.TotalRequests = addNonNegativeInt64(src.stat.TotalRequests, 1)
	if detail.Failed {
		src.stat.FailureCount = addNonNegativeInt64(src.stat.FailureCount, 1)
	} else {
		src.stat.SuccessCount = addNonNegativeInt64(src.stat.SuccessCount, 1)
	}
	src.stat.TotalTokens = addNonNegativeInt64(src.stat.TotalTokens, totals.totalTokens)

	// Credential stats
	credKey := summaryCredentialKey(detail)
	cred, ok := a.credentialAgg[credKey]
	if !ok {
		cred = &CredentialStat{AuthIndex: credKey}
		a.credentialAgg[credKey] = cred
	}
	cred.TotalRequests = addNonNegativeInt64(cred.TotalRequests, 1)
	if detail.Failed {
		cred.FailureCount = addNonNegativeInt64(cred.FailureCount, 1)
	} else {
		cred.SuccessCount = addNonNegativeInt64(cred.SuccessCount, 1)
	}
	cred.TotalTokens = addNonNegativeInt64(cred.TotalTokens, totals.totalTokens)

	// Client API stats
	clientKey := clientAPIIdentity(detail)
	client, ok := a.clientAPIAgg[clientKey]
	if !ok {
		client = &clientAPIStatAccumulator{
			stat: ClientAPIStat{
				APIKey:     clientAPIGroupLabel(detail),
				APIKeyHash: detail.APIKeyHash,
			},
			models: make(map[string]*ClientAPIModelStat),
		}
		a.clientAPIAgg[clientKey] = client
	}
	client.stat.TotalRequests = addNonNegativeInt64(client.stat.TotalRequests, 1)
	if detail.Failed {
		client.stat.FailureCount = addNonNegativeInt64(client.stat.FailureCount, 1)
	} else {
		client.stat.SuccessCount = addNonNegativeInt64(client.stat.SuccessCount, 1)
	}
	client.stat.TotalTokens = addNonNegativeInt64(client.stat.TotalTokens, totals.totalTokens)
	client.stat.InputTokens = addNonNegativeInt64(client.stat.InputTokens, totals.inputTokens)
	client.stat.OutputTokens = addNonNegativeInt64(client.stat.OutputTokens, totals.outputTokens)
	client.stat.CachedTokens = addNonNegativeInt64(client.stat.CachedTokens, totals.cachedTokens)
	client.stat.CacheWriteTokens = addNonNegativeInt64(client.stat.CacheWriteTokens, totals.cacheWriteTokens)
	client.stat.ReasoningTokens = addNonNegativeInt64(client.stat.ReasoningTokens, totals.reasoningTokens)
	client.stat.EstimatedCost = addNonNegativeCost(client.stat.EstimatedCost, cost)
	rangeIncrementClientModel(client, dModel, detail, totals)
	client.models[dModel].EstimatedCost = addNonNegativeCost(client.models[dModel].EstimatedCost, cost)
}

func (a *rangeSummaryAccumulator) summary(s *RequestStatistics, now, healthWindow time.Time) DashboardSummary {
	summary := DashboardSummary{}
	// Build usage
	summary.Usage.TotalRequests = a.totalRequests
	summary.Usage.SuccessCount = a.successCount
	summary.Usage.FailureCount = a.failureCount
	summary.Usage.TotalTokens = a.totalTokens
	summary.Usage.InputTokens = a.inputTokens
	summary.Usage.OutputTokens = a.outputTokens
	summary.Usage.CachedTokens = a.cachedTokens
	summary.Usage.CacheWriteTokens = a.cacheWriteTokens
	summary.Usage.ReasoningTokens = a.reasoningTokens
	if a.latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(a.latencySum) / float64(a.latencyN)
	}

	// Build API snapshots
	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(a.apiAgg))
	for apiName, api := range a.apiAgg {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests:    api.TotalRequests,
			SuccessCount:     api.SuccessCount,
			FailureCount:     api.FailureCount,
			TotalTokens:      api.TotalTokens,
			InputTokens:      api.InputTokens,
			OutputTokens:     api.OutputTokens,
			CachedTokens:     api.CachedTokens,
			CacheWriteTokens: api.CacheWriteTokens,
			ReasoningTokens:  api.ReasoningTokens,
			EstimatedCost:    api.estimatedCost,
			Models:           make(map[string]ModelSnapshotWithoutDetails, len(api.models)),
		}
		if api.latencyN > 0 {
			apiSnap.AvgLatencyMs = float64(api.latencySum) / float64(api.latencyN)
		}
		for mName, m := range api.models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests:    m.TotalRequests,
				SuccessCount:     m.SuccessCount,
				FailureCount:     m.FailureCount,
				TotalTokens:      m.TotalTokens,
				InputTokens:      m.InputTokens,
				OutputTokens:     m.OutputTokens,
				CachedTokens:     m.CachedTokens,
				CacheWriteTokens: m.CacheWriteTokens,
				ReasoningTokens:  m.ReasoningTokens,
				EstimatedCost:    m.estimatedCost,
				Providers:        finalizedModelProviderStats(m.providerStats, m.TotalRequests, m.SuccessCount, m.FailureCount, m.TotalTokens, m.InputTokens, m.OutputTokens, m.CachedTokens, m.CacheWriteTokens, m.ReasoningTokens),
			}
			if m.latencyN > 0 {
				modelSnap.AvgLatencyMs = float64(m.latencySum) / float64(m.latencyN)
			}
			apiSnap.Models[mName] = modelSnap
		}
		summary.Usage.APIs[apiName] = apiSnap
	}

	// Build model stats
	summary.ModelStats = make([]ModelStat, 0, len(a.modelAgg))
	for _, m := range a.modelAgg {
		summary.ModelStats = append(summary.ModelStats, finalizeModelStat(*m))
	}
	sortDashboardModelStats(summary.ModelStats)

	// Build source stats
	summary.SourceStats = make([]SourceStat, 0, len(a.sourceAgg))
	for _, sr := range a.sourceAgg {
		summary.SourceStats = append(summary.SourceStats, sr.stat)
	}
	sortDashboardSourceStats(summary.SourceStats)

	// Build credential stats
	summary.CredentialStats = make([]CredentialStat, 0, len(a.credentialAgg))
	for _, cr := range a.credentialAgg {
		summary.CredentialStats = append(summary.CredentialStats, *cr)
	}
	sortDashboardCredentialStats(summary.CredentialStats)

	// Build client API stats
	summary.ClientAPIStats = clientAPIStatsFromIdentityAccumulators(a.clientAPIAgg)

	// Build health grid from pre-aggregated health buckets (always 7-day window, not scoped by range).
	healthStart := healthWindow.Add(-dashboardHealthSlotCount * dashboardHealthStep)
	summary.HealthGrid = make([]HealthGridSlot, dashboardHealthSlotCount)
	for i := 0; i < dashboardHealthSlotCount; i++ {
		t := healthStart.Add(time.Duration(i) * dashboardHealthStep)
		slot := s.healthBuckets[t.Unix()]
		summary.HealthGrid[i] = HealthGridSlot{
			Slot:    i,
			Total:   slot.success + slot.failure,
			Success: slot.success,
			Failure: slot.failure,
			Start:   t.Format(time.RFC3339),
			End:     t.Add(dashboardHealthStep).Format(time.RFC3339),
		}
	}

	// Preserve sparse hour maps, including explicit zero token/cost values
	// for a.hours that contain requests. Calendar keys use each source timezone.
	summary.Usage.RequestsByDay = make(map[string]int64, len(a.days))
	summary.Usage.TokensByDay = make(map[string]int64, len(a.days))
	summary.Usage.CostByDay = make(map[string]float64, len(a.days))
	for key, totals := range a.days {
		day := key.String()
		summary.Usage.RequestsByDay[day] = totals.requests
		summary.Usage.TokensByDay[day] = totals.tokens
		summary.Usage.CostByDay[day] = totals.cost
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	summary.Usage.CostByHour = make(map[string]float64, 24)
	for hour, totals := range a.hours {
		if totals.requests == 0 {
			continue
		}
		key := hourKeys[hour]
		summary.Usage.RequestsByHour[key] = totals.requests
		summary.Usage.TokensByHour[key] = totals.tokens
		summary.Usage.CostByHour[key] = totals.cost
	}

	// Metadata (uses global counters, not range-scoped).
	summary.Meta.RetentionDays = int(s.retention.Hours() / 24)
	summary.Meta.MaxDetailsPerModel = s.maxDetailsPerModel
	summary.Meta.CurrentDetailCount = s.countDetailsLocked()
	summary.Meta.CurrentHour = now.Hour()
	summary.Meta.EvictedTotal = s.evictedTotal
	summary.Meta.SummaryVersion = s.summaryVersion
	summary.Meta.PriceVersion = s.priceVersion
	summary.Meta.Storage = s.storageStatusLocked()
	summary.Meta.Currency = s.currencyStateLocked(now)
	if !s.lastRecordedAt.IsZero() {
		summary.Meta.LastRecordedAt = s.lastRecordedAt.UTC().Format(time.RFC3339)
	}
	if s.lastImportResult != nil {
		summary.Meta.LastImport = &ImportSummary{
			Added:              s.lastImportResult.Added,
			Skipped:            s.lastImportResult.Skipped,
			IgnoredByRetention: s.lastImportResult.IgnoredByRetention,
		}
	}

	s.applySummaryEstimatedCostsLocked(&summary)
	summary.GeneratedAt = now.UTC().Format(time.RFC3339)
	return summary
}

func (a *rangeSummaryAccumulator) merge(b *rangeSummaryAccumulator) {
	a.totalRequests = addNonNegativeInt64(a.totalRequests, b.totalRequests)
	a.successCount = addNonNegativeInt64(a.successCount, b.successCount)
	a.failureCount = addNonNegativeInt64(a.failureCount, b.failureCount)
	a.totalTokens = addNonNegativeInt64(a.totalTokens, b.totalTokens)
	a.inputTokens = addNonNegativeInt64(a.inputTokens, b.inputTokens)
	a.outputTokens = addNonNegativeInt64(a.outputTokens, b.outputTokens)
	a.cachedTokens = addNonNegativeInt64(a.cachedTokens, b.cachedTokens)
	a.cacheWriteTokens = addNonNegativeInt64(a.cacheWriteTokens, b.cacheWriteTokens)
	a.reasoningTokens = addNonNegativeInt64(a.reasoningTokens, b.reasoningTokens)
	a.latencySum = addNonNegativeInt64(a.latencySum, b.latencySum)
	a.latencyN = addNonNegativeInt64(a.latencyN, b.latencyN)
	for key, value := range b.days {
		day := a.days[key]
		day.requests = addNonNegativeInt64(day.requests, value.requests)
		day.tokens = addNonNegativeInt64(day.tokens, value.tokens)
		day.cost = addNonNegativeCost(day.cost, value.cost)
		a.days[key] = day
	}
	for i, value := range b.hours {
		hour := &a.hours[i]
		hour.requests = addNonNegativeInt64(hour.requests, value.requests)
		hour.tokens = addNonNegativeInt64(hour.tokens, value.tokens)
		hour.cost = addNonNegativeCost(hour.cost, value.cost)
	}
	for key, source := range b.apiAgg {
		dest := getOrCreateAPIRangeAgg(a.apiAgg, key)
		dest.TotalRequests = addNonNegativeInt64(dest.TotalRequests, source.TotalRequests)
		dest.SuccessCount = addNonNegativeInt64(dest.SuccessCount, source.SuccessCount)
		dest.FailureCount = addNonNegativeInt64(dest.FailureCount, source.FailureCount)
		dest.TotalTokens = addNonNegativeInt64(dest.TotalTokens, source.TotalTokens)
		dest.InputTokens = addNonNegativeInt64(dest.InputTokens, source.InputTokens)
		dest.OutputTokens = addNonNegativeInt64(dest.OutputTokens, source.OutputTokens)
		dest.CachedTokens = addNonNegativeInt64(dest.CachedTokens, source.CachedTokens)
		dest.CacheWriteTokens = addNonNegativeInt64(dest.CacheWriteTokens, source.CacheWriteTokens)
		dest.ReasoningTokens = addNonNegativeInt64(dest.ReasoningTokens, source.ReasoningTokens)
		dest.latencySum = addNonNegativeInt64(dest.latencySum, source.latencySum)
		dest.latencyN = addNonNegativeInt64(dest.latencyN, source.latencyN)
		dest.estimatedCost = addNonNegativeCost(dest.estimatedCost, source.estimatedCost)
		for model, value := range source.models {
			item := dest.models[model]
			if item == nil {
				item = &modelRangeAgg{}
				dest.models[model] = item
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
			item.estimatedCost = addNonNegativeCost(item.estimatedCost, value.estimatedCost)
			item.providerStats = mergeModelProviderStats(item.providerStats, value.providerStats)
		}
	}
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
			item = &sourceStatAccumulator{stat: SourceStat{Source: value.stat.Source}}
			a.sourceAgg[key] = item
		}
		if item.stat.Provider == "" {
			item.stat.Provider = value.stat.Provider
		}
		item.stat.TotalRequests = addNonNegativeInt64(item.stat.TotalRequests, value.stat.TotalRequests)
		item.stat.SuccessCount = addNonNegativeInt64(item.stat.SuccessCount, value.stat.SuccessCount)
		item.stat.FailureCount = addNonNegativeInt64(item.stat.FailureCount, value.stat.FailureCount)
		item.stat.TotalTokens = addNonNegativeInt64(item.stat.TotalTokens, value.stat.TotalTokens)
	}
	for key, value := range b.credentialAgg {
		item := a.credentialAgg[key]
		if item == nil {
			item = &CredentialStat{AuthIndex: value.AuthIndex}
			a.credentialAgg[key] = item
		}
		item.TotalRequests = addNonNegativeInt64(item.TotalRequests, value.TotalRequests)
		item.SuccessCount = addNonNegativeInt64(item.SuccessCount, value.SuccessCount)
		item.FailureCount = addNonNegativeInt64(item.FailureCount, value.FailureCount)
		item.TotalTokens = addNonNegativeInt64(item.TotalTokens, value.TotalTokens)
	}
	for key, value := range b.clientAPIAgg {
		item := a.clientAPIAgg[key]
		if item == nil {
			item = &clientAPIStatAccumulator{
				stat:   ClientAPIStat{APIKey: value.stat.APIKey, APIKeyHash: value.stat.APIKeyHash},
				models: make(map[string]*ClientAPIModelStat),
			}
			a.clientAPIAgg[key] = item
		}
		item.stat.TotalRequests = addNonNegativeInt64(item.stat.TotalRequests, value.stat.TotalRequests)
		item.stat.SuccessCount = addNonNegativeInt64(item.stat.SuccessCount, value.stat.SuccessCount)
		item.stat.FailureCount = addNonNegativeInt64(item.stat.FailureCount, value.stat.FailureCount)
		item.stat.TotalTokens = addNonNegativeInt64(item.stat.TotalTokens, value.stat.TotalTokens)
		item.stat.InputTokens = addNonNegativeInt64(item.stat.InputTokens, value.stat.InputTokens)
		item.stat.OutputTokens = addNonNegativeInt64(item.stat.OutputTokens, value.stat.OutputTokens)
		item.stat.CachedTokens = addNonNegativeInt64(item.stat.CachedTokens, value.stat.CachedTokens)
		item.stat.CacheWriteTokens = addNonNegativeInt64(item.stat.CacheWriteTokens, value.stat.CacheWriteTokens)
		item.stat.ReasoningTokens = addNonNegativeInt64(item.stat.ReasoningTokens, value.stat.ReasoningTokens)
		item.stat.EstimatedCost = addNonNegativeCost(item.stat.EstimatedCost, value.stat.EstimatedCost)
		for model, source := range value.models {
			dest := item.models[model]
			if dest == nil {
				dest = &ClientAPIModelStat{Model: source.Model}
				item.models[model] = dest
			}
			dest.TotalRequests = addNonNegativeInt64(dest.TotalRequests, source.TotalRequests)
			dest.SuccessCount = addNonNegativeInt64(dest.SuccessCount, source.SuccessCount)
			dest.FailureCount = addNonNegativeInt64(dest.FailureCount, source.FailureCount)
			dest.TotalTokens = addNonNegativeInt64(dest.TotalTokens, source.TotalTokens)
			dest.InputTokens = addNonNegativeInt64(dest.InputTokens, source.InputTokens)
			dest.OutputTokens = addNonNegativeInt64(dest.OutputTokens, source.OutputTokens)
			dest.CachedTokens = addNonNegativeInt64(dest.CachedTokens, source.CachedTokens)
			dest.CacheWriteTokens = addNonNegativeInt64(dest.CacheWriteTokens, source.CacheWriteTokens)
			dest.ReasoningTokens = addNonNegativeInt64(dest.ReasoningTokens, source.ReasoningTokens)
			dest.EstimatedCost = addNonNegativeCost(dest.EstimatedCost, source.EstimatedCost)
			mergeRangeClientProviders(dest, source)
		}
	}
}

// Merge without sharing maps or slices owned by a cached aggregate.
func mergeRangeClientProviders(dest, source *ClientAPIModelStat) {
	if source.providerStats != nil {
		if dest.providerStats == nil && len(dest.Providers) > 0 {
			dest.providerStats = make(map[string]*ModelProviderStat, len(dest.Providers))
			for _, value := range dest.Providers {
				dest.providerStats[modelProviderStatsKey(value.Provider)] = new(value)
			}
			dest.Providers = nil
		}
		dest.providerStats = mergeModelProviderStats(dest.providerStats, source.providerStats)
	}
	for _, value := range source.Providers {
		key := modelProviderStatsKey(value.Provider)
		if dest.providerStats != nil {
			item := dest.providerStats[key]
			if item == nil {
				dest.providerStats[key] = new(value)
			} else {
				mergeRangeProvider(item, value)
			}
			continue
		}
		found := false
		for i := range dest.Providers {
			if modelProviderStatsKey(dest.Providers[i].Provider) == key {
				mergeRangeProvider(&dest.Providers[i], value)
				found = true
				break
			}
		}
		if !found {
			if len(dest.Providers) < 8 {
				dest.Providers = append(dest.Providers, value)
			} else {
				dest.providerStats = make(map[string]*ModelProviderStat, len(dest.Providers)+1)
				for _, existing := range dest.Providers {
					dest.providerStats[modelProviderStatsKey(existing.Provider)] = new(existing)
				}
				dest.Providers = nil
				dest.providerStats[key] = new(value)
			}
		}
	}
}

func mergeRangeProvider(dest *ModelProviderStat, source ModelProviderStat) {
	dest.TotalRequests = addNonNegativeInt64(dest.TotalRequests, source.TotalRequests)
	dest.SuccessCount = addNonNegativeInt64(dest.SuccessCount, source.SuccessCount)
	dest.FailureCount = addNonNegativeInt64(dest.FailureCount, source.FailureCount)
	dest.TotalTokens = addNonNegativeInt64(dest.TotalTokens, source.TotalTokens)
	dest.InputTokens = addNonNegativeInt64(dest.InputTokens, source.InputTokens)
	dest.OutputTokens = addNonNegativeInt64(dest.OutputTokens, source.OutputTokens)
	dest.CachedTokens = addNonNegativeInt64(dest.CachedTokens, source.CachedTokens)
	dest.CacheWriteTokens = addNonNegativeInt64(dest.CacheWriteTokens, source.CacheWriteTokens)
	dest.ReasoningTokens = addNonNegativeInt64(dest.ReasoningTokens, source.ReasoningTokens)
}
