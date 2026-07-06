package main

import (
	"bufio"
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Statistics Engine
// ============================================================================

type RequestStatistics struct {
	mu sync.RWMutex

	storageControlMu sync.Mutex
	storageEnqueueWG sync.WaitGroup

	maxDetailsPerModel int
	retention          time.Duration
	dedupWindow        time.Duration
	seen               map[string]time.Time

	totalRequests   int64
	successCount    int64
	failureCount    int64
	totalTokens     int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	latencySum      int64
	latencyN        int64
	startedAt       time.Time
	lastRecordedAt  time.Time

	apis map[string]*apiStats

	requestsByDay    map[string]int64
	requestsByHour   map[int]int64
	tokensByDay      map[string]int64
	tokensByHour     map[int]int64
	costByDay        map[string]float64
	costByHour       map[int]float64
	costTokensByDay  map[string]map[string]*TimeSeriesTokenStat
	costTokensByHour map[int]map[string]*TimeSeriesTokenStat
	healthBuckets    map[int64]healthBucket

	modelSummaryStats map[string]*ModelStat
	sourceStats       map[string]*sourceStatAccumulator
	credentialStats   map[string]*CredentialStat
	clientAPIStats    map[string]*clientAPIStatAccumulator

	logResponseHeaders            headerWhitelist
	storageEnabled                bool
	storagePath                   string
	storageFlush                  time.Duration
	storageDir                    string
	storageLegacyPath             string
	storageLoadedPath             string
	storageActiveDate             string
	storageLastFlush              time.Time
	storageLastSnapshot           time.Time
	storageLastCompaction         time.Time
	storageLastSync               time.Time
	storageLastError              string
	storageBuffered               int64
	storageSnapshotInterval       time.Duration
	storageSnapshotRecordInterval int
	storageSnapshotRecords        int64
	storageSyncInterval           time.Duration
	storageSyncRecordInterval     int
	storageUnsyncedRecords        int64
	storageWriteQueueCapacity     int
	storageWriteQueueLength       int
	storageLastWriteBatchRecords  int
	storageLastWriteBatchDuration time.Duration
	storageLastWriteQueueWait     time.Duration
	storageWriteBatchesTotal      int64
	storageWriteRecordsTotal      int64
	storageWriteBatchDurationAvg  time.Duration
	storageWriteBatchDurations    []time.Duration
	storageWriteQueueWaitAvg      time.Duration
	storageWriteQueueWaits        []time.Duration
	storageWriteQueueWaitMax      time.Duration
	storageLastCompactedShards    int
	storageCompactedShardsTotal   int64
	storageWorkerRunning          bool
	storageQueue                  chan persistedDetail
	storageStop                   chan struct{}
	storageDone                   chan struct{}
	storageStopping               bool

	exportMaxRecords int

	priceStoragePath       string
	priceStorageLoadedPath string
	priceStorageLastError  string
	modelPrices            map[string]ModelPrice
	modelPricesUpdatedAt   time.Time
	modelsDevPricesEnabled bool
	modelsDevPricesURL     string
	modelsDevRefresh       time.Duration
	modelsDevPrices        map[string]ModelPrice
	modelsDevUpdatedAt     time.Time
	modelsDevLastAttempt   time.Time
	modelsDevLastSuccess   time.Time
	modelsDevLastError     string
	modelsDevETag          string
	modelsDevStop          chan struct{}
	modelsDevDone          chan struct{}

	lastImportResult *ImportResponse
	evictedTotal     int64

	summaryVersion      uint64
	summaryCacheValid   bool
	summaryCache        DashboardSummary
	summaryCacheVersion uint64
	summaryCacheWindow  time.Time

	summaryRangeCache       map[string]DashboardSummary
	summaryRangeCacheWindow map[string]time.Time

	eventQueryCache      map[dashboardEventCacheKey]EventsResult
	eventQueryCacheOrder []dashboardEventCacheKey
	eventIndexVersion    uint64
	eventIndex           []dashboardEventDetail
	eventAPIIndex        map[string][]dashboardEventDetail
	eventModelIndex      map[string][]dashboardEventDetail
	eventSourceIndex     map[string][]dashboardEventDetail
	eventAuthIndex       map[string][]dashboardEventDetail

	summaryCacheHits           int64
	summaryCacheMisses         int64
	lastSummaryDuration        time.Duration
	eventCacheHits             int64
	eventCacheMisses           int64
	lastEventsQueryDuration    time.Duration
	lastEventsQueryTotal       int
	apiDetailQueries           int64
	lastAPIDetailDuration      time.Duration
	lastAPIDetailTotal         int
	eventsExportRequests       int64
	eventsExportGzipRequests   int64
	eventsExportTruncatedTotal int64
	lastEventsExportDuration   time.Duration
	lastEventsExportFormat     string
	lastEventsExportGzip       bool
	lastEventsExportTotal      int
	lastEventsExported         int
	lastEventsExportTruncated  bool
	lastEventsExportRawBytes   int
	lastEventsExportBodyBytes  int
	conditionalRequests        map[string]conditionalRequestCounter
}

type apiStats struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	Models          map[string]*modelStats
	Sources         map[string]*sourceStatAccumulator
}

type modelStats struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	Details         []RequestDetail
	providerStats   map[string]*ModelProviderStat
}

type detailTotals struct {
	totalTokens     int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	latencySum      int64
	latencyN        int64
}

func timeSeriesTokenKey(model, provider string) string {
	return normalizeModelPriceKey(provider) + "\x00" + normalizeModelPriceKey(model)
}

func incrementTimeSeriesTokenStats(stats map[string]*TimeSeriesTokenStat, model, provider string, totals detailTotals) map[string]*TimeSeriesTokenStat {
	if stats == nil {
		stats = make(map[string]*TimeSeriesTokenStat)
	}
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	key := timeSeriesTokenKey(model, provider)
	stat, ok := stats[key]
	if !ok {
		stat = &TimeSeriesTokenStat{Model: model, Provider: provider}
		stats[key] = stat
	}
	stat.TotalTokens += totals.totalTokens
	stat.InputTokens += totals.inputTokens
	stat.OutputTokens += totals.outputTokens
	stat.CachedTokens += totals.cachedTokens
	stat.ReasoningTokens += totals.reasoningTokens
	return stats
}

func decrementTimeSeriesTokenStats(stats map[string]*TimeSeriesTokenStat, model, provider string, totals detailTotals) bool {
	if stats == nil {
		return false
	}
	key := timeSeriesTokenKey(model, provider)
	stat, ok := stats[key]
	if !ok || stat == nil {
		return false
	}
	stat.TotalTokens -= totals.totalTokens
	stat.InputTokens -= totals.inputTokens
	stat.OutputTokens -= totals.outputTokens
	stat.CachedTokens -= totals.cachedTokens
	stat.ReasoningTokens -= totals.reasoningTokens
	if stat.TotalTokens <= 0 && stat.InputTokens <= 0 && stat.OutputTokens <= 0 && stat.CachedTokens <= 0 && stat.ReasoningTokens <= 0 {
		delete(stats, key)
	}
	return true
}

func modelProviderStatsKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func incrementModelProviderStats(stats map[string]*ModelProviderStat, provider string, failed bool, totals detailTotals) map[string]*ModelProviderStat {
	if stats == nil {
		stats = make(map[string]*ModelProviderStat)
	}
	key := modelProviderStatsKey(provider)
	stat, ok := stats[key]
	if !ok {
		stat = &ModelProviderStat{Provider: strings.TrimSpace(provider)}
		stats[key] = stat
	}
	stat.TotalRequests++
	if failed {
		stat.FailureCount++
	} else {
		stat.SuccessCount++
	}
	stat.TotalTokens += totals.totalTokens
	stat.InputTokens += totals.inputTokens
	stat.OutputTokens += totals.outputTokens
	stat.CachedTokens += totals.cachedTokens
	stat.ReasoningTokens += totals.reasoningTokens
	return stats
}

func decrementModelProviderStats(stats map[string]*ModelProviderStat, provider string, failed bool, totals detailTotals) {
	if stats == nil {
		return
	}
	key := modelProviderStatsKey(provider)
	stat, ok := stats[key]
	if !ok {
		return
	}
	stat.TotalRequests--
	if failed {
		stat.FailureCount--
	} else {
		stat.SuccessCount--
	}
	stat.TotalTokens -= totals.totalTokens
	stat.InputTokens -= totals.inputTokens
	stat.OutputTokens -= totals.outputTokens
	stat.CachedTokens -= totals.cachedTokens
	stat.ReasoningTokens -= totals.reasoningTokens
	if stat.TotalRequests <= 0 {
		delete(stats, key)
	}
}

func finalizedModelProviderStats(stats map[string]*ModelProviderStat, totalRequests, successCount, failureCount, totalTokens, inputTokens, outputTokens, cachedTokens, reasoningTokens int64) []ModelProviderStat {
	providers := make([]ModelProviderStat, 0, len(stats)+1)
	var providerRequests, providerSuccess, providerFailure, providerTotal, providerInput, providerOutput, providerCached, providerReasoning int64
	for _, stat := range stats {
		if stat != nil && stat.TotalRequests > 0 {
			providers = append(providers, *stat)
			providerRequests += stat.TotalRequests
			providerSuccess += stat.SuccessCount
			providerFailure += stat.FailureCount
			providerTotal += stat.TotalTokens
			providerInput += stat.InputTokens
			providerOutput += stat.OutputTokens
			providerCached += stat.CachedTokens
			providerReasoning += stat.ReasoningTokens
		}
	}
	remainder := ModelProviderStat{
		TotalRequests:   maxInt64(totalRequests-providerRequests, 0),
		SuccessCount:    maxInt64(successCount-providerSuccess, 0),
		FailureCount:    maxInt64(failureCount-providerFailure, 0),
		TotalTokens:     maxInt64(totalTokens-providerTotal, 0),
		InputTokens:     maxInt64(inputTokens-providerInput, 0),
		OutputTokens:    maxInt64(outputTokens-providerOutput, 0),
		CachedTokens:    maxInt64(cachedTokens-providerCached, 0),
		ReasoningTokens: maxInt64(reasoningTokens-providerReasoning, 0),
	}
	if remainder.TotalRequests > 0 || remainder.TotalTokens > 0 || remainder.InputTokens > 0 || remainder.OutputTokens > 0 || remainder.CachedTokens > 0 || remainder.ReasoningTokens > 0 {
		providers = append(providers, remainder)
	}
	sort.SliceStable(providers, func(i, j int) bool {
		if providers[i].TotalRequests != providers[j].TotalRequests {
			return providers[i].TotalRequests > providers[j].TotalRequests
		}
		return providers[i].Provider < providers[j].Provider
	})
	return providers
}

func finalizeModelStat(stat ModelStat) ModelStat {
	if stat.latencyN > 0 {
		stat.AvgLatencyMs = float64(stat.latencySum) / float64(stat.latencyN)
	}
	stat.Providers = finalizedModelProviderStats(stat.providerStats, stat.TotalRequests, stat.SuccessCount, stat.FailureCount, stat.TotalTokens, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.ReasoningTokens)
	stat.latencySum = 0
	stat.latencyN = 0
	stat.providerStats = nil
	return stat
}

func finalizeClientAPIModelStat(stat ClientAPIModelStat) ClientAPIModelStat {
	stat.Providers = finalizedModelProviderStats(stat.providerStats, stat.TotalRequests, stat.SuccessCount, stat.FailureCount, stat.TotalTokens, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.ReasoningTokens)
	stat.providerStats = nil
	return stat
}

type healthBucket struct {
	success int64
	failure int64
}

type sourceStatAccumulator struct {
	stat      SourceStat
	providers map[string]int64
}

type clientAPIStatAccumulator struct {
	stat   ClientAPIStat
	models map[string]*ClientAPIModelStat
}

type dashboardEventCacheKey struct {
	limit      int
	offset     int
	timeBucket int64
	rangeKey   string
	model      string
	source     string
	authIndex  string
	api        string
}

// apiKeySalt is a per-process random salt used to produce stable grouping IDs.
var apiKeySalt string

// hourKeys pre-computes "00" through "23" so Snapshot never allocates strings.
var hourKeys = [24]string{
	"00", "01", "02", "03", "04", "05", "06", "07",
	"08", "09", "10", "11", "12", "13", "14", "15",
	"16", "17", "18", "19", "20", "21", "22", "23",
}

const (
	dashboardHealthSlotCount       = 672
	dashboardHealthStep            = 15 * time.Minute
	dashboardEventCacheMax         = 16
	dashboardSummaryRangeCacheMax  = 16
	dashboardSummaryRangeCacheStep = time.Minute
	storageWriteSampleMax          = 256
)

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
		maxDetailsPerModel:            defaultMaxDetailsPerModel,
		retention:                     time.Duration(defaultRetentionDays) * 24 * time.Hour,
		dedupWindow:                   time.Duration(defaultDedupWindowMinutes) * time.Minute,
		seen:                          make(map[string]time.Time),
		apis:                          make(map[string]*apiStats),
		requestsByDay:                 make(map[string]int64),
		requestsByHour:                make(map[int]int64),
		tokensByDay:                   make(map[string]int64),
		tokensByHour:                  make(map[int]int64),
		costByDay:                     make(map[string]float64),
		costByHour:                    make(map[int]float64),
		costTokensByDay:               make(map[string]map[string]*TimeSeriesTokenStat),
		costTokensByHour:              make(map[int]map[string]*TimeSeriesTokenStat),
		healthBuckets:                 make(map[int64]healthBucket),
		modelSummaryStats:             make(map[string]*ModelStat),
		sourceStats:                   make(map[string]*sourceStatAccumulator),
		credentialStats:               make(map[string]*CredentialStat),
		clientAPIStats:                make(map[string]*clientAPIStatAccumulator),
		storagePath:                   defaultRuntimeConfig().StoragePath,
		storageFlush:                  time.Duration(defaultStorageFlushSeconds) * time.Second,
		storageSnapshotInterval:       time.Duration(defaultStorageSnapshotSeconds) * time.Second,
		storageSnapshotRecordInterval: defaultStorageSnapshotRecords,
		storageSyncInterval:           time.Duration(defaultStorageSyncSeconds) * time.Second,
		storageSyncRecordInterval:     defaultStorageSyncRecords,
		storageWriteQueueCapacity:     defaultStorageWriteQueueSize,
		exportMaxRecords:              defaultExportMaxRecords,
		priceStoragePath:              defaultRuntimeConfig().PriceStoragePath,
		modelPrices:                   make(map[string]ModelPrice),
		modelsDevPricesURL:            defaultRuntimeConfig().ModelsDevPricesURL,
		modelsDevRefresh:              time.Duration(defaultRuntimeConfig().ModelsDevRefreshSeconds) * time.Second,
		modelsDevPrices:               make(map[string]ModelPrice),
		conditionalRequests:           make(map[string]conditionalRequestCounter),
		startedAt:                     time.Now(),
	}
}

func (s *RequestStatistics) Configure(cfg runtimeConfig) {
	s.ConfigurePatch(runtimeConfigPatch{
		MaxDetailsPerModel:            positiveIntPtr(cfg.MaxDetailsPerModel),
		RetentionDays:                 intPtr(cfg.RetentionDays),
		DedupWindowMinutes:            intPtr(cfg.DedupWindowMinutes),
		LogResponseHeaders:            stringPtr(cfg.LogResponseHeaders),
		APIKeyHashSalt:                stringPtr(cfg.APIKeyHashSalt),
		StorageEnabled:                boolPtr(cfg.StorageEnabled),
		StoragePath:                   stringPtr(cfg.StoragePath),
		StorageFlushSeconds:           positiveIntPtr(cfg.StorageFlushSeconds),
		StorageSnapshotSeconds:        positiveIntPtr(cfg.StorageSnapshotSeconds),
		StorageSnapshotRecordInterval: positiveIntPtr(cfg.StorageSnapshotRecordInterval),
		StorageSyncSeconds:            intPtr(cfg.StorageSyncSeconds),
		StorageSyncRecordInterval:     intPtr(cfg.StorageSyncRecordInterval),
		ExportMaxRecords:              intPtr(cfg.ExportMaxRecords),
		PriceStoragePath:              stringPtr(cfg.PriceStoragePath),
		ModelsDevPricesEnabled:        boolPtr(cfg.ModelsDevPricesEnabled),
		ModelsDevPricesURL:            stringPtr(cfg.ModelsDevPricesURL),
		ModelsDevRefreshSeconds:       positiveIntPtr(cfg.ModelsDevRefreshSeconds),
		UpdateEnabled:                 boolPtr(cfg.UpdateEnabled),
		UpdateVersion:                 stringPtr(cfg.UpdateVersion),
	})
}

func (s *RequestStatistics) ConfigurePatch(cfg runtimeConfigPatch) {
	if s == nil {
		return
	}
	storageConfigTouched := cfg.StorageEnabled != nil ||
		cfg.StoragePath != nil ||
		cfg.StorageFlushSeconds != nil ||
		cfg.StorageSnapshotSeconds != nil ||
		cfg.StorageSnapshotRecordInterval != nil ||
		cfg.StorageSyncSeconds != nil ||
		cfg.StorageSyncRecordInterval != nil
	if storageConfigTouched {
		s.stopStorageWorker()
	}
	modelsDevConfigTouched := cfg.ModelsDevPricesEnabled != nil ||
		cfg.ModelsDevPricesURL != nil ||
		cfg.ModelsDevRefreshSeconds != nil
	if modelsDevConfigTouched {
		s.stopModelsDevPriceWorker()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.MaxDetailsPerModel != nil && *cfg.MaxDetailsPerModel >= 0 {
		s.maxDetailsPerModel = *cfg.MaxDetailsPerModel
	}
	if cfg.RetentionDays != nil && *cfg.RetentionDays >= 0 {
		s.retention = time.Duration(*cfg.RetentionDays) * 24 * time.Hour
	}
	if cfg.DedupWindowMinutes != nil && *cfg.DedupWindowMinutes >= 0 {
		s.dedupWindow = time.Duration(*cfg.DedupWindowMinutes) * time.Minute
	}
	if cfg.LogResponseHeaders != nil {
		s.logResponseHeaders = parseHeaderWhitelist(*cfg.LogResponseHeaders)
	}
	if cfg.APIKeyHashSalt != nil && strings.TrimSpace(*cfg.APIKeyHashSalt) != "" {
		apiKeySalt = strings.TrimSpace(*cfg.APIKeyHashSalt)
	}
	if cfg.StoragePath != nil && strings.TrimSpace(*cfg.StoragePath) != "" {
		s.storagePath = strings.TrimSpace(*cfg.StoragePath)
	}
	if cfg.StorageFlushSeconds != nil && *cfg.StorageFlushSeconds > 0 {
		s.storageFlush = time.Duration(*cfg.StorageFlushSeconds) * time.Second
	}
	if cfg.StorageSnapshotSeconds != nil && *cfg.StorageSnapshotSeconds >= 0 {
		s.storageSnapshotInterval = time.Duration(*cfg.StorageSnapshotSeconds) * time.Second
	}
	if cfg.StorageSnapshotRecordInterval != nil && *cfg.StorageSnapshotRecordInterval >= 0 {
		s.storageSnapshotRecordInterval = *cfg.StorageSnapshotRecordInterval
	}
	if cfg.StorageSyncSeconds != nil && *cfg.StorageSyncSeconds >= 0 {
		s.storageSyncInterval = time.Duration(*cfg.StorageSyncSeconds) * time.Second
	}
	if cfg.StorageSyncRecordInterval != nil && *cfg.StorageSyncRecordInterval >= 0 {
		s.storageSyncRecordInterval = *cfg.StorageSyncRecordInterval
	}
	if cfg.ExportMaxRecords != nil && *cfg.ExportMaxRecords >= 0 {
		s.exportMaxRecords = *cfg.ExportMaxRecords
	}
	if cfg.PriceStoragePath != nil && strings.TrimSpace(*cfg.PriceStoragePath) != "" {
		s.priceStoragePath = strings.TrimSpace(*cfg.PriceStoragePath)
	}
	oldModelsDevEnabled := s.modelsDevPricesEnabled
	oldModelsDevURL := s.modelsDevPricesURL
	if cfg.ModelsDevPricesURL != nil && strings.TrimSpace(*cfg.ModelsDevPricesURL) != "" {
		s.modelsDevPricesURL = strings.TrimSpace(*cfg.ModelsDevPricesURL)
	}
	if cfg.ModelsDevRefreshSeconds != nil && *cfg.ModelsDevRefreshSeconds > 0 {
		s.modelsDevRefresh = time.Duration(*cfg.ModelsDevRefreshSeconds) * time.Second
	}
	if cfg.ModelsDevPricesEnabled != nil {
		s.modelsDevPricesEnabled = *cfg.ModelsDevPricesEnabled
	}
	if oldModelsDevEnabled != s.modelsDevPricesEnabled || oldModelsDevURL != s.modelsDevPricesURL {
		s.modelsDevPrices = make(map[string]ModelPrice)
		s.modelsDevUpdatedAt = time.Time{}
		s.modelsDevLastAttempt = time.Time{}
		s.modelsDevLastSuccess = time.Time{}
		s.modelsDevLastError = ""
		s.modelsDevETag = ""
	}
	if cfg.StorageEnabled != nil {
		s.storageEnabled = *cfg.StorageEnabled
	}
	s.configureModelsDevPriceWorkerLocked()
	s.configureStorageLocked()
	s.loadModelPricesLocked()
	s.rebuildCostSeriesLocked()
	s.pruneLocked(time.Now(), true)
	s.rebuildSeenLocked(time.Now())
	s.invalidateSummaryLocked()
}

func intPtr(value int) *int {
	return &value
}

func positiveIntPtr(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func (s *RequestStatistics) invalidateSummaryLocked() {
	if s == nil {
		return
	}
	s.summaryVersion++
	s.summaryCacheValid = false
	s.eventQueryCache = nil
	s.eventQueryCacheOrder = nil
	s.eventIndexVersion = 0
	s.eventIndex = nil
	s.eventAPIIndex = nil
	s.eventModelIndex = nil
	s.eventSourceIndex = nil
	s.eventAuthIndex = nil
	s.summaryRangeCache = nil
	s.summaryRangeCacheWindow = nil
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
		BaseURL:    strings.TrimSpace(record.BaseURL),
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
	var persistDetail *persistedDetail
	s.mu.Lock()

	now := time.Now()
	if s.recordDetailLocked(statsKey, modelName, detail, "", now, false) {
		if s.storageEnabled {
			persistDetail = &persistedDetail{API: statsKey, Model: modelName, Detail: detail}
		}
		s.pruneLocked(now, false)
		s.pruneSeenLocked(now)
	}
	s.mu.Unlock()
	if persistDetail != nil {
		s.enqueueStorageDetail(*persistDetail)
	}
}

type persistedDetail struct {
	API        string        `json:"api"`
	Model      string        `json:"model"`
	Detail     RequestDetail `json:"detail"`
	enqueuedAt time.Time     `json:"-"`
}

type persistedStorageSnapshot struct {
	Version     int                `json:"version"`
	GeneratedAt string             `json:"generated_at"`
	Usage       StatisticsSnapshot `json:"usage"`
}

type storageWorkerConfig struct {
	dir                    string
	flushInterval          time.Duration
	snapshotInterval       time.Duration
	snapshotRecordInterval int
	syncInterval           time.Duration
	syncRecordInterval     int
	lastSnapshot           time.Time
}

type conditionalRequestCounter struct {
	Requests    int64
	NotModified int64
}

type storageWorkerState struct {
	cfg             storageWorkerConfig
	file            *os.File
	writer          *bufio.Writer
	loadedPath      string
	activeDate      string
	lastFlush       time.Time
	lastSnapshot    time.Time
	lastSync        time.Time
	buffered        int64
	snapshotRecords int64
	unsyncedRecords int64
}

func (s *RequestStatistics) startStorageWorkerLocked() {
	if s == nil || !s.storageEnabled || strings.TrimSpace(s.storageDir) == "" {
		return
	}
	capacity := s.storageWriteQueueCapacity
	if capacity <= 0 {
		capacity = defaultStorageWriteQueueSize
		s.storageWriteQueueCapacity = capacity
	}
	queue := make(chan persistedDetail, capacity)
	stop := make(chan struct{})
	done := make(chan struct{})
	s.storageWriteQueueLength = 0
	s.storageWorkerRunning = true
	s.storageControlMu.Lock()
	s.storageQueue = queue
	s.storageStop = stop
	s.storageDone = done
	s.storageStopping = false
	s.storageControlMu.Unlock()

	cfg := storageWorkerConfig{
		dir:                    s.storageDir,
		flushInterval:          s.storageFlush,
		snapshotInterval:       s.storageSnapshotInterval,
		snapshotRecordInterval: s.storageSnapshotRecordInterval,
		syncInterval:           s.storageSyncInterval,
		syncRecordInterval:     s.storageSyncRecordInterval,
		lastSnapshot:           s.storageLastSnapshot,
	}
	go s.storageWorkerLoop(cfg, queue, stop, done)
}

func (s *RequestStatistics) stopStorageWorker() {
	if s == nil {
		return
	}
	s.storageControlMu.Lock()
	done := s.storageDone
	if done == nil {
		s.storageControlMu.Unlock()
		return
	}
	if !s.storageStopping {
		s.storageStopping = true
		if s.storageStop != nil {
			close(s.storageStop)
		}
	}
	s.storageControlMu.Unlock()

	s.storageEnqueueWG.Wait()
	<-done

	s.storageControlMu.Lock()
	if s.storageDone == done {
		s.storageQueue = nil
		s.storageStop = nil
		s.storageDone = nil
		s.storageStopping = false
	}
	s.storageControlMu.Unlock()
}

func (s *RequestStatistics) enqueueStorageDetail(detail persistedDetail) {
	if s == nil {
		return
	}
	s.storageControlMu.Lock()
	if s.storageQueue == nil || s.storageStopping {
		s.storageControlMu.Unlock()
		return
	}
	s.storageEnqueueWG.Add(1)
	queue := s.storageQueue
	stop := s.storageStop
	s.storageControlMu.Unlock()
	defer s.storageEnqueueWG.Done()

	detail.enqueuedAt = time.Now()
	select {
	case queue <- detail:
		s.updateStorageQueueLength(len(queue))
	case <-stop:
	}
}

func (s *RequestStatistics) updateStorageQueueLength(length int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.storageWriteQueueLength = length
	s.mu.Unlock()
}

func (s *RequestStatistics) storageWorkerLoop(cfg storageWorkerConfig, queue <-chan persistedDetail, stop <-chan struct{}, done chan<- struct{}) {
	state := &storageWorkerState{cfg: cfg, lastSnapshot: cfg.lastSnapshot}
	defer close(done)
	defer func() {
		state.close(s)
		s.mu.Lock()
		s.storageWorkerRunning = false
		s.storageWriteQueueLength = 0
		s.storageBuffered = 0
		s.storageUnsyncedRecords = 0
		s.storageLoadedPath = ""
		s.storageActiveDate = ""
		s.mu.Unlock()
	}()

	for {
		select {
		case detail := <-queue:
			batch := collectStorageBatch(queue, detail)
			s.updateStorageQueueLength(len(queue))
			state.appendBatch(s, batch)
		case <-stop:
			for {
				select {
				case detail := <-queue:
					batch := collectStorageBatch(queue, detail)
					s.updateStorageQueueLength(len(queue))
					state.appendBatch(s, batch)
				default:
					return
				}
			}
		}
	}
}

func collectStorageBatch(queue <-chan persistedDetail, first persistedDetail) []persistedDetail {
	capacity := len(queue) + 1
	if capacity > defaultStorageWriteBatchSize {
		capacity = defaultStorageWriteBatchSize
	}
	if capacity <= 0 {
		capacity = 1
	}
	batch := make([]persistedDetail, 0, capacity)
	batch = append(batch, first)
	for len(batch) < defaultStorageWriteBatchSize {
		select {
		case detail := <-queue:
			batch = append(batch, detail)
		default:
			return batch
		}
	}
	return batch
}

func (w *storageWorkerState) appendBatch(s *RequestStatistics, batch []persistedDetail) {
	if len(batch) == 0 {
		return
	}
	started := time.Now()
	if err := w.open(s, started); err != nil {
		s.setStorageLastError(err)
		return
	}
	records := 0
	for _, detail := range batch {
		raw, err := json.Marshal(detail)
		if err != nil {
			s.setStorageLastError(err)
			continue
		}
		if _, err := w.writer.Write(raw); err != nil {
			s.setStorageLastError(err)
			continue
		}
		if err := w.writer.WriteByte('\n'); err != nil {
			s.setStorageLastError(err)
			continue
		}
		records++
	}
	if records == 0 {
		return
	}
	w.buffered += int64(records)
	w.snapshotRecords += int64(records)
	w.unsyncedRecords += int64(records)
	finished := time.Now()
	w.report(s)
	s.updateStorageWriteBatchMetrics(records, finished.Sub(started), storageBatchQueueWait(started, batch))
	if w.flushDue(finished) {
		w.flush(s, finished)
	}
	if w.syncDue(finished) {
		w.sync(s, finished)
	}
	if w.snapshotDue(finished) {
		w.writeSnapshot(s, finished)
	}
}

func storageBatchQueueWait(started time.Time, batch []persistedDetail) time.Duration {
	var maxWait time.Duration
	for _, detail := range batch {
		if detail.enqueuedAt.IsZero() {
			continue
		}
		wait := started.Sub(detail.enqueuedAt)
		if wait > maxWait {
			maxWait = wait
		}
	}
	return maxWait
}

func (s *RequestStatistics) updateStorageWriteBatchMetrics(records int, duration time.Duration, queueWait time.Duration) {
	if s == nil || records <= 0 {
		return
	}
	s.mu.Lock()
	s.storageLastWriteBatchRecords = records
	s.storageLastWriteBatchDuration = duration
	s.storageLastWriteQueueWait = queueWait
	s.storageWriteBatchesTotal++
	s.storageWriteRecordsTotal += int64(records)
	s.storageWriteBatchDurationAvg = storageDurationEWMA(s.storageWriteBatchDurationAvg, duration)
	s.storageWriteQueueWaitAvg = storageDurationEWMA(s.storageWriteQueueWaitAvg, queueWait)
	s.storageWriteBatchDurations = appendStorageDurationSample(s.storageWriteBatchDurations, duration)
	s.storageWriteQueueWaits = appendStorageDurationSample(s.storageWriteQueueWaits, queueWait)
	if queueWait > s.storageWriteQueueWaitMax {
		s.storageWriteQueueWaitMax = queueWait
	}
	s.mu.Unlock()
}

func appendStorageDurationSample(samples []time.Duration, sample time.Duration) []time.Duration {
	if sample < 0 {
		sample = 0
	}
	if len(samples) < storageWriteSampleMax {
		return append(samples, sample)
	}
	copy(samples, samples[1:])
	samples[len(samples)-1] = sample
	return samples
}

func (s *RequestStatistics) updateStorageCompaction(compacted int, when time.Time) {
	if s == nil || compacted <= 0 {
		return
	}
	if when.IsZero() {
		when = time.Now()
	}
	s.mu.Lock()
	s.storageLastCompaction = when
	s.storageLastCompactedShards = compacted
	s.storageCompactedShardsTotal += int64(compacted)
	s.mu.Unlock()
}

func storageDurationEWMA(previous time.Duration, current time.Duration) time.Duration {
	if current < 0 {
		current = 0
	}
	if previous <= 0 {
		return current
	}
	const alpha = 0.2
	return time.Duration(float64(previous)*(1-alpha) + float64(current)*alpha)
}

func (w *storageWorkerState) open(s *RequestStatistics, now time.Time) error {
	if w == nil {
		return nil
	}
	date := storageDate(now)
	path := filepath.Join(w.cfg.dir, storageFileName(date))
	if w.file != nil && w.loadedPath == path {
		return nil
	}
	if w.file != nil {
		w.closeFile(s, now, true)
		if w.snapshotRecords > 0 {
			w.writeSnapshot(s, now)
		}
	}
	if err := os.MkdirAll(w.cfg.dir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.writer = bufio.NewWriter(file)
	w.loadedPath = path
	w.activeDate = date
	w.report(s)
	return nil
}

func (w *storageWorkerState) flushDue(now time.Time) bool {
	if w == nil || w.writer == nil || w.buffered <= 0 {
		return false
	}
	return w.cfg.flushInterval <= 0 || w.lastFlush.IsZero() || now.Sub(w.lastFlush) >= w.cfg.flushInterval
}

func (w *storageWorkerState) syncDue(now time.Time) bool {
	if w == nil || w.file == nil || w.unsyncedRecords <= 0 {
		return false
	}
	if w.cfg.syncRecordInterval > 0 && w.unsyncedRecords >= int64(w.cfg.syncRecordInterval) {
		return true
	}
	if w.cfg.syncInterval <= 0 {
		return false
	}
	base := w.lastSync
	if base.IsZero() {
		base = w.lastFlush
	}
	return !base.IsZero() && now.Sub(base) >= w.cfg.syncInterval
}

func (w *storageWorkerState) snapshotDue(now time.Time) bool {
	if w == nil || w.snapshotRecords <= 0 {
		return false
	}
	if w.cfg.snapshotRecordInterval > 0 && w.snapshotRecords >= int64(w.cfg.snapshotRecordInterval) {
		return true
	}
	if w.cfg.snapshotInterval <= 0 {
		return false
	}
	base := w.lastSnapshot
	if base.IsZero() {
		base = w.lastFlush
	}
	return !base.IsZero() && now.Sub(base) >= w.cfg.snapshotInterval
}

func (w *storageWorkerState) flush(s *RequestStatistics, now time.Time) bool {
	if w == nil || w.writer == nil || w.buffered <= 0 {
		return true
	}
	if err := w.writer.Flush(); err != nil {
		s.setStorageLastError(err)
		return false
	}
	w.buffered = 0
	w.lastFlush = now
	w.report(s)
	return true
}

func (w *storageWorkerState) sync(s *RequestStatistics, now time.Time) bool {
	if w == nil || w.file == nil || w.unsyncedRecords <= 0 {
		return true
	}
	if !w.flush(s, now) {
		return false
	}
	if err := w.file.Sync(); err != nil {
		s.setStorageLastError(err)
		return false
	}
	w.unsyncedRecords = 0
	w.lastSync = now
	w.report(s)
	return true
}

func (w *storageWorkerState) writeSnapshot(s *RequestStatistics, now time.Time) bool {
	if w == nil || strings.TrimSpace(w.cfg.dir) == "" {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}
	snapshot := s.Snapshot()
	if err := writeStorageSnapshotFile(w.cfg.dir, snapshot, now); err != nil {
		s.setStorageLastError(err)
		return false
	}
	compacted, err := compactStorageShardsBeforeSnapshot(w.cfg.dir, now, w.loadedPath)
	if err != nil {
		s.setStorageLastError(err)
	} else if compacted > 0 {
		s.updateStorageCompaction(compacted, now)
	}
	w.lastSnapshot = now
	w.snapshotRecords = 0
	w.report(s)
	return true
}

func (w *storageWorkerState) close(s *RequestStatistics) {
	if w == nil {
		return
	}
	now := time.Now()
	w.closeFile(s, now, true)
	if strings.TrimSpace(w.cfg.dir) != "" {
		w.writeSnapshot(s, now)
	}
}

func (w *storageWorkerState) closeFile(s *RequestStatistics, now time.Time, syncFile bool) {
	if w == nil {
		return
	}
	if w.writer != nil {
		if err := w.writer.Flush(); err != nil {
			s.setStorageLastError(err)
		} else {
			w.buffered = 0
			w.lastFlush = now
		}
		w.writer = nil
	}
	if w.file != nil {
		if syncFile {
			if err := w.file.Sync(); err != nil {
				s.setStorageLastError(err)
			} else {
				w.unsyncedRecords = 0
				w.lastSync = now
			}
		}
		if err := w.file.Close(); err != nil {
			s.setStorageLastError(err)
		}
		w.file = nil
	}
	w.loadedPath = ""
	w.activeDate = ""
	w.report(s)
}

func (w *storageWorkerState) report(s *RequestStatistics) {
	if s == nil || w == nil {
		return
	}
	s.mu.Lock()
	s.storageLoadedPath = w.loadedPath
	s.storageActiveDate = w.activeDate
	s.storageLastFlush = w.lastFlush
	s.storageLastSnapshot = w.lastSnapshot
	s.storageLastSync = w.lastSync
	s.storageBuffered = w.buffered
	s.storageSnapshotRecords = w.snapshotRecords
	s.storageUnsyncedRecords = w.unsyncedRecords
	s.mu.Unlock()
}

func (s *RequestStatistics) setStorageLastError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	s.storageLastError = err.Error()
	s.mu.Unlock()
}

func (s *RequestStatistics) recordDetailLocked(apiName, modelName string, detail RequestDetail, dedup string, now time.Time, useDedupWindow bool) bool {
	if s == nil {
		return false
	}
	apiName = usageGroupKeyFromDetail(apiName, detail)
	if strings.TrimSpace(apiName) == "" {
		apiName = "未知接口"
	}
	dedup = dedupKey(apiName, modelName, detail)
	s.pruneSeenLocked(now)
	if useDedupWindow && s.dedupWindow > 0 {
		if _, exists := s.seen[dedup]; exists {
			return false
		}
		s.seen[dedup] = now
	}

	apiSt, ok := s.apis[apiName]
	if !ok {
		apiSt = &apiStats{Models: make(map[string]*modelStats), Sources: make(map[string]*sourceStatAccumulator)}
		s.apis[apiName] = apiSt
	}

	totals := s.updateAPIStats(apiSt, modelName, detail)
	incrementAPISourceStats(apiSt, detail, totals)

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totals.totalTokens
	s.inputTokens += totals.inputTokens
	s.outputTokens += totals.outputTokens
	s.cachedTokens += totals.cachedTokens
	s.reasoningTokens += totals.reasoningTokens
	s.latencySum += totals.latencySum
	s.latencyN += totals.latencyN
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()
	cost := s.detailCostLocked(modelName, detail, totals)
	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totals.totalTokens
	s.tokensByHour[hourKey] += totals.totalTokens
	s.costByDay[dayKey] += cost
	s.costByHour[hourKey] += cost
	s.costTokensByDay[dayKey] = incrementTimeSeriesTokenStats(s.costTokensByDay[dayKey], detailModel(modelName, detail), detail.Provider, totals)
	s.costTokensByHour[hourKey] = incrementTimeSeriesTokenStats(s.costTokensByHour[hourKey], detailModel(modelName, detail), detail.Provider, totals)
	s.incrementModelSummaryStatsLocked(modelName, detail, totals)
	s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
	s.incrementHealthBucketLocked(detail)
	if detail.Timestamp.After(s.lastRecordedAt) {
		s.lastRecordedAt = detail.Timestamp
	}
	s.invalidateSummaryLocked()
	return true
}

func (s *RequestStatistics) configureStorageLocked() {
	if s == nil {
		return
	}
	if !s.storageEnabled {
		s.closeStorageLocked()
		return
	}
	path := strings.TrimSpace(s.storagePath)
	if path == "" {
		path = defaultRuntimeConfig().StoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.storageLastError = err.Error()
		return
	}
	dir, legacyPath := storageLayout(abs)
	s.closeStorageLocked()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.storageLastError = err.Error()
		return
	}
	now := time.Now()
	var warnings []string
	snapshotAt, err := s.loadStorageSnapshotLocked(dir, now)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	if err := s.replayStorageFilesLocked(dir, legacyPath, now, snapshotAt); err != nil {
		warnings = append(warnings, err.Error())
	}
	s.storagePath = path
	s.storageDir = dir
	s.storageLegacyPath = legacyPath
	s.storageLoadedPath = filepath.Join(dir, storageFileName(storageDate(now)))
	s.storageActiveDate = storageDate(now)
	if !snapshotAt.IsZero() {
		s.storageLastSnapshot = snapshotAt
	} else {
		s.storageLastSnapshot = time.Time{}
	}
	s.storageSnapshotRecords = 0
	s.storageUnsyncedRecords = 0
	s.storageBuffered = 0
	if err := s.cleanupStorageFilesLocked(now); err != nil {
		warnings = append(warnings, err.Error())
	}
	if err := combineStorageWarnings(warnings); err != nil {
		s.storageLastError = err.Error()
	} else {
		s.storageLastError = ""
	}
	s.startStorageWorkerLocked()
}

func storageLayout(absPath string) (string, string) {
	if strings.EqualFold(filepath.Ext(absPath), ".jsonl") {
		return strings.TrimSuffix(absPath, filepath.Ext(absPath)), absPath
	}
	return absPath, ""
}

func storageDate(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01-02")
}

func storageFileName(date string) string {
	return "usage-" + date + ".jsonl"
}

func storageSnapshotPath(dir string) string {
	return filepath.Join(dir, "snapshot.json")
}

func parseStorageFileDate(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "usage-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, false
	}
	dateText := strings.TrimSuffix(strings.TrimPrefix(name, "usage-"), ".jsonl")
	t, err := time.Parse("2006-01-02", dateText)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (s *RequestStatistics) loadStorageSnapshotLocked(dir string, now time.Time) (time.Time, error) {
	raw, err := os.ReadFile(storageSnapshotPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var persisted persistedStorageSnapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return time.Time{}, fmt.Errorf("load storage snapshot: %w", err)
	}
	if s.hasRecordsLocked() {
		_, _ = s.mergeSnapshotLocked(persisted.Usage, false, now)
	} else {
		s.restoreStorageSnapshotLocked(persisted.Usage, now)
	}
	generatedAt, err := time.Parse(time.RFC3339, persisted.GeneratedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse storage snapshot time: %w", err)
	}
	return generatedAt, nil
}

func (s *RequestStatistics) hasRecordsLocked() bool {
	return s != nil && (s.totalRequests != 0 || len(s.apis) != 0)
}

func (s *RequestStatistics) restoreStorageSnapshotLocked(snapshot StatisticsSnapshot, now time.Time) {
	if s == nil {
		return
	}
	s.totalRequests = nonNegativeInt64(snapshot.TotalRequests)
	s.successCount = nonNegativeInt64(snapshot.SuccessCount)
	s.failureCount = nonNegativeInt64(snapshot.FailureCount)
	s.totalTokens = nonNegativeInt64(snapshot.TotalTokens)
	s.inputTokens = nonNegativeInt64(snapshot.InputTokens)
	s.outputTokens = nonNegativeInt64(snapshot.OutputTokens)
	s.cachedTokens = nonNegativeInt64(snapshot.CachedTokens)
	s.reasoningTokens = nonNegativeInt64(snapshot.ReasoningTokens)
	s.latencySum, s.latencyN = restoredLatencyAggregate(snapshot.AvgLatencyMs, s.totalRequests)
	s.apis = make(map[string]*apiStats, len(snapshot.APIs))
	s.requestsByDay = copyStringInt64Map(snapshot.RequestsByDay)
	s.requestsByHour = hourStringMapToIntMap(snapshot.RequestsByHour)
	s.tokensByDay = copyStringInt64Map(snapshot.TokensByDay)
	s.tokensByHour = hourStringMapToIntMap(snapshot.TokensByHour)
	s.costByDay = copyStringFloat64Map(snapshot.CostByDay)
	s.costByHour = hourStringMapToFloat64Map(snapshot.CostByHour)
	s.costTokensByDay = timeSeriesTokenStatsByDayFromSnapshot(snapshot.CostTokensByDay)
	s.costTokensByHour = timeSeriesTokenStatsByHourFromSnapshot(snapshot.CostTokensByHour)
	s.healthBuckets = make(map[int64]healthBucket)
	s.modelSummaryStats = make(map[string]*ModelStat)
	s.sourceStats = make(map[string]*sourceStatAccumulator)
	s.credentialStats = make(map[string]*CredentialStat)
	s.clientAPIStats = make(map[string]*clientAPIStatAccumulator)

	var restoredDetails int64
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		apiName = storageSnapshotAPIName(apiName, apiSnapshot)
		apiSt := &apiStats{
			TotalRequests:   nonNegativeInt64(apiSnapshot.TotalRequests),
			SuccessCount:    nonNegativeInt64(apiSnapshot.SuccessCount),
			FailureCount:    nonNegativeInt64(apiSnapshot.FailureCount),
			TotalTokens:     nonNegativeInt64(apiSnapshot.TotalTokens),
			InputTokens:     nonNegativeInt64(apiSnapshot.InputTokens),
			OutputTokens:    nonNegativeInt64(apiSnapshot.OutputTokens),
			CachedTokens:    nonNegativeInt64(apiSnapshot.CachedTokens),
			ReasoningTokens: nonNegativeInt64(apiSnapshot.ReasoningTokens),
			Models:          make(map[string]*modelStats, len(apiSnapshot.Models)),
			Sources:         make(map[string]*sourceStatAccumulator),
		}
		apiSt.latencySum, apiSt.latencyN = restoredLatencyAggregate(apiSnapshot.AvgLatencyMs, apiSt.TotalRequests)
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = normalizeModelName(modelName)
			modelSt := &modelStats{
				TotalRequests:   nonNegativeInt64(modelSnapshot.TotalRequests),
				SuccessCount:    nonNegativeInt64(modelSnapshot.SuccessCount),
				FailureCount:    nonNegativeInt64(modelSnapshot.FailureCount),
				TotalTokens:     nonNegativeInt64(modelSnapshot.TotalTokens),
				InputTokens:     nonNegativeInt64(modelSnapshot.InputTokens),
				OutputTokens:    nonNegativeInt64(modelSnapshot.OutputTokens),
				CachedTokens:    nonNegativeInt64(modelSnapshot.CachedTokens),
				ReasoningTokens: nonNegativeInt64(modelSnapshot.ReasoningTokens),
				Details:         make([]RequestDetail, 0, len(modelSnapshot.Details)),
			}
			modelSt.latencySum, modelSt.latencyN = restoredLatencyAggregate(modelSnapshot.AvgLatencyMs, modelSt.TotalRequests)
			var detailAggregates detailTotals
			for _, detail := range modelSnapshot.Details {
				if detail.Model == "" {
					detail.Model = modelName
				}
				if detail.Timestamp.IsZero() {
					detail.Timestamp = now
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.TTFTMs < 0 {
					detail.TTFTMs = 0
				}
				detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
				modelSt.Details = append(modelSt.Details, detail)
				restoredDetails++

				totals := detailTotalsFromRequest(detail)
				detailAggregates.totalTokens += totals.totalTokens
				detailAggregates.inputTokens += totals.inputTokens
				detailAggregates.outputTokens += totals.outputTokens
				detailAggregates.cachedTokens += totals.cachedTokens
				detailAggregates.reasoningTokens += totals.reasoningTokens
				detailAggregates.latencySum += totals.latencySum
				detailAggregates.latencyN += totals.latencyN
				incrementAPISourceStats(apiSt, detail, totals)
				modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
				s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
				s.incrementHealthBucketLocked(detail)
				if detail.Timestamp.After(s.lastRecordedAt) {
					s.lastRecordedAt = detail.Timestamp
				}
			}
			modelSt.InputTokens = maxInt64(modelSt.InputTokens, detailAggregates.inputTokens)
			modelSt.OutputTokens = maxInt64(modelSt.OutputTokens, detailAggregates.outputTokens)
			modelSt.CachedTokens = maxInt64(modelSt.CachedTokens, detailAggregates.cachedTokens)
			modelSt.ReasoningTokens = maxInt64(modelSt.ReasoningTokens, detailAggregates.reasoningTokens)
			if modelSt.latencyN == 0 && detailAggregates.latencyN > 0 {
				modelSt.latencySum = detailAggregates.latencySum
				modelSt.latencyN = detailAggregates.latencyN
			}
			apiSt.Models[modelName] = modelSt
			s.mergeModelSummaryAggregateLocked(modelName, modelSt)
		}
		restoreAPIAggregatesFromModels(apiSt)
		s.apis[apiName] = apiSt
	}
	restoreSnapshotAggregatesFromAPIs(s)
	if s.totalRequests == 0 && restoredDetails > 0 {
		s.rebuildAggregatesLocked()
	} else {
		s.restoreMissingCostSeriesLocked(restoredDetails)
	}
}

func (s *RequestStatistics) restoreMissingCostSeriesLocked(restoredDetails int64) {
	if s == nil {
		return
	}
	if restoredDetails > 0 {
		rebuildDayTokens := len(s.costTokensByDay) == 0
		rebuildHourTokens := len(s.costTokensByHour) == 0
		if rebuildDayTokens || rebuildHourTokens {
			s.rebuildCostTokenSeriesFromDetailsLocked(rebuildDayTokens, rebuildHourTokens)
		}
	}
	if len(s.costByDay) == 0 && len(s.costTokensByDay) > 0 {
		s.costByDay = s.costByDayFromTokenSeriesLocked()
	}
	if len(s.costByHour) == 0 && len(s.costTokensByHour) > 0 {
		s.costByHour = s.costByHourFromTokenSeriesLocked()
	}
}

func restoredLatencyAggregate(avgLatencyMs float64, requestCount int64) (int64, int64) {
	if !(avgLatencyMs > 0) || requestCount <= 0 {
		return 0, 0
	}
	return int64(math.Round(avgLatencyMs * float64(requestCount))), requestCount
}

func restoreAPIAggregatesFromModels(apiSt *apiStats) {
	if apiSt == nil {
		return
	}
	var inputTokens int64
	var outputTokens int64
	var cachedTokens int64
	var reasoningTokens int64
	var latencySum int64
	var latencyN int64
	for _, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		inputTokens += nonNegativeInt64(modelSt.InputTokens)
		outputTokens += nonNegativeInt64(modelSt.OutputTokens)
		cachedTokens += nonNegativeInt64(modelSt.CachedTokens)
		reasoningTokens += nonNegativeInt64(modelSt.ReasoningTokens)
		latencySum += modelSt.latencySum
		latencyN += modelSt.latencyN
	}
	apiSt.InputTokens = maxInt64(apiSt.InputTokens, inputTokens)
	apiSt.OutputTokens = maxInt64(apiSt.OutputTokens, outputTokens)
	apiSt.CachedTokens = maxInt64(apiSt.CachedTokens, cachedTokens)
	apiSt.ReasoningTokens = maxInt64(apiSt.ReasoningTokens, reasoningTokens)
	if apiSt.latencyN == 0 && latencyN > 0 {
		apiSt.latencySum = latencySum
		apiSt.latencyN = latencyN
	}
}

func restoreSnapshotAggregatesFromAPIs(s *RequestStatistics) {
	if s == nil {
		return
	}
	var inputTokens int64
	var outputTokens int64
	var cachedTokens int64
	var reasoningTokens int64
	var latencySum int64
	var latencyN int64
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		inputTokens += nonNegativeInt64(apiSt.InputTokens)
		outputTokens += nonNegativeInt64(apiSt.OutputTokens)
		cachedTokens += nonNegativeInt64(apiSt.CachedTokens)
		reasoningTokens += nonNegativeInt64(apiSt.ReasoningTokens)
		latencySum += apiSt.latencySum
		latencyN += apiSt.latencyN
	}
	s.inputTokens = maxInt64(s.inputTokens, inputTokens)
	s.outputTokens = maxInt64(s.outputTokens, outputTokens)
	s.cachedTokens = maxInt64(s.cachedTokens, cachedTokens)
	s.reasoningTokens = maxInt64(s.reasoningTokens, reasoningTokens)
	if s.latencyN == 0 && latencyN > 0 {
		s.latencySum = latencySum
		s.latencyN = latencyN
	}
}

func storageSnapshotAPIName(apiName string, apiSnapshot APISnapshot) string {
	for _, modelSnapshot := range apiSnapshot.Models {
		for _, detail := range modelSnapshot.Details {
			if key := usageGroupKeyFromDetail(apiName, detail); strings.TrimSpace(key) != "" {
				return key
			}
		}
	}
	return apiName
}

func (s *RequestStatistics) mergeModelSummaryAggregateLocked(modelName string, modelSt *modelStats) {
	if s == nil || modelSt == nil {
		return
	}
	if s.modelSummaryStats == nil {
		s.modelSummaryStats = make(map[string]*ModelStat)
	}
	modelName = normalizeModelName(modelName)
	stat, ok := s.modelSummaryStats[modelName]
	if !ok {
		stat = &ModelStat{Model: modelName}
		s.modelSummaryStats[modelName] = stat
	}
	stat.TotalRequests += modelSt.TotalRequests
	stat.SuccessCount += modelSt.SuccessCount
	stat.FailureCount += modelSt.FailureCount
	stat.TotalTokens += modelSt.TotalTokens
	stat.InputTokens += modelSt.InputTokens
	stat.OutputTokens += modelSt.OutputTokens
	stat.CachedTokens += modelSt.CachedTokens
	stat.ReasoningTokens += modelSt.ReasoningTokens
	stat.latencySum += modelSt.latencySum
	stat.latencyN += modelSt.latencyN
	for provider, providerStat := range modelSt.providerStats {
		if providerStat == nil {
			continue
		}
		if stat.providerStats == nil {
			stat.providerStats = make(map[string]*ModelProviderStat)
		}
		existing, ok := stat.providerStats[provider]
		if !ok {
			copy := *providerStat
			stat.providerStats[provider] = &copy
			continue
		}
		existing.TotalRequests += providerStat.TotalRequests
		existing.SuccessCount += providerStat.SuccessCount
		existing.FailureCount += providerStat.FailureCount
		existing.TotalTokens += providerStat.TotalTokens
		existing.InputTokens += providerStat.InputTokens
		existing.OutputTokens += providerStat.OutputTokens
		existing.CachedTokens += providerStat.CachedTokens
		existing.ReasoningTokens += providerStat.ReasoningTokens
	}
}

func copyStringInt64Map(values map[string]int64) map[string]int64 {
	if values == nil {
		return make(map[string]int64)
	}
	copied := make(map[string]int64, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func copyStringFloat64Map(values map[string]float64) map[string]float64 {
	if values == nil {
		return make(map[string]float64)
	}
	copied := make(map[string]float64, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func hourStringMapToIntMap(values map[string]int64) map[int]int64 {
	result := make(map[int]int64, len(values))
	for key, value := range values {
		hour, err := strconv.Atoi(key)
		if err != nil || hour < 0 || hour >= 24 {
			continue
		}
		result[hour] = value
	}
	return result
}

func hourStringMapToFloat64Map(values map[string]float64) map[int]float64 {
	result := make(map[int]float64, len(values))
	for key, value := range values {
		hour, err := strconv.Atoi(key)
		if err != nil || hour < 0 || hour >= 24 {
			continue
		}
		result[hour] = value
	}
	return result
}

func timeSeriesTokenStatsByDayFromSnapshot(values map[string][]TimeSeriesTokenStat) map[string]map[string]*TimeSeriesTokenStat {
	result := make(map[string]map[string]*TimeSeriesTokenStat, len(values))
	for bucket, stats := range values {
		if len(stats) == 0 {
			continue
		}
		parsed := timeSeriesTokenStatsFromSnapshot(stats)
		if len(parsed) > 0 {
			result[bucket] = parsed
		}
	}
	return result
}

func timeSeriesTokenStatsByHourFromSnapshot(values map[string][]TimeSeriesTokenStat) map[int]map[string]*TimeSeriesTokenStat {
	result := make(map[int]map[string]*TimeSeriesTokenStat, len(values))
	for key, stats := range values {
		hour, err := strconv.Atoi(key)
		if err != nil || hour < 0 || hour >= 24 || len(stats) == 0 {
			continue
		}
		parsed := timeSeriesTokenStatsFromSnapshot(stats)
		if len(parsed) > 0 {
			result[hour] = parsed
		}
	}
	return result
}

func timeSeriesTokenStatsFromSnapshot(values []TimeSeriesTokenStat) map[string]*TimeSeriesTokenStat {
	result := make(map[string]*TimeSeriesTokenStat, len(values))
	for _, value := range values {
		value.Model = strings.TrimSpace(value.Model)
		value.Provider = strings.TrimSpace(value.Provider)
		if value.Model == "" {
			continue
		}
		key := timeSeriesTokenKey(value.Model, value.Provider)
		if existing, ok := result[key]; ok {
			existing.TotalTokens += value.TotalTokens
			existing.InputTokens += value.InputTokens
			existing.OutputTokens += value.OutputTokens
			existing.CachedTokens += value.CachedTokens
			existing.ReasoningTokens += value.ReasoningTokens
			if existing.Model == "" {
				existing.Model = value.Model
			}
			if existing.Provider == "" {
				existing.Provider = value.Provider
			}
			continue
		}
		stat := value
		result[key] = &stat
	}
	return result
}

func (s *RequestStatistics) writeStorageSnapshotLocked(now time.Time) error {
	if s == nil || strings.TrimSpace(s.storageDir) == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := writeStorageSnapshotFile(s.storageDir, s.snapshotLocked(), now); err != nil {
		return err
	}
	compacted, err := compactStorageShardsBeforeSnapshot(s.storageDir, now, s.storageLoadedPath)
	if err != nil {
		return err
	}
	if compacted > 0 {
		s.storageLastCompaction = now
		s.storageLastCompactedShards = compacted
		s.storageCompactedShardsTotal += int64(compacted)
	}
	s.storageLastSnapshot = now
	s.storageSnapshotRecords = 0
	return nil
}

func writeStorageSnapshotFile(dir string, snapshot StatisticsSnapshot, now time.Time) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload := persistedStorageSnapshot{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Usage:       snapshot,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target := storageSnapshotPath(dir)
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = syncDir(dir)
	return nil
}

func compactStorageShardsBeforeSnapshot(dir string, snapshotAt time.Time, loadedPath string) (int, error) {
	if strings.TrimSpace(dir) == "" || snapshotAt.IsZero() {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	snapshotDay := snapshotAt.UTC().Truncate(24 * time.Hour)
	var compacted int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileDate, ok := parseStorageFileDate(entry.Name())
		if !ok || !fileDate.Before(snapshotDay) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if loadedPath != "" && path == loadedPath {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return compacted, err
		}
		compacted++
	}
	if compacted > 0 {
		_ = syncDir(dir)
	}
	return compacted, nil
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (s *RequestStatistics) replayStorageFilesLocked(dir string, legacyPath string, now time.Time, snapshotAt time.Time) error {
	var warnings []string
	seenFiles := make(map[string]struct{})
	if strings.TrimSpace(legacyPath) != "" && snapshotAt.IsZero() {
		if err := s.replayStorageLocked(legacyPath); err != nil {
			warnings = append(warnings, err.Error())
		}
		seenFiles[legacyPath] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return combineStorageWarnings(warnings)
		}
		return err
	}
	var files []string
	cutoff := time.Time{}
	if s.retention > 0 {
		cutoff = now.Add(-s.retention).UTC().Truncate(24 * time.Hour)
	}
	snapshotDay := time.Time{}
	if !snapshotAt.IsZero() {
		snapshotDay = snapshotAt.UTC().Truncate(24 * time.Hour)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileDate, ok := parseStorageFileDate(entry.Name())
		if !ok {
			continue
		}
		if !cutoff.IsZero() && fileDate.Before(cutoff) {
			continue
		}
		if !snapshotDay.IsZero() && fileDate.Before(snapshotDay) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, ok := seenFiles[path]; ok {
			continue
		}
		files = append(files, path)
	}
	sort.Strings(files)
	for _, path := range files {
		if err := s.replayStorageLocked(path); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return combineStorageWarnings(warnings)
}

func combineStorageWarnings(warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	return errors.New(strings.Join(warnings, "; "))
}

func (s *RequestStatistics) cleanupStorageFilesLocked(now time.Time) error {
	if s == nil || s.retention <= 0 || strings.TrimSpace(s.storageDir) == "" {
		return nil
	}
	cutoff := now.Add(-s.retention).UTC().Truncate(24 * time.Hour)
	entries, err := os.ReadDir(s.storageDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileDate, ok := parseStorageFileDate(entry.Name())
		if !ok || !fileDate.Before(cutoff) {
			continue
		}
		path := filepath.Join(s.storageDir, entry.Name())
		if path == s.storageLoadedPath {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *RequestStatistics) replayStorageLocked(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	now := time.Now()
	existing := s.detailKeysLocked()
	var invalidLines int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var persisted persistedDetail
		if err := json.Unmarshal([]byte(line), &persisted); err != nil {
			invalidLines++
			continue
		}
		apiName := strings.TrimSpace(persisted.API)
		if apiName == "" {
			invalidLines++
			continue
		}
		modelName := normalizeModelName(persisted.Model)
		detail := persisted.Detail
		if detail.Model == "" {
			detail.Model = modelName
		}
		if detail.Timestamp.IsZero() {
			detail.Timestamp = now
		}
		detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
		apiName = usageGroupKeyFromDetail(apiName, detail)
		key := dedupKey(apiName, modelName, detail)
		if _, ok := existing[key]; ok {
			continue
		}
		if s.recordDetailLocked(apiName, modelName, detail, key, now, false) {
			existing[key] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan storage: %w", err)
	}
	if invalidLines > 0 {
		return fmt.Errorf("replay storage skipped %d invalid line(s)", invalidLines)
	}
	return nil
}

func (s *RequestStatistics) detailKeysLocked() map[string]struct{} {
	keys := make(map[string]struct{})
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				keys[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}
	return keys
}

func (s *RequestStatistics) closeStorageLocked() {
	if s == nil {
		return
	}
	now := time.Now()
	s.storageLoadedPath = ""
	s.storageActiveDate = ""
	s.storageBuffered = 0
	s.storageUnsyncedRecords = 0
	s.storageWriteQueueLength = 0
	s.storageLastWriteBatchRecords = 0
	s.storageLastWriteBatchDuration = 0
	s.storageLastWriteQueueWait = 0
	s.storageWriteBatchesTotal = 0
	s.storageWriteRecordsTotal = 0
	s.storageWriteBatchDurationAvg = 0
	s.storageWriteBatchDurations = nil
	s.storageWriteQueueWaitAvg = 0
	s.storageWriteQueueWaits = nil
	s.storageWriteQueueWaitMax = 0
	if !s.storageEnabled {
		s.storageLastCompaction = time.Time{}
		s.storageLastCompactedShards = 0
		s.storageCompactedShardsTotal = 0
	}
	s.storageWorkerRunning = false
	if s.storageEnabled && strings.TrimSpace(s.storageDir) != "" {
		if err := s.writeStorageSnapshotLocked(now); err != nil {
			s.storageLastError = err.Error()
		}
	}
}

type modelsDevProviderPayload struct {
	ID     string                           `json:"id"`
	Name   string                           `json:"name"`
	Models map[string]modelsDevModelPayload `json:"models"`
}

type modelsDevModelPayload struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	LastUpdated string                `json:"last_updated"`
	Cost        *modelsDevCostPayload `json:"cost"`
}

type modelsDevCostPayload struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	CacheRead *float64 `json:"cache_read"`
}

func (s *RequestStatistics) configureModelsDevPriceWorkerLocked() {
	if s == nil {
		return
	}
	if !s.modelsDevPricesEnabled {
		stop := s.modelsDevStop
		done := s.modelsDevDone
		s.modelsDevStop = nil
		s.modelsDevDone = nil
		s.modelsDevPrices = make(map[string]ModelPrice)
		s.modelsDevUpdatedAt = time.Time{}
		s.modelsDevLastAttempt = time.Time{}
		s.modelsDevLastSuccess = time.Time{}
		s.modelsDevLastError = ""
		s.modelsDevETag = ""
		if stop != nil {
			close(stop)
			go waitModelsDevPriceWorker(done)
		}
		return
	}
	if s.modelsDevRefresh <= 0 {
		s.modelsDevRefresh = time.Duration(defaultModelsDevRefreshSeconds) * time.Second
	}
	if strings.TrimSpace(s.modelsDevPricesURL) == "" {
		s.modelsDevPricesURL = defaultModelsDevPricesURL
	}
	if s.modelsDevStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	s.modelsDevStop = stop
	s.modelsDevDone = done
	go s.modelsDevPriceWorker(stop, done)
}

func waitModelsDevPriceWorker(done <-chan struct{}) {
	if done != nil {
		<-done
	}
}

func (s *RequestStatistics) stopModelsDevPriceWorker() {
	if s == nil {
		return
	}
	s.mu.Lock()
	stop := s.modelsDevStop
	done := s.modelsDevDone
	s.modelsDevStop = nil
	s.modelsDevDone = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
}

func (s *RequestStatistics) modelsDevPriceWorker(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	s.refreshModelsDevPricesOnceWithStop(stop)
	for {
		s.mu.RLock()
		interval := s.modelsDevRefresh
		s.mu.RUnlock()
		if interval <= 0 {
			interval = time.Duration(defaultModelsDevRefreshSeconds) * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			s.refreshModelsDevPricesOnceWithStop(stop)
		}
	}
}

func (s *RequestStatistics) refreshModelsDevPricesOnce() {
	s.refreshModelsDevPricesOnceWithStop(nil)
}

func (s *RequestStatistics) refreshModelsDevPricesOnceWithStop(stop <-chan struct{}) {
	if s == nil {
		return
	}
	s.mu.RLock()
	enabled := s.modelsDevPricesEnabled
	url := strings.TrimSpace(s.modelsDevPricesURL)
	etag := s.modelsDevETag
	s.mu.RUnlock()
	if !enabled {
		return
	}
	if url == "" {
		url = defaultModelsDevPricesURL
	}
	attemptAt := time.Now().UTC()
	ctx := context.Background()
	var cancel context.CancelFunc
	if stop != nil {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.recordModelsDevPriceFetchError(attemptAt, err)
		return
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		s.recordModelsDevPriceFetchError(attemptAt, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		s.mu.Lock()
		s.modelsDevLastAttempt = attemptAt
		s.modelsDevLastSuccess = attemptAt
		s.modelsDevLastError = ""
		s.mu.Unlock()
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.recordModelsDevPriceFetchError(attemptAt, fmt.Errorf("models.dev returned HTTP %d", resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		s.recordModelsDevPriceFetchError(attemptAt, err)
		return
	}
	prices, updatedAt, err := parseModelsDevPrices(raw)
	if err != nil {
		s.recordModelsDevPriceFetchError(attemptAt, err)
		return
	}
	s.mu.Lock()
	if !s.modelsDevPricesEnabled || strings.TrimSpace(s.modelsDevPricesURL) != url {
		s.mu.Unlock()
		return
	}
	s.modelsDevPrices = prices
	s.modelsDevUpdatedAt = updatedAt
	s.modelsDevLastAttempt = attemptAt
	s.modelsDevLastSuccess = attemptAt
	s.modelsDevLastError = ""
	s.modelsDevETag = resp.Header.Get("ETag")
	s.rebuildCostSeriesLocked()
	s.invalidateSummaryLocked()
	s.mu.Unlock()
}

func (s *RequestStatistics) recordModelsDevPriceFetchError(attemptAt time.Time, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelsDevLastAttempt = attemptAt
	if err != nil {
		s.modelsDevLastError = err.Error()
	}
}

func parseModelsDevPrices(raw []byte) (map[string]ModelPrice, time.Time, error) {
	var root map[string]modelsDevProviderPayload
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, time.Time{}, err
	}
	prices := make(map[string]ModelPrice)
	seen := make(map[string]struct{})
	var updatedAt time.Time
	for _, providerID := range modelsDevProviderOrder(root) {
		provider := root[providerID]
		modelKeys := make([]string, 0, len(provider.Models))
		for modelKey := range provider.Models {
			modelKeys = append(modelKeys, modelKey)
		}
		sort.Strings(modelKeys)
		for _, modelKey := range modelKeys {
			model := provider.Models[modelKey]
			price, ok := modelPriceFromModelsDevCost(model.Cost)
			if !ok {
				continue
			}
			for _, key := range modelsDevPriceKeys(providerID, modelKey, model.ID) {
				norm := normalizeModelPriceKey(key)
				if norm == "" {
					continue
				}
				if _, exists := seen[norm]; exists {
					continue
				}
				prices[key] = price
				seen[norm] = struct{}{}
			}
			if t := parseModelsDevDate(model.LastUpdated); t.After(updatedAt) {
				updatedAt = t
			}
		}
	}
	return prices, updatedAt, nil
}

func modelsDevProviderOrder(providers map[string]modelsDevProviderPayload) []string {
	priority := []string{"openai", "anthropic", "google", "google-vertex", "xai", "deepseek", "alibaba", "moonshotai", "mistral", "cohere", "perplexity", "groq", "cerebras", "openrouter"}
	seen := make(map[string]struct{}, len(providers))
	ordered := make([]string, 0, len(providers))
	for _, provider := range priority {
		if _, ok := providers[provider]; ok {
			ordered = append(ordered, provider)
			seen[provider] = struct{}{}
		}
	}
	rest := make([]string, 0, len(providers))
	for provider := range providers {
		if _, ok := seen[provider]; !ok {
			rest = append(rest, provider)
		}
	}
	sort.Strings(rest)
	return append(ordered, rest...)
}

func modelPriceFromModelsDevCost(cost *modelsDevCostPayload) (ModelPrice, bool) {
	if cost == nil || cost.Input == nil || cost.Output == nil {
		return ModelPrice{}, false
	}
	cache := *cost.Input
	if cost.CacheRead != nil {
		cache = *cost.CacheRead
	}
	price := ModelPrice{Prompt: *cost.Input, Completion: *cost.Output, Cache: cache}
	return price, validModelPrice(price)
}

func modelsDevPriceKeys(providerID, modelKey, modelID string) []string {
	keys := []string{}
	for _, key := range []string{modelKey, modelID} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		keys = append(keys, key)
		providerID = strings.TrimSpace(providerID)
		if providerID != "" {
			keys = append(keys, providerID+"/"+key)
		}
	}
	return keys
}

func parseModelsDevDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t
	}
	return parseRFC3339OrZero(value)
}

func (s *RequestStatistics) loadModelPricesLocked() {
	if s == nil {
		return
	}
	path := strings.TrimSpace(s.priceStoragePath)
	if path == "" {
		path = defaultRuntimeConfig().PriceStoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return
	}
	if s.priceStorageLoadedPath == abs {
		return
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.priceStoragePath = path
			s.priceStorageLoadedPath = abs
			s.modelPrices = make(map[string]ModelPrice)
			s.modelPricesUpdatedAt = time.Time{}
			s.priceStorageLastError = ""
			return
		}
		s.priceStorageLastError = err.Error()
		return
	}
	var persisted struct {
		UpdatedAt string                `json:"updated_at"`
		Prices    map[string]ModelPrice `json:"prices"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		s.priceStorageLastError = err.Error()
		return
	}
	prices := make(map[string]ModelPrice, len(persisted.Prices))
	for model, price := range persisted.Prices {
		name := strings.TrimSpace(model)
		if name == "" || !validModelPrice(price) {
			continue
		}
		setModelPriceCaseInsensitive(prices, name, price)
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPrices = prices
	s.modelPricesUpdatedAt = parseRFC3339OrZero(persisted.UpdatedAt)
	s.priceStorageLastError = ""
}

func (s *RequestStatistics) saveModelPricesLocked() error {
	if s == nil {
		return nil
	}
	path := strings.TrimSpace(s.priceStoragePath)
	if path == "" {
		path = defaultRuntimeConfig().PriceStoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	updatedAt := time.Now().UTC()
	payload := struct {
		UpdatedAt string                `json:"updated_at"`
		Prices    map[string]ModelPrice `json:"prices"`
	}{
		UpdatedAt: updatedAt.Format(time.RFC3339),
		Prices:    copyModelPrices(s.modelPrices),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		s.priceStorageLastError = err.Error()
		return err
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPricesUpdatedAt = updatedAt
	s.priceStorageLastError = ""
	return nil
}

func parseRFC3339OrZero(value string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return t
}

func validModelPrice(price ModelPrice) bool {
	return validPriceNumber(price.Prompt) && validPriceNumber(price.Completion) && validPriceNumber(price.Cache)
}

func validPriceNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func copyModelPrices(source map[string]ModelPrice) map[string]ModelPrice {
	copy := make(map[string]ModelPrice, len(source))
	for model, price := range source {
		copy[model] = price
	}
	return copy
}

func normalizeModelPriceKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func setModelPriceCaseInsensitive(prices map[string]ModelPrice, model string, price ModelPrice) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	deleteModelPriceCaseInsensitive(prices, model)
	prices[model] = price
}

func deleteModelPriceCaseInsensitive(prices map[string]ModelPrice, model string) {
	norm := normalizeModelPriceKey(model)
	if norm == "" {
		return
	}
	for existing := range prices {
		if normalizeModelPriceKey(existing) == norm {
			delete(prices, existing)
		}
	}
}

func effectiveModelPrices(modelsDevPrices, manualPrices map[string]ModelPrice) map[string]ModelPrice {
	result := copyModelPrices(modelsDevPrices)
	for model, price := range manualPrices {
		setModelPriceCaseInsensitive(result, model, price)
	}
	return result
}

func (s *RequestStatistics) ModelPrices() ModelPricesResponse {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	return s.modelPricesResponseLocked()
}

func (s *RequestStatistics) modelsDevPriceStatusLocked() ModelsDevPriceStatus {
	status := ModelsDevPriceStatus{
		Enabled:        s.modelsDevPricesEnabled,
		URL:            s.modelsDevPricesURL,
		RefreshSeconds: int(s.modelsDevRefresh.Seconds()),
		ETag:           s.modelsDevETag,
		PriceCount:     len(s.modelsDevPrices),
		LastError:      s.modelsDevLastError,
	}
	if !s.modelsDevLastAttempt.IsZero() {
		status.LastAttemptAt = s.modelsDevLastAttempt.UTC().Format(time.RFC3339)
	}
	if !s.modelsDevLastSuccess.IsZero() {
		status.LastSuccessAt = s.modelsDevLastSuccess.UTC().Format(time.RFC3339)
	}
	if !s.modelsDevUpdatedAt.IsZero() {
		status.UpdatedAt = s.modelsDevUpdatedAt.UTC().Format(time.RFC3339)
	}
	return status
}

func (s *RequestStatistics) UpsertModelPrice(model string, price ModelPrice) (ModelPricesResponse, error) {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}, nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPricesResponse{}, errors.New("model is required")
	}
	if !validModelPrice(price) {
		return ModelPricesResponse{}, errors.New("price values must be non-negative finite numbers")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	if s.modelPrices == nil {
		s.modelPrices = make(map[string]ModelPrice)
	}
	setModelPriceCaseInsensitive(s.modelPrices, model, price)
	if err := s.saveModelPricesLocked(); err != nil {
		return ModelPricesResponse{}, err
	}
	s.rebuildCostSeriesLocked()
	s.invalidateSummaryLocked()
	return s.modelPricesResponseLocked(), nil
}

func (s *RequestStatistics) DeleteModelPrice(model string) (ModelPricesResponse, error) {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}, nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPricesResponse{}, errors.New("model is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	if s.modelPrices == nil {
		s.modelPrices = make(map[string]ModelPrice)
	}
	deleteModelPriceCaseInsensitive(s.modelPrices, model)
	if err := s.saveModelPricesLocked(); err != nil {
		return ModelPricesResponse{}, err
	}
	s.rebuildCostSeriesLocked()
	s.invalidateSummaryLocked()
	return s.modelPricesResponseLocked(), nil
}

func (s *RequestStatistics) modelPricesResponseLocked() ModelPricesResponse {
	response := ModelPricesResponse{
		Prices:       effectiveModelPrices(s.modelsDevPrices, s.modelPrices),
		ManualPrices: copyModelPrices(s.modelPrices),
		Storage: ModelPriceStorageStatus{
			Path:       s.priceStoragePath,
			LoadedPath: s.priceStorageLoadedPath,
			LastError:  s.priceStorageLastError,
		},
		ModelsDev: s.modelsDevPriceStatusLocked(),
	}
	if !s.modelPricesUpdatedAt.IsZero() {
		response.UpdatedAt = s.modelPricesUpdatedAt.UTC().Format(time.RFC3339)
	}
	return response
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) detailTotals {
	totals := detailTotalsFromRequest(detail)
	apiSt.TotalRequests++
	if detail.Failed {
		apiSt.FailureCount++
	} else {
		apiSt.SuccessCount++
	}
	apiSt.TotalTokens += totals.totalTokens
	apiSt.InputTokens += totals.inputTokens
	apiSt.OutputTokens += totals.outputTokens
	apiSt.CachedTokens += totals.cachedTokens
	apiSt.ReasoningTokens += totals.reasoningTokens
	apiSt.latencySum += totals.latencySum
	apiSt.latencyN += totals.latencyN

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
	modelSt.TotalTokens += totals.totalTokens
	modelSt.InputTokens += totals.inputTokens
	modelSt.OutputTokens += totals.outputTokens
	modelSt.CachedTokens += totals.cachedTokens
	modelSt.ReasoningTokens += totals.reasoningTokens
	modelSt.latencySum += totals.latencySum
	modelSt.latencyN += totals.latencyN
	modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
	modelSt.Details = append(modelSt.Details, detail)
	return totals
}

func incrementAPISourceStats(apiSt *apiStats, detail RequestDetail, totals detailTotals) {
	if apiSt == nil {
		return
	}
	if apiSt.Sources == nil {
		apiSt.Sources = make(map[string]*sourceStatAccumulator)
	}
	source := summarySourceKey(detail)
	sourceAgg, ok := apiSt.Sources[source]
	if !ok {
		sourceAgg = &sourceStatAccumulator{
			stat:      SourceStat{Source: source, Provider: detail.Provider},
			providers: make(map[string]int64),
		}
		apiSt.Sources[source] = sourceAgg
	}
	if sourceAgg.stat.Provider == "" {
		sourceAgg.stat.Provider = detail.Provider
	}
	sourceAgg.providers[detail.Provider]++
	sourceAgg.stat.TotalRequests++
	if detail.Failed {
		sourceAgg.stat.FailureCount++
	} else {
		sourceAgg.stat.SuccessCount++
	}
	sourceAgg.stat.TotalTokens += totals.totalTokens
}

func decrementAPISourceStats(apiSt *apiStats, detail RequestDetail, totals detailTotals) {
	if apiSt == nil || apiSt.Sources == nil {
		return
	}
	source := summarySourceKey(detail)
	sourceAgg, ok := apiSt.Sources[source]
	if !ok {
		return
	}
	sourceAgg.stat.TotalRequests--
	if detail.Failed {
		sourceAgg.stat.FailureCount--
	} else {
		sourceAgg.stat.SuccessCount--
	}
	sourceAgg.stat.TotalTokens -= totals.totalTokens
	if sourceAgg.providers != nil {
		sourceAgg.providers[detail.Provider]--
		if sourceAgg.providers[detail.Provider] <= 0 {
			delete(sourceAgg.providers, detail.Provider)
		}
		if sourceAgg.stat.Provider == detail.Provider {
			sourceAgg.stat.Provider = ""
			for provider := range sourceAgg.providers {
				sourceAgg.stat.Provider = provider
				break
			}
		}
	}
	if sourceAgg.stat.TotalRequests <= 0 {
		delete(apiSt.Sources, source)
	}
}

func (s *RequestStatistics) incrementModelSummaryStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if s.modelSummaryStats == nil {
		s.modelSummaryStats = make(map[string]*ModelStat)
	}
	modelStat, ok := s.modelSummaryStats[modelName]
	if !ok {
		modelStat = &ModelStat{Model: modelName}
		s.modelSummaryStats[modelName] = modelStat
	}
	modelStat.TotalRequests++
	if detail.Failed {
		modelStat.FailureCount++
	} else {
		modelStat.SuccessCount++
	}
	modelStat.TotalTokens += totals.totalTokens
	modelStat.InputTokens += totals.inputTokens
	modelStat.OutputTokens += totals.outputTokens
	modelStat.CachedTokens += totals.cachedTokens
	modelStat.ReasoningTokens += totals.reasoningTokens
	modelStat.latencySum += totals.latencySum
	modelStat.latencyN += totals.latencyN
	modelStat.providerStats = incrementModelProviderStats(modelStat.providerStats, detail.Provider, detail.Failed, totals)
}

func (s *RequestStatistics) decrementModelSummaryStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	modelStat, ok := s.modelSummaryStats[modelName]
	if !ok {
		return
	}
	modelStat.TotalRequests--
	if detail.Failed {
		modelStat.FailureCount--
	} else {
		modelStat.SuccessCount--
	}
	modelStat.TotalTokens -= totals.totalTokens
	modelStat.InputTokens -= totals.inputTokens
	modelStat.OutputTokens -= totals.outputTokens
	modelStat.CachedTokens -= totals.cachedTokens
	modelStat.ReasoningTokens -= totals.reasoningTokens
	modelStat.latencySum -= totals.latencySum
	modelStat.latencyN -= totals.latencyN
	decrementModelProviderStats(modelStat.providerStats, detail.Provider, detail.Failed, totals)
	if modelStat.TotalRequests <= 0 {
		delete(s.modelSummaryStats, modelName)
	}
}

func (s *RequestStatistics) incrementSummaryDimensionStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if s.sourceStats == nil {
		s.sourceStats = make(map[string]*sourceStatAccumulator)
	}
	if s.credentialStats == nil {
		s.credentialStats = make(map[string]*CredentialStat)
	}
	if s.clientAPIStats == nil {
		s.clientAPIStats = make(map[string]*clientAPIStatAccumulator)
	}

	source := summarySourceKey(detail)
	sourceAgg, ok := s.sourceStats[source]
	if !ok {
		sourceAgg = &sourceStatAccumulator{
			stat:      SourceStat{Source: source, Provider: detail.Provider},
			providers: make(map[string]int64),
		}
		s.sourceStats[source] = sourceAgg
	}
	if sourceAgg.stat.Provider == "" {
		sourceAgg.stat.Provider = detail.Provider
	}
	sourceAgg.providers[detail.Provider]++
	sourceAgg.stat.TotalRequests++
	if detail.Failed {
		sourceAgg.stat.FailureCount++
	} else {
		sourceAgg.stat.SuccessCount++
	}
	sourceAgg.stat.TotalTokens += totals.totalTokens

	credential := summaryCredentialKey(detail)
	credentialStat, ok := s.credentialStats[credential]
	if !ok {
		credentialStat = &CredentialStat{AuthIndex: credential}
		s.credentialStats[credential] = credentialStat
	}
	credentialStat.TotalRequests++
	if detail.Failed {
		credentialStat.FailureCount++
	} else {
		credentialStat.SuccessCount++
	}
	credentialStat.TotalTokens += totals.totalTokens

	clientKey := clientAPIGroupKey(detail)
	clientAgg, ok := s.clientAPIStats[clientKey]
	if !ok {
		clientAgg = &clientAPIStatAccumulator{
			stat: ClientAPIStat{
				APIKey:     clientAPIGroupLabel(detail),
				APIKeyHash: detail.APIKeyHash,
			},
			models: make(map[string]*ClientAPIModelStat),
		}
		s.clientAPIStats[clientKey] = clientAgg
	}
	clientAgg.stat.TotalRequests++
	if detail.Failed {
		clientAgg.stat.FailureCount++
	} else {
		clientAgg.stat.SuccessCount++
	}
	clientAgg.stat.TotalTokens += totals.totalTokens
	clientAgg.stat.InputTokens += totals.inputTokens
	clientAgg.stat.OutputTokens += totals.outputTokens
	clientAgg.stat.CachedTokens += totals.cachedTokens
	clientAgg.stat.ReasoningTokens += totals.reasoningTokens

	clientModel, ok := clientAgg.models[modelName]
	if !ok {
		clientModel = &ClientAPIModelStat{Model: modelName}
		clientAgg.models[modelName] = clientModel
	}
	clientModel.TotalRequests++
	if detail.Failed {
		clientModel.FailureCount++
	} else {
		clientModel.SuccessCount++
	}
	clientModel.TotalTokens += totals.totalTokens
	clientModel.InputTokens += totals.inputTokens
	clientModel.OutputTokens += totals.outputTokens
	clientModel.CachedTokens += totals.cachedTokens
	clientModel.ReasoningTokens += totals.reasoningTokens
	clientModel.providerStats = incrementModelProviderStats(clientModel.providerStats, detail.Provider, detail.Failed, totals)
}

func (s *RequestStatistics) decrementSummaryDimensionStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if sourceAgg, ok := s.sourceStats[summarySourceKey(detail)]; ok {
		sourceAgg.stat.TotalRequests--
		if detail.Failed {
			sourceAgg.stat.FailureCount--
		} else {
			sourceAgg.stat.SuccessCount--
		}
		sourceAgg.stat.TotalTokens -= totals.totalTokens
		if sourceAgg.providers != nil {
			sourceAgg.providers[detail.Provider]--
			if sourceAgg.providers[detail.Provider] <= 0 {
				delete(sourceAgg.providers, detail.Provider)
			}
			if sourceAgg.stat.Provider == detail.Provider {
				sourceAgg.stat.Provider = ""
				for provider := range sourceAgg.providers {
					sourceAgg.stat.Provider = provider
					break
				}
			}
		}
		if sourceAgg.stat.TotalRequests <= 0 {
			delete(s.sourceStats, summarySourceKey(detail))
		}
	}

	if credentialStat, ok := s.credentialStats[summaryCredentialKey(detail)]; ok {
		credentialStat.TotalRequests--
		if detail.Failed {
			credentialStat.FailureCount--
		} else {
			credentialStat.SuccessCount--
		}
		credentialStat.TotalTokens -= totals.totalTokens
		if credentialStat.TotalRequests <= 0 {
			delete(s.credentialStats, summaryCredentialKey(detail))
		}
	}

	clientKey := clientAPIGroupKey(detail)
	if clientAgg, ok := s.clientAPIStats[clientKey]; ok {
		clientAgg.stat.TotalRequests--
		if detail.Failed {
			clientAgg.stat.FailureCount--
		} else {
			clientAgg.stat.SuccessCount--
		}
		clientAgg.stat.TotalTokens -= totals.totalTokens
		clientAgg.stat.InputTokens -= totals.inputTokens
		clientAgg.stat.OutputTokens -= totals.outputTokens
		clientAgg.stat.CachedTokens -= totals.cachedTokens
		clientAgg.stat.ReasoningTokens -= totals.reasoningTokens

		if clientModel, ok := clientAgg.models[modelName]; ok {
			clientModel.TotalRequests--
			if detail.Failed {
				clientModel.FailureCount--
			} else {
				clientModel.SuccessCount--
			}
			clientModel.TotalTokens -= totals.totalTokens
			clientModel.InputTokens -= totals.inputTokens
			clientModel.OutputTokens -= totals.outputTokens
			clientModel.CachedTokens -= totals.cachedTokens
			clientModel.ReasoningTokens -= totals.reasoningTokens
			decrementModelProviderStats(clientModel.providerStats, detail.Provider, detail.Failed, totals)
			if clientModel.TotalRequests <= 0 {
				delete(clientAgg.models, modelName)
			}
		}
		if clientAgg.stat.TotalRequests <= 0 {
			delete(s.clientAPIStats, clientKey)
		}
	}
}

func (s *RequestStatistics) incrementHealthBucketLocked(detail RequestDetail) {
	key, ok := healthBucketKey(detail.Timestamp)
	if !ok {
		return
	}
	if s.healthBuckets == nil {
		s.healthBuckets = make(map[int64]healthBucket)
	}
	bucket := s.healthBuckets[key]
	if detail.Failed {
		bucket.failure++
	} else {
		bucket.success++
	}
	s.healthBuckets[key] = bucket
}

func (s *RequestStatistics) decrementHealthBucketLocked(detail RequestDetail) {
	key, ok := healthBucketKey(detail.Timestamp)
	if !ok || s.healthBuckets == nil {
		return
	}
	bucket, ok := s.healthBuckets[key]
	if !ok {
		return
	}
	if detail.Failed {
		bucket.failure--
	} else {
		bucket.success--
	}
	if bucket.success <= 0 && bucket.failure <= 0 {
		delete(s.healthBuckets, key)
		return
	}
	s.healthBuckets[key] = bucket
}

func (s *RequestStatistics) pruneLocked(now time.Time, sortNeeded bool) {
	if s == nil {
		return
	}
	changed := false
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
						s.decrementCounters(d, apiSt, modelSt, modelName)
						s.evictedTotal++
						changed = true
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
				s.evictedTotal += int64(len(removed))
				changed = true
				details = append([]RequestDetail(nil), details[len(details)-keep:]...)
			}
			modelSt.Details = details
			if len(modelSt.Details) == 0 && modelSt.TotalRequests <= 0 {
				delete(apiSt.Models, modelName)
			}
		}
		if len(apiSt.Models) == 0 && apiSt.TotalRequests <= 0 {
			delete(s.apis, apiName)
		}
	}
	if changed {
		s.invalidateSummaryLocked()
	}
}

func (s *RequestStatistics) decrementCounters(d RequestDetail, apiSt *apiStats, modelSt *modelStats, modelName string) {
	totals := detailTotalsFromRequest(d)
	s.totalRequests--
	if d.Failed {
		s.failureCount--
	} else {
		s.successCount--
	}
	s.totalTokens -= totals.totalTokens
	s.inputTokens -= totals.inputTokens
	s.outputTokens -= totals.outputTokens
	s.cachedTokens -= totals.cachedTokens
	s.reasoningTokens -= totals.reasoningTokens
	s.latencySum -= totals.latencySum
	s.latencyN -= totals.latencyN

	apiSt.TotalRequests--
	if d.Failed {
		apiSt.FailureCount--
	} else {
		apiSt.SuccessCount--
	}
	apiSt.TotalTokens -= totals.totalTokens
	apiSt.InputTokens -= totals.inputTokens
	apiSt.OutputTokens -= totals.outputTokens
	apiSt.CachedTokens -= totals.cachedTokens
	apiSt.ReasoningTokens -= totals.reasoningTokens
	apiSt.latencySum -= totals.latencySum
	apiSt.latencyN -= totals.latencyN
	decrementAPISourceStats(apiSt, d, totals)

	modelSt.TotalRequests--
	if d.Failed {
		modelSt.FailureCount--
	} else {
		modelSt.SuccessCount--
	}
	modelSt.TotalTokens -= totals.totalTokens
	modelSt.InputTokens -= totals.inputTokens
	modelSt.OutputTokens -= totals.outputTokens
	modelSt.CachedTokens -= totals.cachedTokens
	modelSt.ReasoningTokens -= totals.reasoningTokens
	modelSt.latencySum -= totals.latencySum
	modelSt.latencyN -= totals.latencyN
	decrementModelProviderStats(modelSt.providerStats, d.Provider, d.Failed, totals)

	dayKey := d.Timestamp.Format("2006-01-02")
	hourKey := d.Timestamp.Hour()
	cost := s.detailCostLocked(modelName, d, totals)
	s.requestsByDay[dayKey]--
	s.requestsByHour[hourKey]--
	s.tokensByDay[dayKey] -= totals.totalTokens
	s.tokensByHour[hourKey] -= totals.totalTokens
	if decrementTimeSeriesTokenStats(s.costTokensByDay[dayKey], detailModel(modelName, d), d.Provider, totals) {
		s.costByDay[dayKey] -= cost
	}
	if len(s.costTokensByDay[dayKey]) == 0 {
		delete(s.costTokensByDay, dayKey)
	}
	if decrementTimeSeriesTokenStats(s.costTokensByHour[hourKey], detailModel(modelName, d), d.Provider, totals) {
		s.costByHour[hourKey] -= cost
	}
	if len(s.costTokensByHour[hourKey]) == 0 {
		delete(s.costTokensByHour, hourKey)
	}
	s.decrementModelSummaryStatsLocked(modelName, d, totals)
	s.decrementSummaryDimensionStatsLocked(modelName, d, totals)
	s.decrementHealthBucketLocked(d)
}

func (s *RequestStatistics) rebuildAggregatesLocked() {
	if s == nil {
		return
	}
	s.totalRequests = 0
	s.successCount = 0
	s.failureCount = 0
	s.totalTokens = 0
	s.inputTokens = 0
	s.outputTokens = 0
	s.cachedTokens = 0
	s.reasoningTokens = 0
	s.latencySum = 0
	s.latencyN = 0
	s.requestsByDay = make(map[string]int64)
	s.requestsByHour = make(map[int]int64)
	s.tokensByDay = make(map[string]int64)
	s.tokensByHour = make(map[int]int64)
	s.costByDay = make(map[string]float64)
	s.costByHour = make(map[int]float64)
	s.costTokensByDay = make(map[string]map[string]*TimeSeriesTokenStat)
	s.costTokensByHour = make(map[int]map[string]*TimeSeriesTokenStat)
	s.healthBuckets = make(map[int64]healthBucket)
	s.modelSummaryStats = make(map[string]*ModelStat)
	s.sourceStats = make(map[string]*sourceStatAccumulator)
	s.credentialStats = make(map[string]*CredentialStat)
	s.clientAPIStats = make(map[string]*clientAPIStatAccumulator)
	for _, apiSt := range s.apis {
		apiSt.TotalRequests = 0
		apiSt.SuccessCount = 0
		apiSt.FailureCount = 0
		apiSt.TotalTokens = 0
		apiSt.InputTokens = 0
		apiSt.OutputTokens = 0
		apiSt.CachedTokens = 0
		apiSt.ReasoningTokens = 0
		apiSt.latencySum = 0
		apiSt.latencyN = 0
		apiSt.Sources = make(map[string]*sourceStatAccumulator)
		for modelName, modelSt := range apiSt.Models {
			modelSt.TotalRequests = 0
			modelSt.SuccessCount = 0
			modelSt.FailureCount = 0
			modelSt.TotalTokens = 0
			modelSt.InputTokens = 0
			modelSt.OutputTokens = 0
			modelSt.CachedTokens = 0
			modelSt.ReasoningTokens = 0
			modelSt.latencySum = 0
			modelSt.latencyN = 0
			modelSt.providerStats = nil
			for _, detail := range modelSt.Details {
				totals := detailTotalsFromRequest(detail)
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
				s.totalTokens += totals.totalTokens
				s.inputTokens += totals.inputTokens
				s.outputTokens += totals.outputTokens
				s.cachedTokens += totals.cachedTokens
				s.reasoningTokens += totals.reasoningTokens
				s.latencySum += totals.latencySum
				s.latencyN += totals.latencyN
				apiSt.TotalTokens += totals.totalTokens
				apiSt.InputTokens += totals.inputTokens
				apiSt.OutputTokens += totals.outputTokens
				apiSt.CachedTokens += totals.cachedTokens
				apiSt.ReasoningTokens += totals.reasoningTokens
				apiSt.latencySum += totals.latencySum
				apiSt.latencyN += totals.latencyN
				incrementAPISourceStats(apiSt, detail, totals)
				modelSt.TotalTokens += totals.totalTokens
				modelSt.InputTokens += totals.inputTokens
				modelSt.OutputTokens += totals.outputTokens
				modelSt.CachedTokens += totals.cachedTokens
				modelSt.ReasoningTokens += totals.reasoningTokens
				modelSt.latencySum += totals.latencySum
				modelSt.latencyN += totals.latencyN
				modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := detail.Timestamp.Hour()
				cost := s.detailCostLocked(modelName, detail, totals)
				s.requestsByDay[dayKey]++
				s.requestsByHour[hourKey]++
				s.tokensByDay[dayKey] += totals.totalTokens
				s.tokensByHour[hourKey] += totals.totalTokens
				s.costByDay[dayKey] += cost
				s.costByHour[hourKey] += cost
				s.costTokensByDay[dayKey] = incrementTimeSeriesTokenStats(s.costTokensByDay[dayKey], detailModel(modelName, detail), detail.Provider, totals)
				s.costTokensByHour[hourKey] = incrementTimeSeriesTokenStats(s.costTokensByHour[hourKey], detailModel(modelName, detail), detail.Provider, totals)
				s.incrementModelSummaryStatsLocked(modelName, detail, totals)
				s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
				s.incrementHealthBucketLocked(detail)
			}
		}
	}
}

func (s *RequestStatistics) rebuildCostSeriesLocked() {
	if s == nil {
		return
	}
	if len(s.costTokensByDay) == 0 && len(s.costTokensByHour) == 0 {
		return
	}
	if len(s.costTokensByDay) > 0 {
		s.costByDay = s.costByDayFromTokenSeriesLocked()
	}
	if len(s.costTokensByHour) > 0 {
		s.costByHour = s.costByHourFromTokenSeriesLocked()
	}
}

func (s *RequestStatistics) rebuildCostTokenSeriesFromDetailsLocked(rebuildDay, rebuildHour bool) {
	if s == nil || (!rebuildDay && !rebuildHour) {
		return
	}
	if rebuildDay {
		s.costTokensByDay = make(map[string]map[string]*TimeSeriesTokenStat)
	}
	if rebuildHour {
		s.costTokensByHour = make(map[int]map[string]*TimeSeriesTokenStat)
	}
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				totals := detailTotalsFromRequest(detail)
				if rebuildDay {
					dayKey := detail.Timestamp.Format("2006-01-02")
					s.costTokensByDay[dayKey] = incrementTimeSeriesTokenStats(s.costTokensByDay[dayKey], detailModel(modelName, detail), detail.Provider, totals)
				}
				if rebuildHour {
					hourKey := detail.Timestamp.Hour()
					s.costTokensByHour[hourKey] = incrementTimeSeriesTokenStats(s.costTokensByHour[hourKey], detailModel(modelName, detail), detail.Provider, totals)
				}
			}
		}
	}
}

func (s *RequestStatistics) costByDayFromTokenSeriesLocked() map[string]float64 {
	result := make(map[string]float64, len(s.costTokensByDay))
	for day, stats := range s.costTokensByDay {
		for _, stat := range stats {
			if stat == nil {
				continue
			}
			result[day] += s.timeSeriesTokenCostLocked(*stat)
		}
	}
	return result
}

func (s *RequestStatistics) costByHourFromTokenSeriesLocked() map[int]float64 {
	result := make(map[int]float64, len(s.costTokensByHour))
	for hour, stats := range s.costTokensByHour {
		for _, stat := range stats {
			if stat == nil {
				continue
			}
			result[hour] += s.timeSeriesTokenCostLocked(*stat)
		}
	}
	return result
}

// Snapshot returns a full deep-copy of all statistics including details.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *RequestStatistics) snapshotLocked() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens
	result.InputTokens = s.inputTokens
	result.OutputTokens = s.outputTokens
	result.CachedTokens = s.cachedTokens
	result.ReasoningTokens = s.reasoningTokens
	if s.latencyN > 0 {
		result.AvgLatencyMs = float64(s.latencySum) / float64(s.latencyN)
	}

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, apiSt := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests:   apiSt.TotalRequests,
			SuccessCount:    apiSt.SuccessCount,
			FailureCount:    apiSt.FailureCount,
			TotalTokens:     apiSt.TotalTokens,
			InputTokens:     apiSt.InputTokens,
			OutputTokens:    apiSt.OutputTokens,
			CachedTokens:    apiSt.CachedTokens,
			ReasoningTokens: apiSt.ReasoningTokens,
			Models:          make(map[string]ModelSnapshot, len(apiSt.Models)),
		}
		if apiSt.latencyN > 0 {
			apiSnapshot.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
		}
		for modelName, modelSt := range apiSt.Models {
			details := make([]RequestDetail, len(modelSt.Details))
			copy(details, modelSt.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests:   modelSt.TotalRequests,
				SuccessCount:    modelSt.SuccessCount,
				FailureCount:    modelSt.FailureCount,
				TotalTokens:     modelSt.TotalTokens,
				InputTokens:     modelSt.InputTokens,
				OutputTokens:    modelSt.OutputTokens,
				CachedTokens:    modelSt.CachedTokens,
				ReasoningTokens: modelSt.ReasoningTokens,
				Details:         details,
			}
			if modelSt.latencyN > 0 {
				modelSnapshot := apiSnapshot.Models[modelName]
				modelSnapshot.AvgLatencyMs = float64(modelSt.latencySum) / float64(modelSt.latencyN)
				apiSnapshot.Models[modelName] = modelSnapshot
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

	result.CostByDay = make(map[string]float64, len(s.costByDay))
	for k, v := range s.costByDay {
		result.CostByDay[k] = v
	}

	result.CostByHour = make(map[string]float64, 24)
	for hour, v := range s.costByHour {
		if hour >= 0 && hour < 24 {
			result.CostByHour[hourKeys[hour]] = v
		}
	}
	result.CostTokensByDay = timeSeriesTokenStatsByDaySnapshot(s.costTokensByDay)
	result.CostTokensByHour = timeSeriesTokenStatsByHourSnapshot(s.costTokensByHour)

	return result
}

// MergeSnapshot imports a snapshot into the current statistics.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	result, persisted := s.mergeSnapshotLocked(snapshot, true, time.Now())
	s.mu.Unlock()
	for _, detail := range persisted {
		s.enqueueStorageDetail(detail)
	}
	return result
}

func (s *RequestStatistics) mergeSnapshotLocked(snapshot StatisticsSnapshot, persist bool, now time.Time) (MergeResult, []persistedDetail) {
	result := MergeResult{}
	var persisted []persistedDetail
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

				importAPIName := usageGroupKeyFromDetail(apiName, detail)
				key := dedupKey(importAPIName, importModelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}

				if s.recordImported(importAPIName, importModelName, detail, now) {
					if persist && s.storageEnabled {
						persisted = append(persisted, persistedDetail{API: importAPIName, Model: importModelName, Detail: detail})
					}
					result.Added++
				}
			}
		}
	}

	s.pruneLocked(now, true)
	s.rebuildSeenLocked(now)
	return result, persisted
}

func (s *RequestStatistics) recordImported(apiName, modelName string, detail RequestDetail, now time.Time) bool {
	return s.recordDetailLocked(apiName, modelName, detail, dedupKey(apiName, modelName, detail), now, false)
}

func usageDetailTotalTokens(detail UsageDetail) int64 {
	totalTokens := detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = detail.InputTokens + detail.OutputTokens
	}
	return nonNegativeInt64(totalTokens)
}

func detailTotalTokens(tokens TokenStats) int64 {
	totalTokens := tokens.TotalTokens
	if totalTokens == 0 {
		totalTokens = tokens.InputTokens + tokens.OutputTokens
	}
	return nonNegativeInt64(totalTokens)
}

func detailTotalsFromRequest(detail RequestDetail) detailTotals {
	totals := detailTotals{
		totalTokens:     detailTotalTokens(detail.Tokens),
		inputTokens:     nonNegativeInt64(detail.Tokens.InputTokens),
		outputTokens:    nonNegativeInt64(detail.Tokens.OutputTokens),
		cachedTokens:    normalizedCacheTokens(detail.Tokens),
		reasoningTokens: nonNegativeInt64(detail.Tokens.ReasoningTokens),
	}
	if detail.LatencyMs > 0 {
		totals.latencySum = detail.LatencyMs
		totals.latencyN = 1
	}
	return totals
}

func (s *RequestStatistics) detailCostLocked(modelName string, detail RequestDetail, totals detailTotals) float64 {
	if s == nil {
		return 0
	}
	return s.timeSeriesTokenCostLocked(TimeSeriesTokenStat{
		Model:        detailModel(modelName, detail),
		Provider:     detail.Provider,
		TotalTokens:  totals.totalTokens,
		InputTokens:  totals.inputTokens,
		OutputTokens: totals.outputTokens,
		CachedTokens: totals.cachedTokens,
	})
}

func (s *RequestStatistics) timeSeriesTokenCostLocked(stat TimeSeriesTokenStat) float64 {
	if s == nil {
		return 0
	}
	price, ok := s.priceForDetailLocked(stat.Model, stat.Provider)
	if !ok {
		return 0
	}
	cached := float64(nonNegativeInt64(stat.CachedTokens))
	input := float64(nonNegativeInt64(stat.InputTokens)) - cached
	if input < 0 {
		input = 0
	}
	return input/1e6*price.Prompt + float64(nonNegativeInt64(stat.OutputTokens))/1e6*price.Completion + cached/1e6*price.Cache
}

func (s *RequestStatistics) priceForDetailLocked(modelName, provider string) (ModelPrice, bool) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if price, ok := priceForDetailFromMap(s.modelPrices, modelName, provider); ok {
		return price, true
	}
	return priceForDetailFromMap(s.modelsDevPrices, modelName, provider)
}

func priceForDetailFromMap(prices map[string]ModelPrice, modelName, provider string) (ModelPrice, bool) {
	for _, key := range modelPriceLookupKeys(modelName, provider) {
		if price, ok := modelPriceCaseInsensitive(prices, key); ok {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func modelPriceLookupKeys(modelName, provider string) []string {
	modelName = strings.TrimSpace(modelName)
	provider = strings.TrimSpace(provider)
	keys := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	add := func(key string) {
		key = strings.TrimSpace(key)
		norm := normalizeModelPriceKey(key)
		if norm == "" {
			return
		}
		if _, ok := seen[norm]; ok {
			return
		}
		keys = append(keys, key)
		seen[norm] = struct{}{}
	}
	if provider != "" && modelName != "" {
		add(provider + "/" + modelName)
	}
	add(modelName)
	if idx := strings.Index(modelName, "/"); idx > 0 && idx < len(modelName)-1 {
		add(modelName[idx+1:])
	}
	return keys
}

func modelPriceCaseInsensitive(prices map[string]ModelPrice, model string) (ModelPrice, bool) {
	norm := normalizeModelPriceKey(model)
	if norm == "" || len(prices) == 0 {
		return ModelPrice{}, false
	}
	for key, price := range prices {
		if normalizeModelPriceKey(key) == norm {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func summarySourceKey(detail RequestDetail) string {
	source := detail.Source
	if source == "" {
		return "未知来源"
	}
	return source
}

func summaryCredentialKey(detail RequestDetail) string {
	if detail.AuthIndex == "" {
		return "(空)"
	}
	return detail.AuthIndex
}

func healthBucketKey(t time.Time) (int64, bool) {
	if t.IsZero() {
		return 0, false
	}
	return t.UTC().Truncate(dashboardHealthStep).Unix(), true
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

func nonNegativeIntFromInt64(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func durationMilliseconds(value time.Duration) float64 {
	if value <= 0 {
		return 0
	}
	return float64(value) / float64(time.Millisecond)
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
	if s == nil {
		return DashboardSummary{}
	}

	startedAt := time.Now()
	now := startedAt
	healthWindow := summaryHealthWindow(now)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.summaryCacheValid && s.summaryCacheVersion == s.summaryVersion && s.summaryCacheWindow.Equal(healthWindow) {
		s.summaryCacheHits++
		s.lastSummaryDuration = time.Since(startedAt)
		return cloneDashboardSummaryWithGeneratedAt(s.summaryCache, now)
	}

	s.summaryCacheMisses++
	summary := s.buildSummaryWithoutDetailsLocked(now, healthWindow)
	s.summaryCache = cloneDashboardSummary(summary)
	s.summaryCacheValid = true
	s.summaryCacheVersion = s.summaryVersion
	s.summaryCacheWindow = healthWindow
	s.lastSummaryDuration = time.Since(startedAt)
	return summary
}

// SummaryWithoutDetailsForRange computes a lightweight dashboard summary scoped to
// the given time range. "all" or empty rangeKey delegates to the fast pre-aggregated path.
func (s *RequestStatistics) SummaryWithoutDetailsForRange(rangeKey string) DashboardSummary {
	if s == nil {
		return DashboardSummary{}
	}
	if rangeKey == "" || rangeKey == "all" {
		return s.SummaryWithoutDetails()
	}

	startedAt := time.Now()
	now := startedAt
	healthWindow := summaryHealthWindow(now)
	cutoff := dashboardRangeCutoff(rangeKey, now)
	if cutoff.IsZero() {
		return s.SummaryWithoutDetails()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.summaryRangeCache == nil {
		s.summaryRangeCache = make(map[string]DashboardSummary)
		s.summaryRangeCacheWindow = make(map[string]time.Time)
	}
	cacheKey := summaryRangeCacheKey(rangeKey, now)
	cached, ok := s.summaryRangeCache[cacheKey]
	if ok && s.summaryRangeCacheWindow != nil {
		if window, hasWindow := s.summaryRangeCacheWindow[cacheKey]; hasWindow && window.Equal(healthWindow) {
			s.summaryCacheHits++
			s.lastSummaryDuration = time.Since(startedAt)
			return cloneDashboardSummaryWithGeneratedAt(cached, now)
		}
	}

	s.summaryCacheMisses++
	summary := s.buildSummaryWithoutDetailsForRangeLocked(now, healthWindow, cutoff)
	s.summaryRangeCache[cacheKey] = cloneDashboardSummary(summary)
	s.summaryRangeCacheWindow[cacheKey] = healthWindow
	s.pruneSummaryRangeCacheLocked(cacheKey)
	s.lastSummaryDuration = time.Since(startedAt)
	return summary
}

func summaryRangeCacheKey(rangeKey string, now time.Time) string {
	return rangeKey + "|" + strconv.FormatInt(summaryRangeCacheBucket(now).Unix(), 10)
}

func summaryRangeCacheBucket(now time.Time) time.Time {
	return now.UTC().Truncate(dashboardSummaryRangeCacheStep)
}

func (s *RequestStatistics) pruneSummaryRangeCacheLocked(keepKey string) {
	if s == nil || len(s.summaryRangeCache) <= dashboardSummaryRangeCacheMax {
		return
	}
	for key := range s.summaryRangeCache {
		if len(s.summaryRangeCache) <= dashboardSummaryRangeCacheMax {
			break
		}
		if key == keepKey && len(s.summaryRangeCache) > 1 {
			continue
		}
		delete(s.summaryRangeCache, key)
		if s.summaryRangeCacheWindow != nil {
			delete(s.summaryRangeCacheWindow, key)
		}
	}
}

func summaryHealthWindow(now time.Time) time.Time {
	return now.UTC().Truncate(dashboardHealthStep).Add(dashboardHealthStep)
}

func cloneDashboardSummaryWithGeneratedAt(summary DashboardSummary, now time.Time) DashboardSummary {
	cloned := cloneDashboardSummary(summary)
	cloned.GeneratedAt = now.UTC().Format(time.RFC3339)
	return cloned
}

func cloneDashboardSummary(summary DashboardSummary) DashboardSummary {
	cloned := summary
	cloned.Usage = cloneStatisticsSnapshotWithoutDetails(summary.Usage)
	cloned.HealthGrid = append([]HealthGridSlot(nil), summary.HealthGrid...)
	cloned.SourceStats = append([]SourceStat(nil), summary.SourceStats...)
	cloned.CredentialStats = append([]CredentialStat(nil), summary.CredentialStats...)
	cloned.ClientAPIStats = make([]ClientAPIStat, len(summary.ClientAPIStats))
	for i, stat := range summary.ClientAPIStats {
		cloned.ClientAPIStats[i] = stat
		cloned.ClientAPIStats[i].Models = cloneClientAPIModelStats(stat.Models)
	}
	cloned.ModelStats = cloneModelStats(summary.ModelStats)
	if summary.Meta.LastImport != nil {
		lastImport := *summary.Meta.LastImport
		cloned.Meta.LastImport = &lastImport
	}
	return cloned
}

func cloneStatisticsSnapshotWithoutDetails(snapshot StatisticsSnapshotWithoutDetails) StatisticsSnapshotWithoutDetails {
	cloned := snapshot
	cloned.APIs = make(map[string]APISnapshotWithoutDetails, len(snapshot.APIs))
	for apiName, apiSnapshot := range snapshot.APIs {
		apiClone := apiSnapshot
		apiClone.Models = make(map[string]ModelSnapshotWithoutDetails, len(apiSnapshot.Models))
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelSnapshot.Providers = cloneModelProviderStats(modelSnapshot.Providers)
			apiClone.Models[modelName] = modelSnapshot
		}
		cloned.APIs[apiName] = apiClone
	}
	cloned.RequestsByDay = cloneInt64Map(snapshot.RequestsByDay)
	cloned.RequestsByHour = cloneInt64Map(snapshot.RequestsByHour)
	cloned.TokensByDay = cloneInt64Map(snapshot.TokensByDay)
	cloned.TokensByHour = cloneInt64Map(snapshot.TokensByHour)
	cloned.CostByDay = cloneFloat64Map(snapshot.CostByDay)
	cloned.CostByHour = cloneFloat64Map(snapshot.CostByHour)
	return cloned
}

func cloneModelStats(stats []ModelStat) []ModelStat {
	cloned := make([]ModelStat, len(stats))
	for i, stat := range stats {
		cloned[i] = stat
		cloned[i].Providers = cloneModelProviderStats(stat.Providers)
	}
	return cloned
}

func cloneClientAPIModelStats(stats []ClientAPIModelStat) []ClientAPIModelStat {
	cloned := make([]ClientAPIModelStat, len(stats))
	for i, stat := range stats {
		cloned[i] = stat
		cloned[i].Providers = cloneModelProviderStats(stat.Providers)
	}
	return cloned
}

func cloneModelProviderStats(stats []ModelProviderStat) []ModelProviderStat {
	return append([]ModelProviderStat(nil), stats...)
}

func cloneInt64Map(values map[string]int64) map[string]int64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int64, len(values))
	for k, v := range values {
		cloned[k] = v
	}
	return cloned
}

func cloneFloat64Map(values map[string]float64) map[string]float64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]float64, len(values))
	for k, v := range values {
		cloned[k] = v
	}
	return cloned
}

func timeSeriesTokenStatsByDaySnapshot(values map[string]map[string]*TimeSeriesTokenStat) map[string][]TimeSeriesTokenStat {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string][]TimeSeriesTokenStat, len(values))
	for bucket, stats := range values {
		result[bucket] = timeSeriesTokenStatsSnapshot(stats)
	}
	return result
}

func timeSeriesTokenStatsByHourSnapshot(values map[int]map[string]*TimeSeriesTokenStat) map[string][]TimeSeriesTokenStat {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string][]TimeSeriesTokenStat, len(values))
	for hour, stats := range values {
		if hour < 0 || hour >= 24 {
			continue
		}
		result[hourKeys[hour]] = timeSeriesTokenStatsSnapshot(stats)
	}
	return result
}

func timeSeriesTokenStatsSnapshot(values map[string]*TimeSeriesTokenStat) []TimeSeriesTokenStat {
	if len(values) == 0 {
		return nil
	}
	stats := make([]TimeSeriesTokenStat, 0, len(values))
	for _, stat := range values {
		if stat == nil {
			continue
		}
		stats = append(stats, *stat)
	}
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Model != stats[j].Model {
			return stats[i].Model < stats[j].Model
		}
		return stats[i].Provider < stats[j].Provider
	})
	return stats
}

func (s *RequestStatistics) buildSummaryWithoutDetailsLocked(now time.Time, healthWindow time.Time) DashboardSummary {
	summary := DashboardSummary{}
	summary.Usage.TotalRequests = s.totalRequests
	summary.Usage.SuccessCount = s.successCount
	summary.Usage.FailureCount = s.failureCount
	summary.Usage.TotalTokens = s.totalTokens
	summary.Usage.InputTokens = s.inputTokens
	summary.Usage.OutputTokens = s.outputTokens
	summary.Usage.CachedTokens = s.cachedTokens
	summary.Usage.ReasoningTokens = s.reasoningTokens
	if s.latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(s.latencySum) / float64(s.latencyN)
	}

	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(s.apis))

	healthStart := healthWindow.Add(-dashboardHealthSlotCount * dashboardHealthStep)

	for apiName, apiSt := range s.apis {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests:   apiSt.TotalRequests,
			SuccessCount:    apiSt.SuccessCount,
			FailureCount:    apiSt.FailureCount,
			TotalTokens:     apiSt.TotalTokens,
			InputTokens:     apiSt.InputTokens,
			OutputTokens:    apiSt.OutputTokens,
			CachedTokens:    apiSt.CachedTokens,
			ReasoningTokens: apiSt.ReasoningTokens,
			Models:          make(map[string]ModelSnapshotWithoutDetails, len(apiSt.Models)),
		}
		if apiSt.latencyN > 0 {
			apiSnap.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
		}

		for modelName, modelSt := range apiSt.Models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests:   modelSt.TotalRequests,
				SuccessCount:    modelSt.SuccessCount,
				FailureCount:    modelSt.FailureCount,
				TotalTokens:     modelSt.TotalTokens,
				InputTokens:     modelSt.InputTokens,
				OutputTokens:    modelSt.OutputTokens,
				CachedTokens:    modelSt.CachedTokens,
				ReasoningTokens: modelSt.ReasoningTokens,
				Providers:       finalizedModelProviderStats(modelSt.providerStats, modelSt.TotalRequests, modelSt.SuccessCount, modelSt.FailureCount, modelSt.TotalTokens, modelSt.InputTokens, modelSt.OutputTokens, modelSt.CachedTokens, modelSt.ReasoningTokens),
			}
			if modelSt.latencyN > 0 {
				modelSnap.AvgLatencyMs = float64(modelSt.latencySum) / float64(modelSt.latencyN)
			}

			apiSnap.Models[modelName] = modelSnap
		}
		summary.Usage.APIs[apiName] = apiSnap
	}

	// Finalize model average latencies from accumulated sums.
	summary.ModelStats = make([]ModelStat, 0, len(s.modelSummaryStats))
	for _, m := range s.modelSummaryStats {
		summary.ModelStats = append(summary.ModelStats, finalizeModelStat(*m))
	}
	sort.SliceStable(summary.ModelStats, func(i, j int) bool {
		return summary.ModelStats[i].TotalRequests > summary.ModelStats[j].TotalRequests
	})

	// Build source stats sorted by requests
	summary.SourceStats = make([]SourceStat, 0, len(s.sourceStats))
	for _, sr := range s.sourceStats {
		summary.SourceStats = append(summary.SourceStats, sr.stat)
	}
	sort.SliceStable(summary.SourceStats, func(i, j int) bool {
		return summary.SourceStats[i].TotalRequests > summary.SourceStats[j].TotalRequests
	})

	// Build credential stats sorted by requests
	summary.CredentialStats = make([]CredentialStat, 0, len(s.credentialStats))
	for _, cr := range s.credentialStats {
		summary.CredentialStats = append(summary.CredentialStats, *cr)
	}
	sort.SliceStable(summary.CredentialStats, func(i, j int) bool {
		return summary.CredentialStats[i].TotalRequests > summary.CredentialStats[j].TotalRequests
	})

	summary.ClientAPIStats = make([]ClientAPIStat, 0, len(s.clientAPIStats))
	for _, agg := range s.clientAPIStats {
		stat := agg.stat
		stat.Models = make([]ClientAPIModelStat, 0, len(agg.models))
		for _, model := range agg.models {
			stat.Models = append(stat.Models, finalizeClientAPIModelStat(*model))
		}
		sort.SliceStable(stat.Models, func(i, j int) bool {
			return stat.Models[i].TotalRequests > stat.Models[j].TotalRequests
		})
		summary.ClientAPIStats = append(summary.ClientAPIStats, stat)
	}
	sort.SliceStable(summary.ClientAPIStats, func(i, j int) bool {
		return summary.ClientAPIStats[i].TotalRequests > summary.ClientAPIStats[j].TotalRequests
	})

	// Build health grid
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
	summary.Usage.CostByDay = make(map[string]float64, len(s.costByDay))
	for k, v := range s.costByDay {
		summary.Usage.CostByDay[k] = v
	}
	summary.Usage.CostByHour = make(map[string]float64, 24)
	for hour, v := range s.costByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.CostByHour[hourKeys[hour]] = v
		}
	}

	// Metadata
	summary.Meta.RetentionDays = int(s.retention.Hours() / 24)
	summary.Meta.MaxDetailsPerModel = s.maxDetailsPerModel
	summary.Meta.CurrentDetailCount = s.countDetailsLocked()
	summary.Meta.EvictedTotal = s.evictedTotal
	summary.Meta.SummaryVersion = s.summaryVersion
	summary.Meta.Storage = s.storageStatusLocked()
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

	summary.GeneratedAt = now.UTC().Format(time.RFC3339)
	return summary
}

// buildSummaryWithoutDetailsForRangeLocked scans all events within the cutoff window
// and builds a fresh DashboardSummary. Caller must hold s.mu.
func (s *RequestStatistics) buildSummaryWithoutDetailsForRangeLocked(now time.Time, healthWindow time.Time, cutoff time.Time) DashboardSummary {
	summary := DashboardSummary{}

	// Usage accumulators
	var totalRequests, successCount, failureCount int64
	var totalTokens, inputTokens, outputTokens, cachedTokens, reasoningTokens int64
	var latencySum, latencyN int64

	requestsByDay := make(map[string]int64)
	requestsByHour := make(map[int]int64)
	tokensByDay := make(map[string]int64)
	tokensByHour := make(map[int]int64)
	costByDay := make(map[string]float64)
	costByHour := make(map[int]float64)

	// Dimension aggregators
	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*sourceStatAccumulator)
	credentialAgg := make(map[string]*CredentialStat)
	clientAPIAgg := make(map[string]*clientAPIStatAccumulator)
	apiAgg := make(map[string]*apiRangeAgg)

	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				if !cutoff.IsZero() && (detail.Timestamp.IsZero() || detail.Timestamp.Before(cutoff)) {
					continue
				}
				totals := detailTotalsFromRequest(detail)
				dModel := detailModel(modelName, detail)

				// Global usage
				totalRequests++
				if detail.Failed {
					failureCount++
				} else {
					successCount++
				}
				totalTokens += totals.totalTokens
				inputTokens += totals.inputTokens
				outputTokens += totals.outputTokens
				cachedTokens += totals.cachedTokens
				reasoningTokens += totals.reasoningTokens
				if detail.LatencyMs > 0 {
					latencySum += detail.LatencyMs
					latencyN++
				}

				// Day/hour time series
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := detail.Timestamp.Hour()
				cost := s.detailCostLocked(modelName, detail, totals)
				requestsByDay[dayKey]++
				requestsByHour[hourKey]++
				tokensByDay[dayKey] += totals.totalTokens
				tokensByHour[hourKey] += totals.totalTokens
				costByDay[dayKey] += cost
				costByHour[hourKey] += cost

				// Per-API aggregation
				api := getOrCreateAPIRangeAgg(apiAgg, apiName)
				api.TotalRequests++
				if detail.Failed {
					api.FailureCount++
				} else {
					api.SuccessCount++
				}
				api.TotalTokens += totals.totalTokens
				api.InputTokens += totals.inputTokens
				api.OutputTokens += totals.outputTokens
				api.CachedTokens += totals.cachedTokens
				api.ReasoningTokens += totals.reasoningTokens
				if detail.LatencyMs > 0 {
					api.latencySum += detail.LatencyMs
					api.latencyN++
				}
				rangeIncrementAPIModel(api, dModel, detail, totals)

				// Model summary stats
				ms, ok := modelAgg[dModel]
				if !ok {
					ms = &ModelStat{Model: dModel}
					modelAgg[dModel] = ms
				}
				ms.TotalRequests++
				if detail.Failed {
					ms.FailureCount++
				} else {
					ms.SuccessCount++
				}
				ms.TotalTokens += totals.totalTokens
				ms.InputTokens += totals.inputTokens
				ms.OutputTokens += totals.outputTokens
				ms.CachedTokens += totals.cachedTokens
				ms.ReasoningTokens += totals.reasoningTokens
				ms.providerStats = incrementModelProviderStats(ms.providerStats, detail.Provider, detail.Failed, totals)
				if detail.LatencyMs > 0 {
					ms.latencySum += detail.LatencyMs
					ms.latencyN++
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
				src.stat.TotalRequests++
				if detail.Failed {
					src.stat.FailureCount++
				} else {
					src.stat.SuccessCount++
				}
				src.stat.TotalTokens += totals.totalTokens

				// Credential stats
				credKey := summaryCredentialKey(detail)
				cred, ok := credentialAgg[credKey]
				if !ok {
					cred = &CredentialStat{AuthIndex: credKey}
					credentialAgg[credKey] = cred
				}
				cred.TotalRequests++
				if detail.Failed {
					cred.FailureCount++
				} else {
					cred.SuccessCount++
				}
				cred.TotalTokens += totals.totalTokens

				// Client API stats
				clientKey := clientAPIGroupKey(detail)
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
				client.stat.TotalRequests++
				if detail.Failed {
					client.stat.FailureCount++
				} else {
					client.stat.SuccessCount++
				}
				client.stat.TotalTokens += totals.totalTokens
				client.stat.InputTokens += totals.inputTokens
				client.stat.OutputTokens += totals.outputTokens
				client.stat.CachedTokens += totals.cachedTokens
				client.stat.ReasoningTokens += totals.reasoningTokens
				rangeIncrementClientModel(client, dModel, detail, totals)
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
	summary.Usage.ReasoningTokens = reasoningTokens
	if latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(latencySum) / float64(latencyN)
	}

	// Build API snapshots
	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(apiAgg))
	for apiName, api := range apiAgg {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests:   api.TotalRequests,
			SuccessCount:    api.SuccessCount,
			FailureCount:    api.FailureCount,
			TotalTokens:     api.TotalTokens,
			InputTokens:     api.InputTokens,
			OutputTokens:    api.OutputTokens,
			CachedTokens:    api.CachedTokens,
			ReasoningTokens: api.ReasoningTokens,
			Models:          make(map[string]ModelSnapshotWithoutDetails, len(api.models)),
		}
		if api.latencyN > 0 {
			apiSnap.AvgLatencyMs = float64(api.latencySum) / float64(api.latencyN)
		}
		for mName, m := range api.models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests:   m.TotalRequests,
				SuccessCount:    m.SuccessCount,
				FailureCount:    m.FailureCount,
				TotalTokens:     m.TotalTokens,
				InputTokens:     m.InputTokens,
				OutputTokens:    m.OutputTokens,
				CachedTokens:    m.CachedTokens,
				ReasoningTokens: m.ReasoningTokens,
				Providers:       finalizedModelProviderStats(m.providerStats, m.TotalRequests, m.SuccessCount, m.FailureCount, m.TotalTokens, m.InputTokens, m.OutputTokens, m.CachedTokens, m.ReasoningTokens),
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
	summary.ClientAPIStats = make([]ClientAPIStat, 0, len(clientAPIAgg))
	for _, agg := range clientAPIAgg {
		stat := agg.stat
		stat.Models = make([]ClientAPIModelStat, 0, len(agg.models))
		for _, model := range agg.models {
			stat.Models = append(stat.Models, finalizeClientAPIModelStat(*model))
		}
		sort.SliceStable(stat.Models, func(i, j int) bool {
			return stat.Models[i].TotalRequests > stat.Models[j].TotalRequests
		})
		summary.ClientAPIStats = append(summary.ClientAPIStats, stat)
	}
	sort.SliceStable(summary.ClientAPIStats, func(i, j int) bool {
		return summary.ClientAPIStats[i].TotalRequests > summary.ClientAPIStats[j].TotalRequests
	})

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

	// Time series
	summary.Usage.RequestsByDay = make(map[string]int64, len(requestsByDay))
	for k, v := range requestsByDay {
		summary.Usage.RequestsByDay[k] = v
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	for hour, v := range requestsByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.RequestsByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.TokensByDay = make(map[string]int64, len(tokensByDay))
	for k, v := range tokensByDay {
		summary.Usage.TokensByDay[k] = v
	}
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	for hour, v := range tokensByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.TokensByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.CostByDay = make(map[string]float64, len(costByDay))
	for k, v := range costByDay {
		summary.Usage.CostByDay[k] = v
	}
	summary.Usage.CostByHour = make(map[string]float64, 24)
	for hour, v := range costByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.CostByHour[hourKeys[hour]] = v
		}
	}

	// Metadata (uses global counters, not range-scoped).
	summary.Meta.RetentionDays = int(s.retention.Hours() / 24)
	summary.Meta.MaxDetailsPerModel = s.maxDetailsPerModel
	summary.Meta.CurrentDetailCount = s.countDetailsLocked()
	summary.Meta.EvictedTotal = s.evictedTotal
	summary.Meta.SummaryVersion = s.summaryVersion
	summary.Meta.Storage = s.storageStatusLocked()
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

	summary.GeneratedAt = now.UTC().Format(time.RFC3339)
	return summary
}

// apiRangeAgg and modelRangeAgg are lightweight accumulators used during
// range-scoped summary construction.
type apiRangeAgg struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	models          map[string]*modelRangeAgg
}

type modelRangeAgg struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	providerStats   map[string]*ModelProviderStat
}

func getOrCreateAPIRangeAgg(apiAgg map[string]*apiRangeAgg, apiName string) *apiRangeAgg {
	a, ok := apiAgg[apiName]
	if !ok {
		a = &apiRangeAgg{models: make(map[string]*modelRangeAgg)}
		apiAgg[apiName] = a
	}
	return a
}

func rangeIncrementAPIModel(api *apiRangeAgg, modelName string, detail RequestDetail, totals detailTotals) {
	m, ok := api.models[modelName]
	if !ok {
		m = &modelRangeAgg{}
		api.models[modelName] = m
	}
	m.TotalRequests++
	if detail.Failed {
		m.FailureCount++
	} else {
		m.SuccessCount++
	}
	m.TotalTokens += totals.totalTokens
	m.InputTokens += totals.inputTokens
	m.OutputTokens += totals.outputTokens
	m.CachedTokens += totals.cachedTokens
	m.ReasoningTokens += totals.reasoningTokens
	m.providerStats = incrementModelProviderStats(m.providerStats, detail.Provider, detail.Failed, totals)
	if detail.LatencyMs > 0 {
		m.latencySum += detail.LatencyMs
		m.latencyN++
	}
}

func rangeIncrementClientModel(client *clientAPIStatAccumulator, modelName string, detail RequestDetail, totals detailTotals) {
	cm, ok := client.models[modelName]
	if !ok {
		cm = &ClientAPIModelStat{Model: modelName}
		client.models[modelName] = cm
	}
	cm.TotalRequests++
	if detail.Failed {
		cm.FailureCount++
	} else {
		cm.SuccessCount++
	}
	cm.TotalTokens += totals.totalTokens
	cm.InputTokens += totals.inputTokens
	cm.OutputTokens += totals.outputTokens
	cm.CachedTokens += totals.cachedTokens
	cm.ReasoningTokens += totals.reasoningTokens
	cm.providerStats = incrementModelProviderStats(cm.providerStats, detail.Provider, detail.Failed, totals)
}

func detailModel(modelName string, detail RequestDetail) string {
	if detail.Model != "" {
		return detail.Model
	}
	return modelName
}

func clientAPIGroupLabel(detail RequestDetail) string {
	label := strings.TrimSpace(detail.APIKey)
	if label == "" {
		return "未知 API"
	}
	return label
}

func clientAPIGroupKey(detail RequestDetail) string {
	label := strings.TrimSpace(detail.APIKey)
	if label != "" {
		return "api_key:" + label
	}
	hash := strings.TrimSpace(detail.APIKeyHash)
	if hash != "" {
		return "api_key_hash:" + hash
	}
	return "(unknown)"
}

func dashboardRangeCutoff(rangeKey string, now time.Time) time.Time {
	switch rangeKey {
	case "7h":
		return now.Add(-7 * time.Hour)
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

type dashboardEventDetail struct {
	detail    *RequestDetail
	sortKey   string
	modelName string
	sequence  int64
}

func (d dashboardEventDetail) requestDetail() RequestDetail {
	if d.detail == nil {
		return RequestDetail{}
	}
	return *d.detail
}

func (d dashboardEventDetail) timestamp() time.Time {
	if d.detail == nil {
		return time.Time{}
	}
	return d.detail.Timestamp
}

func dashboardEventBefore(a, b dashboardEventDetail) bool {
	at := a.timestamp()
	bt := b.timestamp()
	if !at.Equal(bt) {
		return at.After(bt)
	}
	if a.sortKey != b.sortKey {
		return a.sortKey < b.sortKey
	}
	return a.sequence < b.sequence
}

type dashboardEventHeap []dashboardEventDetail

func (h dashboardEventHeap) Len() int { return len(h) }

func (h dashboardEventHeap) Less(i, j int) bool {
	return dashboardEventBefore(h[j], h[i])
}

func (h dashboardEventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *dashboardEventHeap) Push(x any) {
	*h = append(*h, x.(dashboardEventDetail))
}

func (h *dashboardEventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func appendBoundedDashboardEventHeap(events *dashboardEventHeap, candidate dashboardEventDetail, limit int) {
	if events == nil || limit <= 0 {
		return
	}
	if events.Len() < limit {
		heap.Push(events, candidate)
		return
	}
	if dashboardEventBefore(candidate, (*events)[0]) {
		(*events)[0] = candidate
		heap.Fix(events, 0)
	}
}

func dashboardEventCacheKeyFor(params EventsQuery, now time.Time) dashboardEventCacheKey {
	var timeBucket int64
	if params.Range != "" && params.Range != "all" {
		timeBucket = now.UTC().Unix()
	}
	return dashboardEventCacheKey{
		limit:      params.Limit,
		offset:     params.Offset,
		timeBucket: timeBucket,
		rangeKey:   params.Range,
		model:      params.Model,
		source:     params.Source,
		authIndex:  params.AuthIndex,
		api:        params.API,
	}
}

func cloneEventsResult(result EventsResult, generatedAt time.Time) EventsResult {
	cloned := result
	cloned.Events = cloneRequestDetails(result.Events)
	if !generatedAt.IsZero() {
		cloned.GeneratedAt = generatedAt.UTC().Format(time.RFC3339)
	}
	return cloned
}

func cloneRequestDetails(details []RequestDetail) []RequestDetail {
	if details == nil {
		return nil
	}
	cloned := make([]RequestDetail, len(details))
	for i, detail := range details {
		cloned[i] = detail
		if detail.Headers != nil {
			cloned[i].Headers = cloneHeaders(detail.Headers)
		}
	}
	return cloned
}

func cloneHeaders(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string][]string, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func (s *RequestStatistics) cacheDashboardEventsLocked(key dashboardEventCacheKey, result EventsResult) {
	if s == nil {
		return
	}
	if s.eventQueryCache == nil {
		s.eventQueryCache = make(map[dashboardEventCacheKey]EventsResult)
	}
	if _, exists := s.eventQueryCache[key]; !exists {
		s.eventQueryCacheOrder = append(s.eventQueryCacheOrder, key)
	}
	s.eventQueryCache[key] = cloneEventsResult(result, time.Time{})
	for len(s.eventQueryCacheOrder) > dashboardEventCacheMax {
		evict := s.eventQueryCacheOrder[0]
		s.eventQueryCacheOrder = s.eventQueryCacheOrder[1:]
		delete(s.eventQueryCache, evict)
	}
}

func (s *RequestStatistics) dashboardEventIndexLocked(api string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	if s.eventIndexVersion != s.summaryVersion {
		s.eventIndexVersion = s.summaryVersion
		s.eventIndex = nil
		s.eventAPIIndex = nil
		s.eventModelIndex = nil
		s.eventSourceIndex = nil
		s.eventAuthIndex = nil
	}
	if api != "" {
		if s.eventAPIIndex == nil {
			s.eventAPIIndex = make(map[string][]dashboardEventDetail)
		}
		if events, ok := s.eventAPIIndex[api]; ok {
			return events
		}
		events := buildDashboardEventIndexForAPI(api, s.apis[api])
		s.eventAPIIndex[api] = events
		return events
	}
	if s.eventIndex == nil {
		var events []dashboardEventDetail
		for apiName, apiSt := range s.apis {
			events = appendDashboardEventIndexForAPI(events, apiName, apiSt)
		}
		sort.Slice(events, func(i, j int) bool {
			return dashboardEventBefore(events[i], events[j])
		})
		s.eventIndex = events
	}
	return s.eventIndex
}

func (s *RequestStatistics) dashboardEventQueryIndexLocked(params EventsQuery) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	if params.API != "" {
		return s.dashboardEventIndexLocked(params.API)
	}
	if params.Model != "" {
		return s.dashboardEventModelIndexLocked(params.Model)
	}
	if params.Source != "" {
		return s.dashboardEventSourceIndexLocked(params.Source)
	}
	if params.AuthIndex != "" {
		return s.dashboardEventAuthIndexLocked(params.AuthIndex)
	}
	return s.dashboardEventIndexLocked("")
}

func (s *RequestStatistics) dashboardEventModelIndexLocked(model string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventModelIndex == nil {
		s.eventModelIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventModelIndex[model]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventModelKey(event) == model {
			events = append(events, event)
		}
	}
	s.eventModelIndex[model] = events
	return events
}

func (s *RequestStatistics) dashboardEventSourceIndexLocked(source string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventSourceIndex == nil {
		s.eventSourceIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventSourceIndex[source]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventSourceKey(event) == source {
			events = append(events, event)
		}
	}
	s.eventSourceIndex[source] = events
	return events
}

func (s *RequestStatistics) dashboardEventAuthIndexLocked(authIndex string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventAuthIndex == nil {
		s.eventAuthIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventAuthIndex[authIndex]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventAuthKey(event) == authIndex {
			events = append(events, event)
		}
	}
	s.eventAuthIndex[authIndex] = events
	return events
}

func dashboardEventModelKey(event dashboardEventDetail) string {
	d := event.requestDetail()
	if d.Model != "" {
		return d.Model
	}
	return event.modelName
}

func dashboardEventSourceKey(event dashboardEventDetail) string {
	source := event.requestDetail().Source
	if source == "" {
		return "未知来源"
	}
	return source
}

func dashboardEventAuthKey(event dashboardEventDetail) string {
	return event.requestDetail().AuthIndex
}

func buildDashboardEventIndexForAPI(apiName string, apiSt *apiStats) []dashboardEventDetail {
	events := appendDashboardEventIndexForAPI(nil, apiName, apiSt)
	sort.Slice(events, func(i, j int) bool {
		return dashboardEventBefore(events[i], events[j])
	})
	return events
}

func appendDashboardEventIndexForAPI(events []dashboardEventDetail, apiName string, apiSt *apiStats) []dashboardEventDetail {
	if apiSt == nil {
		return events
	}
	sequence := int64(len(events))
	for modelName, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		for i := range modelSt.Details {
			events = append(events, dashboardEventDetail{detail: &modelSt.Details[i], sortKey: apiName, modelName: modelName, sequence: sequence})
			sequence++
		}
	}
	return events
}

func requestDetailsFromDashboardEvents(events []dashboardEventDetail) []RequestDetail {
	details := make([]RequestDetail, len(events))
	for i, event := range events {
		details[i] = event.requestDetail()
	}
	return details
}

func dashboardEventQueryHasFilters(params EventsQuery) bool {
	return params.Range != "" && params.Range != "all" ||
		params.Model != "" ||
		params.Source != "" ||
		params.AuthIndex != ""
}

func dashboardEventPastCutoff(d RequestDetail, cutoff time.Time) bool {
	if cutoff.IsZero() {
		return false
	}
	return d.Timestamp.IsZero() || d.Timestamp.Before(cutoff)
}

func dashboardEventMatches(d RequestDetail, params EventsQuery, cutoff time.Time) bool {
	if dashboardEventPastCutoff(d, cutoff) {
		return false
	}
	if params.Model != "" && d.Model != params.Model {
		return false
	}
	if params.Source != "" {
		src := d.Source
		if src == "" {
			src = "未知来源"
		}
		if src != params.Source {
			return false
		}
	}
	if params.AuthIndex != "" && d.AuthIndex != params.AuthIndex {
		return false
	}
	return true
}

// QueryEvents returns paginated, filtered event details.
func (s *RequestStatistics) QueryEvents(params EventsQuery) EventsResult {
	return s.queryEvents(params, true, 0)
}

// QueryAllEvents returns every matching event for backend-generated exports.
func (s *RequestStatistics) QueryAllEvents(params EventsQuery) EventsResult {
	return s.queryEvents(params, false, 0)
}

// QueryExportEvents returns matching events up to maxRecords while still
// counting the full match total for capped backend-generated exports.
func (s *RequestStatistics) QueryExportEvents(params EventsQuery, maxRecords int) EventsResult {
	return s.queryEvents(params, false, maxRecords)
}

// QueryExportEventsPage returns one page of exportable events while still
// counting the full match total. snapshotAt freezes the upper time bound so
// background exports do not shift when new requests arrive while paging.
func (s *RequestStatistics) QueryExportEventsPage(params EventsQuery, offset int, pageLimit int, maxRecords int, snapshotAt time.Time) EventsResult {
	if s == nil {
		return EventsResult{}
	}
	if offset < 0 {
		offset = 0
	}
	if pageLimit <= 0 {
		pageLimit = 1000
	}
	startedAt := time.Now()
	params = normalizeEventsQuery(params, false)
	if snapshotAt.IsZero() {
		snapshotAt = startedAt
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := dashboardRangeCutoff(params.Range, snapshotAt)
	index := s.dashboardEventQueryIndexLocked(params)
	events := make([]RequestDetail, 0, pageLimit)
	total := 0
	for _, dm := range index {
		d := dm.requestDetail()
		if !snapshotAt.IsZero() && !d.Timestamp.IsZero() && d.Timestamp.After(snapshotAt) {
			continue
		}
		if dashboardEventPastCutoff(d, cutoff) {
			break
		}
		if !dashboardEventMatches(d, params, cutoff) {
			continue
		}
		matchOffset := total
		if (maxRecords <= 0 || matchOffset < maxRecords) && matchOffset >= offset && len(events) < pageLimit {
			events = append(events, d)
		}
		total++
	}
	limit := exportResultLimit(total, maxRecords)
	if events == nil {
		events = []RequestDetail{}
	}
	result := EventsResult{
		Events:      events,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		Truncated:   maxRecords > 0 && total > maxRecords,
		GeneratedAt: snapshotAt.UTC().Format(time.RFC3339),
	}
	s.lastEventsQueryDuration = time.Since(startedAt)
	s.lastEventsQueryTotal = total
	return result
}

func normalizeEventsQuery(params EventsQuery, paginate bool) EventsQuery {
	if paginate {
		if params.Limit <= 0 || params.Limit > 500 {
			params.Limit = 50
		}
		if params.Offset < 0 {
			params.Offset = 0
		}
		return params
	}
	params.Limit = 0
	params.Offset = 0
	return params
}

func (s *RequestStatistics) queryEvents(params EventsQuery, paginate bool, exportLimit int) EventsResult {
	if s == nil {
		return EventsResult{}
	}
	startedAt := time.Now()

	params = normalizeEventsQuery(params, paginate)

	s.mu.Lock()
	defer s.mu.Unlock()
	finish := func(result EventsResult) EventsResult {
		s.lastEventsQueryDuration = time.Since(startedAt)
		s.lastEventsQueryTotal = result.Total
		return result
	}

	now := time.Now()
	cutoff := dashboardRangeCutoff(params.Range, now)
	var cacheKey dashboardEventCacheKey
	if paginate {
		cacheKey = dashboardEventCacheKeyFor(params, now)
		if cached, ok := s.eventQueryCache[cacheKey]; ok {
			s.eventCacheHits++
			return finish(cloneEventsResult(cached, now))
		}
		s.eventCacheMisses++
	}

	index := s.dashboardEventQueryIndexLocked(params)
	if !dashboardEventQueryHasFilters(params) {
		total := len(index)
		if !paginate {
			eventsIndex := index
			limit := total
			truncated := false
			if exportLimit > 0 && total > exportLimit {
				eventsIndex = index[:exportLimit]
				limit = exportLimit
				truncated = true
			}
			return finish(EventsResult{
				Events:      requestDetailsFromDashboardEvents(eventsIndex),
				Total:       total,
				Limit:       limit,
				Offset:      0,
				Truncated:   truncated,
				GeneratedAt: now.UTC().Format(time.RFC3339),
			})
		}
		if params.Offset >= total {
			result := EventsResult{
				Events:      []RequestDetail{},
				Total:       total,
				Limit:       params.Limit,
				Offset:      params.Offset,
				GeneratedAt: now.UTC().Format(time.RFC3339),
			}
			s.cacheDashboardEventsLocked(cacheKey, result)
			return finish(result)
		}
		end := params.Offset + params.Limit
		if end > total {
			end = total
		}
		result := EventsResult{
			Events:      requestDetailsFromDashboardEvents(index[params.Offset:end]),
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return finish(result)
	}

	var events []RequestDetail
	total := 0
	for _, dm := range index {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(d, cutoff) {
			break
		}
		if !dashboardEventMatches(d, params, cutoff) {
			continue
		}
		if !paginate {
			if exportLimit <= 0 || len(events) < exportLimit {
				events = append(events, d)
			}
		} else if total >= params.Offset && len(events) < params.Limit {
			events = append(events, d)
		}
		total++
	}

	if !paginate {
		if events == nil {
			events = []RequestDetail{}
		}
		return finish(EventsResult{
			Events:      events,
			Total:       total,
			Limit:       exportResultLimit(total, exportLimit),
			Offset:      0,
			Truncated:   exportLimit > 0 && total > exportLimit,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		})
	}

	if params.Offset >= total {
		result := EventsResult{
			Events:      []RequestDetail{},
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return finish(result)
	}

	result := EventsResult{
		Events:      events,
		Total:       total,
		Limit:       params.Limit,
		Offset:      params.Offset,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}
	s.cacheDashboardEventsLocked(cacheKey, result)
	return finish(result)
}

func exportResultLimit(total int, exportLimit int) int {
	if exportLimit > 0 && total > exportLimit {
		return exportLimit
	}
	return total
}

// QueryAPIDetail returns range-scoped aggregates and recent events for one API
// without making the browser page through every matching event.
func (s *RequestStatistics) QueryAPIDetail(api string, rangeKey string, recentLimit int, errorLimit int) APIDetailResponse {
	startedAt := time.Now()
	result := APIDetailResponse{
		API:         api,
		GeneratedAt: startedAt.UTC().Format(time.RFC3339),
	}
	if s == nil {
		return result
	}
	if recentLimit <= 0 || recentLimit > 500 {
		recentLimit = 120
	}
	if errorLimit <= 0 || errorLimit > 100 {
		errorLimit = 20
	}

	now := time.Now()
	cutoff := dashboardRangeCutoff(rangeKey, now)

	s.mu.Lock()
	defer s.mu.Unlock()
	finish := func(result APIDetailResponse) APIDetailResponse {
		s.apiDetailQueries++
		s.lastAPIDetailDuration = time.Since(startedAt)
		s.lastAPIDetailTotal = result.TotalEvents
		return result
	}

	apiSt := s.apis[api]
	aggregateScope := cutoff.IsZero()
	if aggregateScope {
		result.Summary = apiDetailSummaryFromAPIStats(apiSt)
		result.ModelStats = apiDetailModelStatsFromAPIStats(apiSt)
		result.SourceStats = apiDetailSourceStatsFromAPIStats(apiSt)
		result.TotalEvents = nonNegativeIntFromInt64(result.Summary.TotalRequests)
	}

	index := s.dashboardEventIndexLocked(api)
	if len(index) == 0 {
		result.GeneratedAt = now.UTC().Format(time.RFC3339)
		return finish(result)
	}

	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*SourceStat)
	errorAgg := make(map[string]*APIDetailErrorStat)
	recentEvents := dashboardEventHeap{}
	heap.Init(&recentEvents)
	var latencySum int64
	var latencyN int64
	sequence := int64(0)

	for _, dm := range index {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(d, cutoff) {
			break
		}
		totalTokens := detailTotalTokens(d.Tokens)
		inputTokens := nonNegativeInt64(d.Tokens.InputTokens)
		outputTokens := nonNegativeInt64(d.Tokens.OutputTokens)
		reasoningTokens := nonNegativeInt64(d.Tokens.ReasoningTokens)
		cachedTokens := normalizedCacheTokens(d.Tokens)

		if !aggregateScope {
			result.TotalEvents++
			result.Summary.TotalRequests++
			if d.Failed {
				result.Summary.FailureCount++
			} else {
				result.Summary.SuccessCount++
			}
			result.Summary.TotalTokens += totalTokens
			result.Summary.InputTokens += inputTokens
			result.Summary.OutputTokens += outputTokens
			result.Summary.CachedTokens += cachedTokens
			result.Summary.ReasoningTokens += reasoningTokens
			if d.LatencyMs > 0 {
				latencySum += d.LatencyMs
				latencyN++
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
			ms.TotalRequests++
			if d.Failed {
				ms.FailureCount++
			} else {
				ms.SuccessCount++
			}
			ms.TotalTokens += totalTokens
			ms.InputTokens += inputTokens
			ms.OutputTokens += outputTokens
			ms.CachedTokens += cachedTokens
			ms.ReasoningTokens += reasoningTokens
			ms.providerStats = incrementModelProviderStats(ms.providerStats, d.Provider, d.Failed, detailTotals{
				totalTokens:     totalTokens,
				inputTokens:     inputTokens,
				outputTokens:    outputTokens,
				cachedTokens:    cachedTokens,
				reasoningTokens: reasoningTokens,
			})
			if d.LatencyMs > 0 {
				ms.latencySum += d.LatencyMs
				ms.latencyN++
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
			ss.TotalRequests++
			if d.Failed {
				ss.FailureCount++
			} else {
				ss.SuccessCount++
			}
			ss.TotalTokens += totalTokens
		}

		if d.Failed {
			failure := strings.TrimSpace(d.Failure)
			if failure == "" {
				failure = "未返回错误内容"
			}
			key := fmt.Sprintf("%d|%s", d.StatusCode, failure)
			es, ok := errorAgg[key]
			if !ok {
				es = &APIDetailErrorStat{StatusCode: d.StatusCode, Failure: failure}
				errorAgg[key] = es
			}
			es.Count++
		}

		appendBoundedDashboardEventHeap(&recentEvents, dashboardEventDetail{detail: dm.detail, sortKey: d.Model, sequence: sequence}, recentLimit)
		sequence++
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
		result.RecentEvents[i] = dm.requestDetail()
	}
	result.GeneratedAt = now.UTC().Format(time.RFC3339)
	return finish(result)
}

func apiDetailSummaryFromAPIStats(apiSt *apiStats) APIDetailSummary {
	if apiSt == nil {
		return APIDetailSummary{}
	}
	summary := APIDetailSummary{
		TotalRequests:   apiSt.TotalRequests,
		SuccessCount:    apiSt.SuccessCount,
		FailureCount:    apiSt.FailureCount,
		TotalTokens:     apiSt.TotalTokens,
		InputTokens:     apiSt.InputTokens,
		OutputTokens:    apiSt.OutputTokens,
		CachedTokens:    apiSt.CachedTokens,
		ReasoningTokens: apiSt.ReasoningTokens,
	}
	if apiSt.latencyN > 0 {
		summary.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
	}
	return summary
}

func apiDetailModelStatsFromAPIStats(apiSt *apiStats) []ModelStat {
	if apiSt == nil || len(apiSt.Models) == 0 {
		return nil
	}
	stats := make([]ModelStat, 0, len(apiSt.Models))
	for modelName, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		stat := ModelStat{
			Model:           modelName,
			TotalRequests:   modelSt.TotalRequests,
			SuccessCount:    modelSt.SuccessCount,
			FailureCount:    modelSt.FailureCount,
			TotalTokens:     modelSt.TotalTokens,
			InputTokens:     modelSt.InputTokens,
			OutputTokens:    modelSt.OutputTokens,
			CachedTokens:    modelSt.CachedTokens,
			ReasoningTokens: modelSt.ReasoningTokens,
			Providers:       finalizedModelProviderStats(modelSt.providerStats, modelSt.TotalRequests, modelSt.SuccessCount, modelSt.FailureCount, modelSt.TotalTokens, modelSt.InputTokens, modelSt.OutputTokens, modelSt.CachedTokens, modelSt.ReasoningTokens),
		}
		if modelSt.latencyN > 0 {
			stat.AvgLatencyMs = float64(modelSt.latencySum) / float64(modelSt.latencyN)
		}
		stats = append(stats, stat)
	}
	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
	return stats
}

func apiDetailSourceStatsFromAPIStats(apiSt *apiStats) []SourceStat {
	if apiSt == nil || len(apiSt.Sources) == 0 {
		return nil
	}
	stats := make([]SourceStat, 0, len(apiSt.Sources))
	for _, sourceAgg := range apiSt.Sources {
		if sourceAgg == nil {
			continue
		}
		stats = append(stats, sourceAgg.stat)
	}
	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
	return stats
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

func (s *RequestStatistics) DashboardVersion() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryVersion
}

func (s *RequestStatistics) Close() {
	if s == nil {
		return
	}
	s.stopStorageWorker()
	s.stopModelsDevPriceWorker()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeStorageLocked()
}

func (s *RequestStatistics) ConfigSnapshot() ExportConfig {
	if s == nil {
		return ExportConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ExportConfig{
		RetentionDays:                 int(s.retention.Hours() / 24),
		MaxDetailsPerModel:            s.maxDetailsPerModel,
		DedupWindowMinutes:            int(s.dedupWindow.Minutes()),
		LogResponseHeaders:            s.logResponseHeaders.String(),
		StorageEnabled:                s.storageEnabled,
		StoragePath:                   s.storagePath,
		StorageFlushSeconds:           int(s.storageFlush.Seconds()),
		StorageSnapshotSeconds:        int(s.storageSnapshotInterval.Seconds()),
		StorageSnapshotRecordInterval: s.storageSnapshotRecordInterval,
		StorageSyncSeconds:            int(s.storageSyncInterval.Seconds()),
		StorageSyncRecordInterval:     s.storageSyncRecordInterval,
		ExportMaxRecords:              s.exportMaxRecords,
		PriceStoragePath:              s.priceStoragePath,
		ModelsDevPricesEnabled:        s.modelsDevPricesEnabled,
		ModelsDevPricesURL:            s.modelsDevPricesURL,
		ModelsDevRefreshSeconds:       int(s.modelsDevRefresh.Seconds()),
	}
}

func (s *RequestStatistics) ExportMaxRecords() int {
	if s == nil {
		return defaultExportMaxRecords
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.exportMaxRecords
}

func (s *RequestStatistics) StorageStatus() StorageStatus {
	if s == nil {
		return StorageStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageStatusLocked()
}

func (s *RequestStatistics) storageStatusLocked() StorageStatus {
	queueLength, queueCapacity := 0, 0
	writePressure := ""
	if s.storageEnabled {
		queueLength = s.storageWriteQueueLength
		queueCapacity = s.storageWriteQueueCapacity
		writePressure = storageWritePressure(queueLength, queueCapacity, s.storageWriteQueueWaitAvg)
	}
	status := StorageStatus{
		Enabled:                       s.storageEnabled,
		Path:                          s.storagePath,
		LoadedPath:                    s.storageLoadedPath,
		LastError:                     s.storageLastError,
		PendingBufferedRecords:        s.storageBuffered,
		PendingSnapshotRecords:        s.storageSnapshotRecords,
		PendingUnsyncedRecords:        s.storageUnsyncedRecords,
		WriteQueueLength:              queueLength,
		WriteQueueCapacity:            queueCapacity,
		LastWriteBatchRecords:         s.storageLastWriteBatchRecords,
		LastWriteBatchDurationMs:      storageDurationMilliseconds(s.storageLastWriteBatchDuration),
		LastWriteQueueWaitMs:          storageDurationMilliseconds(s.storageLastWriteQueueWait),
		WriteBatchesTotal:             s.storageWriteBatchesTotal,
		WriteRecordsTotal:             s.storageWriteRecordsTotal,
		WriteBatchAvgDurationMs:       storageDurationMilliseconds(s.storageWriteBatchDurationAvg),
		WriteBatchP95DurationMs:       storageDurationMilliseconds(storageDurationPercentile(s.storageWriteBatchDurations, 0.95)),
		WriteBatchP99DurationMs:       storageDurationMilliseconds(storageDurationPercentile(s.storageWriteBatchDurations, 0.99)),
		WriteQueueWaitAvgMs:           storageDurationMilliseconds(s.storageWriteQueueWaitAvg),
		WriteQueueWaitP95Ms:           storageDurationMilliseconds(storageDurationPercentile(s.storageWriteQueueWaits, 0.95)),
		WriteQueueWaitP99Ms:           storageDurationMilliseconds(storageDurationPercentile(s.storageWriteQueueWaits, 0.99)),
		WriteQueueWaitMaxMs:           storageDurationMilliseconds(s.storageWriteQueueWaitMax),
		WritePressure:                 writePressure,
		LastCompactedShards:           s.storageLastCompactedShards,
		CompactedShardsTotal:          s.storageCompactedShardsTotal,
		SnapshotIntervalSeconds:       int(s.storageSnapshotInterval.Seconds()),
		SnapshotRecordIntervalRecords: s.storageSnapshotRecordInterval,
		SyncIntervalSeconds:           int(s.storageSyncInterval.Seconds()),
		SyncRecordIntervalRecords:     s.storageSyncRecordInterval,
	}
	if !s.storageLastFlush.IsZero() {
		status.LastFlushAt = s.storageLastFlush.UTC().Format(time.RFC3339)
	}
	if !s.storageLastSnapshot.IsZero() {
		status.LastSnapshotAt = s.storageLastSnapshot.UTC().Format(time.RFC3339)
	}
	if !s.storageLastCompaction.IsZero() {
		status.LastCompactionAt = s.storageLastCompaction.UTC().Format(time.RFC3339)
	}
	if !s.storageLastSync.IsZero() {
		status.LastSyncAt = s.storageLastSync.UTC().Format(time.RFC3339)
	}
	return status
}

func storageWritePressure(queueLength int, queueCapacity int, avgWait time.Duration) string {
	if queueCapacity > 0 && queueLength >= queueCapacity {
		return "full"
	}
	if queueCapacity > 0 && queueLength*4 >= queueCapacity {
		return "backlog"
	}
	if queueLength > 0 {
		return "queued"
	}
	if avgWait >= 200*time.Millisecond {
		return "slow"
	}
	return "normal"
}

func storageDurationMilliseconds(duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return float64(duration) / float64(time.Millisecond)
}

func storageDurationPercentile(samples []time.Duration, percentile float64) time.Duration {
	if len(samples) == 0 || percentile <= 0 {
		return 0
	}
	ordered := make([]time.Duration, len(samples))
	copy(ordered, samples)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i] < ordered[j]
	})
	rank := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(ordered) {
		rank = len(ordered) - 1
	}
	return ordered[rank]
}

func (s *RequestStatistics) RecordConditionalRequest(endpoint string, hasValidator bool, notModified bool) {
	if s == nil || !hasValidator {
		return
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "unknown"
	}
	s.mu.Lock()
	if s.conditionalRequests == nil {
		s.conditionalRequests = make(map[string]conditionalRequestCounter)
	}
	counter := s.conditionalRequests[endpoint]
	counter.Requests++
	if notModified {
		counter.NotModified++
	}
	s.conditionalRequests[endpoint] = counter
	s.mu.Unlock()
}

func (s *RequestStatistics) RecordEventsExport(format string, gzipped bool, result EventsResult, rawBytes int, bodyBytes int, duration time.Duration) {
	if s == nil {
		return
	}
	s.RecordEventsExportSummary(format, gzipped, result.Total, len(result.Events), result.Truncated, rawBytes, bodyBytes, duration)
}

func (s *RequestStatistics) RecordEventsExportSummary(format string, gzipped bool, total int, exported int, truncated bool, rawBytes int, bodyBytes int, duration time.Duration) {
	if s == nil {
		return
	}
	if rawBytes < 0 {
		rawBytes = 0
	}
	if bodyBytes < 0 {
		bodyBytes = 0
	}
	if total < 0 {
		total = 0
	}
	if exported < 0 {
		exported = 0
	}
	if duration < 0 {
		duration = 0
	}
	s.mu.Lock()
	s.eventsExportRequests++
	if gzipped {
		s.eventsExportGzipRequests++
	}
	if truncated {
		s.eventsExportTruncatedTotal++
	}
	s.lastEventsExportDuration = duration
	s.lastEventsExportFormat = strings.TrimSpace(format)
	s.lastEventsExportGzip = gzipped
	s.lastEventsExportTotal = total
	s.lastEventsExported = exported
	s.lastEventsExportTruncated = truncated
	s.lastEventsExportRawBytes = rawBytes
	s.lastEventsExportBodyBytes = bodyBytes
	s.mu.Unlock()
}

func (s *RequestStatistics) RuntimeStatus() RuntimeStatus {
	if s == nil {
		return RuntimeStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := RuntimeStatus{
		SeenCount:                  len(s.seen),
		SummaryVersion:             s.summaryVersion,
		SummaryCacheValid:          s.summaryCacheValid && s.summaryCacheVersion == s.summaryVersion,
		SummaryCacheHits:           s.summaryCacheHits,
		SummaryCacheMisses:         s.summaryCacheMisses,
		LastSummaryDurationMs:      durationMilliseconds(s.lastSummaryDuration),
		EventCacheEntries:          len(s.eventQueryCache),
		EventCacheHits:             s.eventCacheHits,
		EventCacheMisses:           s.eventCacheMisses,
		LastEventsQueryDurationMs:  durationMilliseconds(s.lastEventsQueryDuration),
		LastEventsQueryTotal:       s.lastEventsQueryTotal,
		EventIndexVersion:          s.eventIndexVersion,
		EventIndexEntries:          len(s.eventIndex),
		APIDetailQueries:           s.apiDetailQueries,
		LastAPIDetailDurationMs:    durationMilliseconds(s.lastAPIDetailDuration),
		LastAPIDetailTotalEvents:   s.lastAPIDetailTotal,
		EventsExportRequests:       s.eventsExportRequests,
		EventsExportGzipRequests:   s.eventsExportGzipRequests,
		EventsExportTruncatedTotal: s.eventsExportTruncatedTotal,
		LastEventsExportDurationMs: durationMilliseconds(s.lastEventsExportDuration),
		LastEventsExportFormat:     s.lastEventsExportFormat,
		LastEventsExportGzip:       s.lastEventsExportGzip,
		LastEventsExportTotal:      s.lastEventsExportTotal,
		LastEventsExported:         s.lastEventsExported,
		LastEventsExportTruncated:  s.lastEventsExportTruncated,
		LastEventsExportRawBytes:   s.lastEventsExportRawBytes,
		LastEventsExportBodyBytes:  s.lastEventsExportBodyBytes,
		ConditionalRequests:        conditionalRequestStatusMap(s.conditionalRequests),
	}
	if !s.startedAt.IsZero() {
		status.StartedAt = s.startedAt.UTC().Format(time.RFC3339)
	}
	if !s.lastRecordedAt.IsZero() {
		status.LastRecordedAt = s.lastRecordedAt.UTC().Format(time.RFC3339)
	}
	if s.lastImportResult != nil {
		status.LastImport = &ImportSummary{
			Added:              s.lastImportResult.Added,
			Skipped:            s.lastImportResult.Skipped,
			IgnoredByRetention: s.lastImportResult.IgnoredByRetention,
		}
	}
	return status
}

func conditionalRequestStatusMap(counters map[string]conditionalRequestCounter) map[string]ConditionalRequestStatus {
	if len(counters) == 0 {
		return nil
	}
	status := make(map[string]ConditionalRequestStatus, len(counters))
	for endpoint, counter := range counters {
		misses := counter.Requests - counter.NotModified
		if misses < 0 {
			misses = 0
		}
		entry := ConditionalRequestStatus{
			Requests:    counter.Requests,
			NotModified: counter.NotModified,
			Misses:      misses,
		}
		if counter.Requests > 0 {
			entry.HitRate = float64(counter.NotModified) / float64(counter.Requests)
		}
		status[endpoint] = entry
	}
	return status
}
