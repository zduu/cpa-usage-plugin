package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Statistics Engine
// ============================================================================

type RequestStatistics struct {
	mu sync.RWMutex

	maxDetailsPerModel int
	retention          time.Duration
	dedupWindow        time.Duration
	seen               map[string]time.Time

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64

	logResponseHeaders map[string]bool

	lastImportResult *ImportResponse
	evictedTotal     int64
}

type apiStats struct {
	TotalRequests int64
	SuccessCount  int64
	FailureCount  int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

type modelStats struct {
	TotalRequests int64
	SuccessCount  int64
	FailureCount  int64
	TotalTokens   int64
	Details       []RequestDetail
}

// apiKeySalt is a per-process random salt used to produce stable grouping IDs.
var apiKeySalt string

// hourKeys pre-computes "00" through "23" so Snapshot never allocates strings.
var hourKeys = [24]string{
	"00", "01", "02", "03", "04", "05", "06", "07",
	"08", "09", "10", "11", "12", "13", "14", "15",
	"16", "17", "18", "19", "20", "21", "22", "23",
}

func init() {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		for i := range b {
			b[i] = byte(i * 17)
		}
	}
	apiKeySalt = hex.EncodeToString(b[:])
}

func hashAPIKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	h := sha256.Sum224([]byte(apiKeySalt + ":" + s))
	return hex.EncodeToString(h[:])
}

var stats = NewRequestStatistics()

func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		maxDetailsPerModel: defaultMaxDetailsPerModel,
		retention:          time.Duration(defaultRetentionDays) * 24 * time.Hour,
		dedupWindow:        time.Duration(defaultDedupWindowMinutes) * time.Minute,
		seen:               make(map[string]time.Time),
		apis:               make(map[string]*apiStats),
		requestsByDay:      make(map[string]int64),
		requestsByHour:     make(map[int]int64),
		tokensByDay:        make(map[string]int64),
		tokensByHour:       make(map[int]int64),
	}
}

func (s *RequestStatistics) Configure(cfg runtimeConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.MaxDetailsPerModel >= 0 {
		s.maxDetailsPerModel = cfg.MaxDetailsPerModel
	}
	if cfg.RetentionDays >= 0 {
		s.retention = time.Duration(cfg.RetentionDays) * 24 * time.Hour
	}
	if cfg.DedupWindowMinutes >= 0 {
		s.dedupWindow = time.Duration(cfg.DedupWindowMinutes) * time.Minute
	}
	s.logResponseHeaders = parseHeaderWhitelist(cfg.LogResponseHeaders)
	s.pruneLocked(time.Now(), true)
	s.rebuildAggregatesLocked()
	s.rebuildSeenLocked(time.Now())
}

func (s *RequestStatistics) Record(record UsageRecord) {
	if s == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	totalTokens := usageDetailTotalTokens(record.Detail)

	statsKey := usageGroupKey(record)

	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	detail := RequestDetail{
		Model:      modelName,
		Timestamp:  timestamp,
		LatencyMs:  record.Latency.Milliseconds(),
		TTFTMs:     record.TTFT.Milliseconds(),
		APIKey:     maskAPIKey(record.APIKey),
		APIKeyHash: hashAPIKey(record.APIKey),
		Source:     usageSource(record),
		Provider:   strings.TrimSpace(record.Provider),
		AuthID:     strings.TrimSpace(record.AuthID),
		AuthIndex:  strings.TrimSpace(record.AuthIndex),
		AuthType:   strings.TrimSpace(record.AuthType),
		Thinking:   usageThinking(record),
		Tokens: TokenStats{
			InputTokens:     record.Detail.InputTokens,
			OutputTokens:    record.Detail.OutputTokens,
			ReasoningTokens: record.Detail.ReasoningTokens,
			CachedTokens:    record.Detail.CachedTokens,
			CacheTokens:     maxInt64(record.Detail.CachedTokens, record.Detail.CacheReadTokens+record.Detail.CacheCreationTokens),
			TotalTokens:     totalTokens,
		},
		Failed:     record.Failed,
		StatusCode: record.Failure.StatusCode,
		Failure:    trimLong(redactSensitiveText(record.Failure.Body), 500),
		Headers:    filterHeaders(record.ResponseHeaders, s.logResponseHeaders),
	}
	dedup := dedupKey(statsKey, modelName, detail)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.pruneSeenLocked(now)
	if s.dedupWindow > 0 {
		if _, exists := s.seen[dedup]; exists {
			return
		}
		s.seen[dedup] = now
	}

	apiSt, ok := s.apis[statsKey]
	if !ok {
		apiSt = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = apiSt
	}

	s.updateAPIStats(apiSt, modelName, detail)

	if totalTokens < 0 {
		totalTokens = 0
	}
	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()
	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens

	s.pruneLocked(now, false)
	s.pruneSeenLocked(now)
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) {
	totalTokens := detailTotalTokens(detail.Tokens)
	apiSt.TotalRequests++
	if detail.Failed {
		apiSt.FailureCount++
	} else {
		apiSt.SuccessCount++
	}
	apiSt.TotalTokens += totalTokens

	modelSt, ok := apiSt.Models[model]
	if !ok {
		modelSt = &modelStats{}
		apiSt.Models[model] = modelSt
	}
	modelSt.TotalRequests++
	if detail.Failed {
		modelSt.FailureCount++
	} else {
		modelSt.SuccessCount++
	}
	modelSt.TotalTokens += totalTokens
	modelSt.Details = append(modelSt.Details, detail)
}

func (s *RequestStatistics) pruneLocked(now time.Time, sortNeeded bool) {
	if s == nil {
		return
	}
	var cutoff time.Time
	if s.retention > 0 {
		cutoff = now.Add(-s.retention)
	}
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			delete(s.apis, apiName)
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				delete(apiSt.Models, modelName)
				continue
			}
			details := modelSt.Details
			if !cutoff.IsZero() {
				kept := details[:0]
				for _, d := range details {
					if d.Timestamp.IsZero() || !d.Timestamp.Before(cutoff) {
						kept = append(kept, d)
					} else {
						s.decrementCounters(d, apiSt, modelSt)
						s.evictedTotal++
					}
				}
				details = kept
			}
			if sortNeeded {
				sort.SliceStable(details, func(i, j int) bool {
					return details[i].Timestamp.Before(details[j].Timestamp)
				})
			}
			if s.maxDetailsPerModel >= 0 && len(details) > s.maxDetailsPerModel {
				keep := s.maxDetailsPerModel
				removed := details[:len(details)-keep]
				for _, d := range removed {
					s.decrementCounters(d, apiSt, modelSt)
					s.evictedTotal++
				}
				details = append([]RequestDetail(nil), details[len(details)-keep:]...)
			}
			modelSt.Details = details
			if len(modelSt.Details) == 0 {
				delete(apiSt.Models, modelName)
			}
		}
		if len(apiSt.Models) == 0 {
			delete(s.apis, apiName)
		}
	}
}

func (s *RequestStatistics) decrementCounters(d RequestDetail, apiSt *apiStats, modelSt *modelStats) {
	totalTokens := detailTotalTokens(d.Tokens)
	s.totalRequests--
	if d.Failed {
		s.failureCount--
	} else {
		s.successCount--
	}
	s.totalTokens -= totalTokens

	apiSt.TotalRequests--
	if d.Failed {
		apiSt.FailureCount--
	} else {
		apiSt.SuccessCount--
	}
	apiSt.TotalTokens -= totalTokens

	modelSt.TotalRequests--
	if d.Failed {
		modelSt.FailureCount--
	} else {
		modelSt.SuccessCount--
	}
	modelSt.TotalTokens -= totalTokens

	dayKey := d.Timestamp.Format("2006-01-02")
	hourKey := d.Timestamp.Hour()
	s.requestsByDay[dayKey]--
	s.requestsByHour[hourKey]--
	s.tokensByDay[dayKey] -= totalTokens
	s.tokensByHour[hourKey] -= totalTokens
}

func (s *RequestStatistics) rebuildAggregatesLocked() {
	if s == nil {
		return
	}
	s.totalRequests = 0
	s.successCount = 0
	s.failureCount = 0
	s.totalTokens = 0
	s.requestsByDay = make(map[string]int64)
	s.requestsByHour = make(map[int]int64)
	s.tokensByDay = make(map[string]int64)
	s.tokensByHour = make(map[int]int64)
	for _, apiSt := range s.apis {
		apiSt.TotalRequests = 0
		apiSt.SuccessCount = 0
		apiSt.FailureCount = 0
		apiSt.TotalTokens = 0
		for _, modelSt := range apiSt.Models {
			modelSt.TotalRequests = 0
			modelSt.SuccessCount = 0
			modelSt.FailureCount = 0
			modelSt.TotalTokens = 0
			for _, detail := range modelSt.Details {
				totalTokens := detailTotalTokens(detail.Tokens)
				s.totalRequests++
				apiSt.TotalRequests++
				modelSt.TotalRequests++
				if detail.Failed {
					s.failureCount++
					apiSt.FailureCount++
					modelSt.FailureCount++
				} else {
					s.successCount++
					apiSt.SuccessCount++
					modelSt.SuccessCount++
				}
				s.totalTokens += totalTokens
				apiSt.TotalTokens += totalTokens
				modelSt.TotalTokens += totalTokens
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := detail.Timestamp.Hour()
				s.requestsByDay[dayKey]++
				s.requestsByHour[hourKey]++
				s.tokensByDay[dayKey] += totalTokens
				s.tokensByHour[hourKey] += totalTokens
			}
		}
	}
}

// Snapshot returns a full deep-copy of all statistics including details.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, apiSt := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: apiSt.TotalRequests,
			SuccessCount:  apiSt.SuccessCount,
			FailureCount:  apiSt.FailureCount,
			TotalTokens:   apiSt.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSt.Models)),
		}
		for modelName, modelSt := range apiSt.Models {
			details := make([]RequestDetail, len(modelSt.Details))
			copy(details, modelSt.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelSt.TotalRequests,
				SuccessCount:  modelSt.SuccessCount,
				FailureCount:  modelSt.FailureCount,
				TotalTokens:   modelSt.TotalTokens,
				Details:       details,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, 24)
	for hour, v := range s.requestsByHour {
		if hour >= 0 && hour < 24 {
			result.RequestsByHour[hourKeys[hour]] = v
		}
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, 24)
	for hour, v := range s.tokensByHour {
		if hour >= 0 && hour < 24 {
			result.TokensByHour[hourKeys[hour]] = v
		}
	}

	return result
}

// MergeSnapshot imports a snapshot into the current statistics.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var cutoff time.Time
	if s.retention > 0 {
		cutoff = now.Add(-s.retention)
	}

	seen := make(map[string]struct{})
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		if strings.TrimSpace(apiName) == "" {
			continue
		}

		apiSt, ok := s.apis[apiName]
		if !ok || apiSt == nil {
			apiSt = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = apiSt
		} else if apiSt.Models == nil {
			apiSt.Models = make(map[string]*modelStats)
		}

		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = normalizeModelName(modelName)

			for _, detail := range modelSnapshot.Details {
				importModelName := normalizeModelName(detail.Model)
				if importModelName == "unknown" {
					importModelName = modelName
				}
				detail.Model = importModelName
				detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = now
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.TTFTMs < 0 {
					detail.TTFTMs = 0
				}

				if !cutoff.IsZero() && !detail.Timestamp.IsZero() && detail.Timestamp.Before(cutoff) {
					result.IgnoredByRetention++
					continue
				}

				key := dedupKey(apiName, importModelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}

				s.recordImported(apiName, importModelName, apiSt, detail)
				result.Added++
			}
		}
	}

	s.pruneLocked(now, true)
	s.rebuildAggregatesLocked()
	s.rebuildSeenLocked(now)
	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, apiSt *apiStats, detail RequestDetail) {
	totalTokens := detailTotalTokens(detail.Tokens)

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(apiSt, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func usageDetailTotalTokens(detail UsageDetail) int64 {
	totalTokens := detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return nonNegativeInt64(totalTokens)
}

func detailTotalTokens(tokens TokenStats) int64 {
	totalTokens := tokens.TotalTokens
	if totalTokens == 0 {
		totalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	return nonNegativeInt64(totalTokens)
}

func normalizedCacheTokens(tokens TokenStats) int64 {
	return maxInt64(nonNegativeInt64(tokens.CachedTokens), nonNegativeInt64(tokens.CacheTokens))
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

func (s *RequestStatistics) pruneSeenLocked(now time.Time) {
	if s == nil || s.dedupWindow <= 0 {
		return
	}
	cutoff := now.Add(-s.dedupWindow)
	for key, seenAt := range s.seen {
		if seenAt.Before(cutoff) {
			delete(s.seen, key)
		}
	}
}

func (s *RequestStatistics) rebuildSeenLocked(now time.Time) {
	if s == nil {
		return
	}
	if s.dedupWindow <= 0 {
		s.seen = make(map[string]time.Time)
		return
	}
	s.seen = make(map[string]time.Time)
	cutoff := now.Add(-s.dedupWindow)
	for apiName, apiSt := range s.apis {
		for modelName, modelSt := range apiSt.Models {
			for _, detail := range modelSt.Details {
				seenAt := detail.Timestamp
				if seenAt.IsZero() {
					seenAt = now
				}
				if seenAt.Before(cutoff) {
					continue
				}
				s.seen[dedupKey(apiName, modelName, detail)] = seenAt
			}
		}
	}
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := detail.Tokens
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.CacheTokens,
		tokens.TotalTokens,
	)
}

// ============================================================================
// New P0 Methods: Lightweight Summary + Paginated Events
// ============================================================================

// SummaryWithoutDetails computes a lightweight dashboard summary without detail arrays.
func (s *RequestStatistics) SummaryWithoutDetails() DashboardSummary {
	summary := DashboardSummary{}
	if s == nil {
		return summary
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	summary.Usage.TotalRequests = s.totalRequests
	summary.Usage.SuccessCount = s.successCount
	summary.Usage.FailureCount = s.failureCount
	summary.Usage.TotalTokens = s.totalTokens

	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(s.apis))
	var globalLatencySum int64
	var globalLatencyN int64

	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*SourceStat)
	credentialAgg := make(map[string]*CredentialStat)
	type clientAPIAccumulator struct {
		stat   ClientAPIStat
		models map[string]*ClientAPIModelStat
	}
	clientAPIs := make(map[string]*clientAPIAccumulator)

	healthSlots := make([]struct{ s, f int64 }, 672)
	healthStep := 15 * time.Minute
	healthStart := time.Now().UTC().Add(-672 * healthStep)

	for apiName, apiSt := range s.apis {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests: apiSt.TotalRequests,
			SuccessCount:  apiSt.SuccessCount,
			FailureCount:  apiSt.FailureCount,
			TotalTokens:   apiSt.TotalTokens,
			Models:        make(map[string]ModelSnapshotWithoutDetails, len(apiSt.Models)),
		}
		var apiLatencySum int64
		var apiLatencyN int64

		for modelName, modelSt := range apiSt.Models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests: modelSt.TotalRequests,
				SuccessCount:  modelSt.SuccessCount,
				FailureCount:  modelSt.FailureCount,
				TotalTokens:   modelSt.TotalTokens,
			}
			var modelLatencySum int64
			var modelLatencyN int64

			m, ok := modelAgg[modelName]
			if !ok {
				m = &ModelStat{Model: modelName}
				modelAgg[modelName] = m
			}
			for _, d := range modelSt.Details {
				totalTokens := detailTotalTokens(d.Tokens)
				inputTokens := nonNegativeInt64(d.Tokens.InputTokens)
				outputTokens := nonNegativeInt64(d.Tokens.OutputTokens)
				reasoningTokens := nonNegativeInt64(d.Tokens.ReasoningTokens)
				cachedTokens := normalizedCacheTokens(d.Tokens)

				modelSnap.InputTokens += inputTokens
				modelSnap.OutputTokens += outputTokens
				modelSnap.CachedTokens += cachedTokens
				modelSnap.ReasoningTokens += reasoningTokens
				apiSnap.InputTokens += inputTokens
				apiSnap.OutputTokens += outputTokens
				apiSnap.CachedTokens += cachedTokens
				apiSnap.ReasoningTokens += reasoningTokens
				summary.Usage.InputTokens += inputTokens
				summary.Usage.OutputTokens += outputTokens
				summary.Usage.CachedTokens += cachedTokens
				summary.Usage.ReasoningTokens += reasoningTokens

				if d.LatencyMs > 0 {
					modelLatencySum += d.LatencyMs
					modelLatencyN++
				}

				// Model aggregation
				m.TotalRequests++
				if d.Failed {
					m.FailureCount++
				} else {
					m.SuccessCount++
				}
				m.TotalTokens += totalTokens
				m.InputTokens += inputTokens
				m.OutputTokens += outputTokens
				m.CachedTokens += cachedTokens
				m.ReasoningTokens += reasoningTokens
				if d.LatencyMs > 0 {
					m.latencySum += d.LatencyMs
					m.latencyN++
				}

				// Source aggregation
				src := d.Source
				if src == "" {
					src = "未知来源"
				}
				sr, ok := sourceAgg[src]
				if !ok {
					sr = &SourceStat{Source: src, Provider: d.Provider}
					sourceAgg[src] = sr
				}
				sr.TotalRequests++
				if d.Failed {
					sr.FailureCount++
				} else {
					sr.SuccessCount++
				}
				sr.TotalTokens += totalTokens

				// Credential aggregation (by CPA credential)
				credIdx := d.AuthIndex
				if credIdx == "" {
					credIdx = "(空)"
				}
				cr, ok := credentialAgg[credIdx]
				if !ok {
					cr = &CredentialStat{AuthIndex: credIdx}
					credentialAgg[credIdx] = cr
				}
				cr.TotalRequests++
				if d.Failed {
					cr.FailureCount++
				} else {
					cr.SuccessCount++
				}
				cr.TotalTokens += totalTokens

				clientKey := d.APIKeyHash
				if clientKey == "" {
					clientKey = d.APIKey
				}
				if clientKey == "" {
					clientKey = "(unknown)"
				}
				clientLabel := d.APIKey
				if clientLabel == "" {
					clientLabel = "未知 API"
				}
				clientAgg, ok := clientAPIs[clientKey]
				if !ok {
					clientAgg = &clientAPIAccumulator{
						stat: ClientAPIStat{
							APIKey:     clientLabel,
							APIKeyHash: d.APIKeyHash,
						},
						models: make(map[string]*ClientAPIModelStat),
					}
					clientAPIs[clientKey] = clientAgg
				}
				clientAgg.stat.TotalRequests++
				if d.Failed {
					clientAgg.stat.FailureCount++
				} else {
					clientAgg.stat.SuccessCount++
				}
				clientAgg.stat.TotalTokens += totalTokens
				clientAgg.stat.InputTokens += inputTokens
				clientAgg.stat.OutputTokens += outputTokens
				clientAgg.stat.CachedTokens += cachedTokens
				clientAgg.stat.ReasoningTokens += reasoningTokens

				clientModel, ok := clientAgg.models[modelName]
				if !ok {
					clientModel = &ClientAPIModelStat{Model: modelName}
					clientAgg.models[modelName] = clientModel
				}
				clientModel.TotalRequests++
				if d.Failed {
					clientModel.FailureCount++
				} else {
					clientModel.SuccessCount++
				}
				clientModel.TotalTokens += totalTokens
				clientModel.InputTokens += inputTokens
				clientModel.OutputTokens += outputTokens
				clientModel.CachedTokens += cachedTokens
				clientModel.ReasoningTokens += reasoningTokens

				// Health grid
				if !d.Timestamp.IsZero() {
					idx := int(d.Timestamp.UTC().Sub(healthStart) / healthStep)
					if idx >= 0 && idx < 672 {
						if d.Failed {
							healthSlots[idx].f++
						} else {
							healthSlots[idx].s++
						}
					}
				}
			}
			if modelLatencyN > 0 {
				modelSnap.AvgLatencyMs = float64(modelLatencySum) / float64(modelLatencyN)
			}
			apiLatencySum += modelLatencySum
			apiLatencyN += modelLatencyN
			apiSnap.Models[modelName] = modelSnap
		}
		if apiLatencyN > 0 {
			apiSnap.AvgLatencyMs = float64(apiLatencySum) / float64(apiLatencyN)
		}
		globalLatencySum += apiLatencySum
		globalLatencyN += apiLatencyN
		summary.Usage.APIs[apiName] = apiSnap
	}

	if globalLatencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(globalLatencySum) / float64(globalLatencyN)
	}

	// Finalize model average latencies from accumulated sums.
	summary.ModelStats = make([]ModelStat, 0, len(modelAgg))
	for _, m := range modelAgg {
		if m.latencyN > 0 {
			m.AvgLatencyMs = float64(m.latencySum) / float64(m.latencyN)
		}
		m.latencySum = 0
		m.latencyN = 0
		summary.ModelStats = append(summary.ModelStats, *m)
	}
	sort.SliceStable(summary.ModelStats, func(i, j int) bool {
		return summary.ModelStats[i].TotalRequests > summary.ModelStats[j].TotalRequests
	})

	// Build source stats sorted by requests
	summary.SourceStats = make([]SourceStat, 0, len(sourceAgg))
	for _, sr := range sourceAgg {
		summary.SourceStats = append(summary.SourceStats, *sr)
	}
	sort.SliceStable(summary.SourceStats, func(i, j int) bool {
		return summary.SourceStats[i].TotalRequests > summary.SourceStats[j].TotalRequests
	})

	// Build credential stats sorted by requests
	summary.CredentialStats = make([]CredentialStat, 0, len(credentialAgg))
	for _, cr := range credentialAgg {
		summary.CredentialStats = append(summary.CredentialStats, *cr)
	}
	sort.SliceStable(summary.CredentialStats, func(i, j int) bool {
		return summary.CredentialStats[i].TotalRequests > summary.CredentialStats[j].TotalRequests
	})

	summary.ClientAPIStats = make([]ClientAPIStat, 0, len(clientAPIs))
	for _, agg := range clientAPIs {
		agg.stat.Models = make([]ClientAPIModelStat, 0, len(agg.models))
		for _, model := range agg.models {
			agg.stat.Models = append(agg.stat.Models, *model)
		}
		sort.SliceStable(agg.stat.Models, func(i, j int) bool {
			return agg.stat.Models[i].TotalRequests > agg.stat.Models[j].TotalRequests
		})
		summary.ClientAPIStats = append(summary.ClientAPIStats, agg.stat)
	}
	sort.SliceStable(summary.ClientAPIStats, func(i, j int) bool {
		return summary.ClientAPIStats[i].TotalRequests > summary.ClientAPIStats[j].TotalRequests
	})

	// Build health grid
	summary.HealthGrid = make([]HealthGridSlot, 672)
	for i := 0; i < 672; i++ {
		slot := &healthSlots[i]
		t := healthStart.Add(time.Duration(i) * healthStep)
		summary.HealthGrid[i] = HealthGridSlot{
			Slot:    i,
			Total:   slot.s + slot.f,
			Success: slot.s,
			Failure: slot.f,
			Start:   t.Format(time.RFC3339),
			End:     t.Add(healthStep).Format(time.RFC3339),
		}
	}

	// Time series
	summary.Usage.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		summary.Usage.RequestsByDay[k] = v
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	for hour, v := range s.requestsByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.RequestsByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		summary.Usage.TokensByDay[k] = v
	}
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	for hour, v := range s.tokensByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.TokensByHour[hourKeys[hour]] = v
		}
	}

	// Metadata
	summary.Meta.RetentionDays = int(s.retention.Hours() / 24)
	summary.Meta.MaxDetailsPerModel = s.maxDetailsPerModel
	summary.Meta.CurrentDetailCount = s.countDetailsLocked()
	summary.Meta.EvictedTotal = s.evictedTotal
	if s.lastImportResult != nil {
		summary.Meta.LastImport = &ImportSummary{
			Added:              s.lastImportResult.Added,
			Skipped:            s.lastImportResult.Skipped,
			IgnoredByRetention: s.lastImportResult.IgnoredByRetention,
		}
	}

	summary.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	return summary
}

// QueryEvents returns paginated, filtered event details.
func (s *RequestStatistics) QueryEvents(params EventsQuery) EventsResult {
	if s == nil {
		return EventsResult{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if params.Limit <= 0 || params.Limit > 500 {
		params.Limit = 50
	}

	now := time.Now()
	var cutoff time.Time
	switch params.Range {
	case "7h":
		cutoff = now.Add(-7 * time.Hour)
	case "24h":
		cutoff = now.Add(-24 * time.Hour)
	case "7d":
		cutoff = now.Add(-7 * 24 * time.Hour)
	}

	// Collect all matching events
	type detailWithMeta struct {
		RequestDetail
		apiName string
	}
	var all []detailWithMeta
	for apiName, apiSt := range s.apis {
		if params.API != "" && apiName != params.API {
			continue
		}
		for _, modelSt := range apiSt.Models {
			for _, d := range modelSt.Details {
				if !cutoff.IsZero() && !d.Timestamp.IsZero() && d.Timestamp.Before(cutoff) {
					continue
				}
				if params.Model != "" && d.Model != params.Model {
					continue
				}
				if params.Source != "" {
					src := d.Source
					if src == "" {
						src = "未知来源"
					}
					if src != params.Source {
						continue
					}
				}
				if params.AuthIndex != "" && d.AuthIndex != params.AuthIndex {
					continue
				}
				all = append(all, detailWithMeta{RequestDetail: d, apiName: apiName})
			}
		}
	}

	// Sort by timestamp descending, then by api name for stability
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Timestamp.Equal(all[j].Timestamp) {
			return all[i].apiName < all[j].apiName
		}
		return all[i].Timestamp.After(all[j].Timestamp)
	})

	total := len(all)

	if params.Offset >= total {
		return EventsResult{
			Events:      []RequestDetail{},
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
	}

	end := params.Offset + params.Limit
	if end > total {
		end = total
	}

	events := make([]RequestDetail, end-params.Offset)
	for i, dm := range all[params.Offset:end] {
		events[i] = dm.RequestDetail
	}

	return EventsResult{
		Events:      events,
		Total:       total,
		Limit:       params.Limit,
		Offset:      params.Offset,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}
}

func (s *RequestStatistics) countDetailsLocked() int64 {
	var count int64
	for _, apiSt := range s.apis {
		for _, modelSt := range apiSt.Models {
			count += int64(len(modelSt.Details))
		}
	}
	return count
}

func (s *RequestStatistics) DetailCount() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countDetailsLocked()
}

func (s *RequestStatistics) EvictedTotal() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evictedTotal
}
