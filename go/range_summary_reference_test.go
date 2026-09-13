package main

import (
	"sort"
	"time"
)

// Independent pre-optimization scan: intentionally does not use block reducers
// or caches, so cross-boundary and invalidation tests compare different paths.
func (s *RequestStatistics) referenceRangeSummaryLocked(now time.Time, healthWindow time.Time, cutoff time.Time, clientAPI string) DashboardSummary {
	summary := DashboardSummary{}
	pricer := queryDetailPricer{stats: s}

	// Usage accumulators
	var totalRequests, successCount, failureCount int64
	var totalTokens, inputTokens, outputTokens, cachedTokens, cacheWriteTokens, reasoningTokens int64
	var latencySum, latencyN int64

	// Counters share the same time key. One day lookup and a fixed hour
	// array avoid six independent maps for every retained request.
	type timeTotals struct {
		requests, tokens int64
		cost             float64
	}
	days := make(map[summaryDayKey]timeTotals)
	var hours [24]timeTotals

	// Dimension aggregators
	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*sourceStatAccumulator)
	credentialAgg := make(map[string]*CredentialStat)
	clientAPIAgg := make(map[clientAPIGroupIdentity]*clientAPIStatAccumulator)
	apiAgg := make(map[string]*apiRangeAgg)

	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for accountingIndex := 0; accountingIndex < modelSt.accountingCount(); accountingIndex++ {
				detail := modelSt.accountingDetailAt(accountingIndex)
				if !cutoff.IsZero() && (detail.Timestamp.IsZero() || detail.Timestamp.Before(cutoff)) {
					continue
				}
				if !clientAPISelectorMatchesDetail(clientAPI, detail) {
					continue
				}
				totals := detailTotalsFromRequest(detail)
				dModel := detailModel(modelName, detail)

				// Global usage
				totalRequests = addNonNegativeInt64(totalRequests, 1)
				if detail.Failed {
					failureCount = addNonNegativeInt64(failureCount, 1)
				} else {
					successCount = addNonNegativeInt64(successCount, 1)
				}
				totalTokens = addNonNegativeInt64(totalTokens, totals.totalTokens)
				inputTokens = addNonNegativeInt64(inputTokens, totals.inputTokens)
				outputTokens = addNonNegativeInt64(outputTokens, totals.outputTokens)
				cachedTokens = addNonNegativeInt64(cachedTokens, totals.cachedTokens)
				cacheWriteTokens = addNonNegativeInt64(cacheWriteTokens, totals.cacheWriteTokens)
				reasoningTokens = addNonNegativeInt64(reasoningTokens, totals.reasoningTokens)
				if detail.LatencyMs > 0 {
					latencySum = addNonNegativeInt64(latencySum, detail.LatencyMs)
					latencyN = addNonNegativeInt64(latencyN, 1)
				}

				// Day/hour time series
				dayKey := newSummaryDayKey(detail.Timestamp)
				hourKey := detail.Timestamp.Hour()
				cost := pricer.cost(modelName, detail, totals)
				day := days[dayKey]
				day.requests = addNonNegativeInt64(day.requests, 1)
				day.tokens = addNonNegativeInt64(day.tokens, totals.totalTokens)
				day.cost = addNonNegativeCost(day.cost, cost)
				days[dayKey] = day
				hour := &hours[hourKey]
				hour.requests = addNonNegativeInt64(hour.requests, 1)
				hour.tokens = addNonNegativeInt64(hour.tokens, totals.totalTokens)
				hour.cost = addNonNegativeCost(hour.cost, cost)

				// Per-API aggregation
				api := getOrCreateAPIRangeAgg(apiAgg, apiName)
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
				ms, ok := modelAgg[dModel]
				if !ok {
					ms = &ModelStat{Model: dModel}
					modelAgg[dModel] = ms
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
				src, ok := sourceAgg[source]
				if !ok {
					src = &sourceStatAccumulator{
						stat:      SourceStat{Source: source, Provider: detail.Provider},
						providers: make(map[string]int64),
					}
					sourceAgg[source] = src
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
				cred, ok := credentialAgg[credKey]
				if !ok {
					cred = &CredentialStat{AuthIndex: credKey}
					credentialAgg[credKey] = cred
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
				client, ok := clientAPIAgg[clientKey]
				if !ok {
					client = &clientAPIStatAccumulator{
						stat: ClientAPIStat{
							APIKey:     clientAPIGroupLabel(detail),
							APIKeyHash: detail.APIKeyHash,
						},
						models: make(map[string]*ClientAPIModelStat),
					}
					clientAPIAgg[clientKey] = client
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
		}
	}

	// Build usage
	summary.Usage.TotalRequests = totalRequests
	summary.Usage.SuccessCount = successCount
	summary.Usage.FailureCount = failureCount
	summary.Usage.TotalTokens = totalTokens
	summary.Usage.InputTokens = inputTokens
	summary.Usage.OutputTokens = outputTokens
	summary.Usage.CachedTokens = cachedTokens
	summary.Usage.CacheWriteTokens = cacheWriteTokens
	summary.Usage.ReasoningTokens = reasoningTokens
	if latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(latencySum) / float64(latencyN)
	}

	// Build API snapshots
	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(apiAgg))
	for apiName, api := range apiAgg {
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
	summary.ModelStats = make([]ModelStat, 0, len(modelAgg))
	for _, m := range modelAgg {
		summary.ModelStats = append(summary.ModelStats, finalizeModelStat(*m))
	}
	sort.SliceStable(summary.ModelStats, func(i, j int) bool {
		return summary.ModelStats[i].TotalRequests > summary.ModelStats[j].TotalRequests
	})

	// Build source stats
	summary.SourceStats = make([]SourceStat, 0, len(sourceAgg))
	for _, sr := range sourceAgg {
		summary.SourceStats = append(summary.SourceStats, sr.stat)
	}
	sort.SliceStable(summary.SourceStats, func(i, j int) bool {
		return summary.SourceStats[i].TotalRequests > summary.SourceStats[j].TotalRequests
	})

	// Build credential stats
	summary.CredentialStats = make([]CredentialStat, 0, len(credentialAgg))
	for _, cr := range credentialAgg {
		summary.CredentialStats = append(summary.CredentialStats, *cr)
	}
	sort.SliceStable(summary.CredentialStats, func(i, j int) bool {
		return summary.CredentialStats[i].TotalRequests > summary.CredentialStats[j].TotalRequests
	})

	// Build client API stats
	summary.ClientAPIStats = clientAPIStatsFromIdentityAccumulators(clientAPIAgg)

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
	// for hours that contain requests. Calendar keys use each source timezone.
	summary.Usage.RequestsByDay = make(map[string]int64, len(days))
	summary.Usage.TokensByDay = make(map[string]int64, len(days))
	summary.Usage.CostByDay = make(map[string]float64, len(days))
	for key, totals := range days {
		day := key.String()
		summary.Usage.RequestsByDay[day] = totals.requests
		summary.Usage.TokensByDay[day] = totals.tokens
		summary.Usage.CostByDay[day] = totals.cost
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	summary.Usage.CostByHour = make(map[string]float64, 24)
	for hour, totals := range hours {
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
