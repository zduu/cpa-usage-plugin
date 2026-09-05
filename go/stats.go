package main

import (
	"bufio"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
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
	seen               map[requestDedupKey]time.Time

	totalRequests    int64
	successCount     int64
	failureCount     int64
	totalTokens      int64
	inputTokens      int64
	outputTokens     int64
	cachedTokens     int64
	cacheWriteTokens int64
	reasoningTokens  int64
	latencySum       int64
	latencyN         int64
	startedAt        time.Time
	lastRecordedAt   time.Time

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

	priceStoragePath string
	// claudeCacheRepairEnabled 控制历史缓存双计修复(claude_cache_repair.go)。
	// 默认关闭:旧数据签名不可判定,须用户确认后启用。
	claudeCacheRepairEnabled bool
	priceStorageLoadedPath   string
	priceStorageLastError    string
	modelPrices              map[string]ModelPrice
	modelPriceIndex          map[string]ModelPrice
	modelPricesUpdatedAt     time.Time
	priceVersion             uint64
	modelsDevPricesEnabled   bool
	modelsDevPricesURL       string
	modelsDevRefresh         time.Duration
	modelsDevPrices          map[string]ModelPrice
	modelsDevPriceIndex      map[string]ModelPrice
	modelsDevUpdatedAt       time.Time
	modelsDevLastAttempt     time.Time
	modelsDevLastSuccess     time.Time
	modelsDevLastError       string
	modelsDevETag            string
	modelsDevStop            chan struct{}
	modelsDevDone            chan struct{}
	// timeBasedPrices 缓存「当前价格表里是否存在时段规则」,按 priceVersion 派生。
	timeBasedPrices        bool
	timeBasedPricesValid   bool
	timeBasedPricesVersion uint64

	pricingLocation       *time.Location
	pricingTimezone       string
	pricingTimezoneError  string
	exchangeRateEnabled   bool
	exchangeRateURL       string
	exchangeRateRefresh   time.Duration
	exchangeRateTimeout   time.Duration
	exchangeRateFallback  float64
	exchangeRate          float64
	exchangeRateSource    string
	exchangeRateFetchedAt time.Time
	exchangeRateLastError string
	exchangeRateFailures  int
	exchangeRateStop      chan struct{}
	exchangeRateDone      chan struct{}
	currencyVersion       uint64

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
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	estimatedCost    float64
	latencySum       int64
	latencyN         int64
	Models           map[string]*modelStats
	Sources          map[string]*sourceStatAccumulator
}

type modelStats struct {
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	estimatedCost    float64
	latencySum       int64
	latencyN         int64
	Details          []RequestDetail
	providerStats    map[string]*ModelProviderStat
}

type detailTotals struct {
	totalTokens      int64
	inputTokens      int64
	outputTokens     int64
	cachedTokens     int64
	cacheWriteTokens int64
	reasoningTokens  int64
	latencySum       int64
	latencyN         int64
}

type apiDetailErrorKey struct {
	statusCode int
	failure    string
}

// requestDedupKey is the identity of a persisted detail within a day.  Endpoint,
// Stream, Thinking, BaseURL and similar metadata attached after the fact by
// response interceptors are deliberately excluded — otherwise a native record
// that lacks them could never match the interceptor enrichment (EnrichRecordedUsage
// computes the key from the native record).  Add fields here only when the value
// is known identically on both sides by construction.
type requestDedupKey struct {
	apiName          string
	modelName        string
	timestamp        time.Time
	source           string
	authIndex        string
	clientAPIHash    string
	clientAPIKey     string
	failure          string
	failed           bool
	latencyMs        int64
	ttftMs           int64
	statusCode       int
	inputTokens      int64
	outputTokens     int64
	reasoning        int64
	cachedTokens     int64
	cacheTokens      int64
	cacheWriteTokens int64
	totalTokens      int64
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
	stat.TotalTokens = addNonNegativeInt64(stat.TotalTokens, totals.totalTokens)
	stat.InputTokens = addNonNegativeInt64(stat.InputTokens, totals.inputTokens)
	stat.OutputTokens = addNonNegativeInt64(stat.OutputTokens, totals.outputTokens)
	stat.CachedTokens = addNonNegativeInt64(stat.CachedTokens, totals.cachedTokens)
	stat.CacheWriteTokens = addNonNegativeInt64(stat.CacheWriteTokens, totals.cacheWriteTokens)
	stat.ReasoningTokens = addNonNegativeInt64(stat.ReasoningTokens, totals.reasoningTokens)
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
	stat.TotalTokens = subtractNonNegativeInt64(stat.TotalTokens, totals.totalTokens)
	stat.InputTokens = subtractNonNegativeInt64(stat.InputTokens, totals.inputTokens)
	stat.OutputTokens = subtractNonNegativeInt64(stat.OutputTokens, totals.outputTokens)
	stat.CachedTokens = subtractNonNegativeInt64(stat.CachedTokens, totals.cachedTokens)
	stat.CacheWriteTokens = subtractNonNegativeInt64(stat.CacheWriteTokens, totals.cacheWriteTokens)
	stat.ReasoningTokens = subtractNonNegativeInt64(stat.ReasoningTokens, totals.reasoningTokens)
	if stat.TotalTokens <= 0 && stat.InputTokens <= 0 && stat.OutputTokens <= 0 && stat.CachedTokens <= 0 && stat.CacheWriteTokens <= 0 && stat.ReasoningTokens <= 0 {
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
	stat.TotalRequests = addNonNegativeInt64(stat.TotalRequests, 1)
	if failed {
		stat.FailureCount = addNonNegativeInt64(stat.FailureCount, 1)
	} else {
		stat.SuccessCount = addNonNegativeInt64(stat.SuccessCount, 1)
	}
	stat.TotalTokens = addNonNegativeInt64(stat.TotalTokens, totals.totalTokens)
	stat.InputTokens = addNonNegativeInt64(stat.InputTokens, totals.inputTokens)
	stat.OutputTokens = addNonNegativeInt64(stat.OutputTokens, totals.outputTokens)
	stat.CachedTokens = addNonNegativeInt64(stat.CachedTokens, totals.cachedTokens)
	stat.CacheWriteTokens = addNonNegativeInt64(stat.CacheWriteTokens, totals.cacheWriteTokens)
	stat.ReasoningTokens = addNonNegativeInt64(stat.ReasoningTokens, totals.reasoningTokens)
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
	stat.TotalRequests = subtractNonNegativeInt64(stat.TotalRequests, 1)
	if failed {
		stat.FailureCount = subtractNonNegativeInt64(stat.FailureCount, 1)
	} else {
		stat.SuccessCount = subtractNonNegativeInt64(stat.SuccessCount, 1)
	}
	stat.TotalTokens = subtractNonNegativeInt64(stat.TotalTokens, totals.totalTokens)
	stat.InputTokens = subtractNonNegativeInt64(stat.InputTokens, totals.inputTokens)
	stat.OutputTokens = subtractNonNegativeInt64(stat.OutputTokens, totals.outputTokens)
	stat.CachedTokens = subtractNonNegativeInt64(stat.CachedTokens, totals.cachedTokens)
	stat.CacheWriteTokens = subtractNonNegativeInt64(stat.CacheWriteTokens, totals.cacheWriteTokens)
	stat.ReasoningTokens = subtractNonNegativeInt64(stat.ReasoningTokens, totals.reasoningTokens)
	if stat.TotalRequests <= 0 {
		delete(stats, key)
	}
}

func finalizedModelProviderStats(stats map[string]*ModelProviderStat, totalRequests, successCount, failureCount, totalTokens, inputTokens, outputTokens, cachedTokens, cacheWriteTokens, reasoningTokens int64) []ModelProviderStat {
	providers := make([]ModelProviderStat, 0, len(stats)+1)
	var providerRequests, providerSuccess, providerFailure, providerTotal, providerInput, providerOutput, providerCached, providerCacheWrite, providerReasoning int64
	for _, stat := range stats {
		if stat != nil && stat.TotalRequests > 0 {
			providers = append(providers, *stat)
			providerRequests = addNonNegativeInt64(providerRequests, stat.TotalRequests)
			providerSuccess = addNonNegativeInt64(providerSuccess, stat.SuccessCount)
			providerFailure = addNonNegativeInt64(providerFailure, stat.FailureCount)
			providerTotal = addNonNegativeInt64(providerTotal, stat.TotalTokens)
			providerInput = addNonNegativeInt64(providerInput, stat.InputTokens)
			providerOutput = addNonNegativeInt64(providerOutput, stat.OutputTokens)
			providerCached = addNonNegativeInt64(providerCached, stat.CachedTokens)
			providerCacheWrite = addNonNegativeInt64(providerCacheWrite, stat.CacheWriteTokens)
			providerReasoning = addNonNegativeInt64(providerReasoning, stat.ReasoningTokens)
		}
	}
	remainder := ModelProviderStat{
		TotalRequests:    maxInt64(totalRequests-providerRequests, 0),
		SuccessCount:     maxInt64(successCount-providerSuccess, 0),
		FailureCount:     maxInt64(failureCount-providerFailure, 0),
		TotalTokens:      maxInt64(totalTokens-providerTotal, 0),
		InputTokens:      maxInt64(inputTokens-providerInput, 0),
		OutputTokens:     maxInt64(outputTokens-providerOutput, 0),
		CachedTokens:     maxInt64(cachedTokens-providerCached, 0),
		CacheWriteTokens: maxInt64(cacheWriteTokens-providerCacheWrite, 0),
		ReasoningTokens:  maxInt64(reasoningTokens-providerReasoning, 0),
	}
	if remainder.TotalRequests > 0 || remainder.TotalTokens > 0 || remainder.InputTokens > 0 || remainder.OutputTokens > 0 || remainder.CachedTokens > 0 || remainder.CacheWriteTokens > 0 || remainder.ReasoningTokens > 0 {
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
	stat.Providers = finalizedModelProviderStats(stat.providerStats, stat.TotalRequests, stat.SuccessCount, stat.FailureCount, stat.TotalTokens, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.CacheWriteTokens, stat.ReasoningTokens)
	stat.latencySum = 0
	stat.latencyN = 0
	stat.providerStats = nil
	return stat
}

func finalizeClientAPIModelStat(stat ClientAPIModelStat) ClientAPIModelStat {
	stat.Providers = finalizedModelProviderStats(stat.providerStats, stat.TotalRequests, stat.SuccessCount, stat.FailureCount, stat.TotalTokens, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.CacheWriteTokens, stat.ReasoningTokens)
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
	clientAPI  string
}

// apiKeySalt produces stable grouping IDs for raw client API keys. Users can
// override it with api_key_hash_salt when they need instance-specific hashes.
const defaultAPIKeyHashSalt = "cpa-usage-plugin-client-api-v2"

var apiKeySalt = defaultAPIKeyHashSalt
var apiKeySaltMu sync.RWMutex

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

func setAPIKeySalt(value string) {
	apiKeySaltMu.Lock()
	apiKeySalt = value
	apiKeySaltMu.Unlock()
}

func currentAPIKeySalt() string {
	apiKeySaltMu.RLock()
	value := apiKeySalt
	apiKeySaltMu.RUnlock()
	return value
}

func hashAPIKey(raw string) string {
	s := canonicalClientAPIKey(raw)
	if s == "" {
		return ""
	}
	h := sha256.Sum224([]byte(currentAPIKeySalt() + ":" + s))
	return hex.EncodeToString(h[:])
}

func canonicalClientAPIKey(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return ""
	}
	fields := strings.Fields(s)
	if len(fields) == 2 {
		scheme := strings.ToLower(strings.TrimSuffix(fields[0], ":"))
		switch scheme {
		case "bearer", "token", "key", "apikey", "api-key":
			return strings.Trim(strings.TrimSpace(fields[1]), `"'`)
		}
	}
	lower := strings.ToLower(s)
	for _, scheme := range []string{"bearer", "token", "key", "apikey", "api-key"} {
		prefix := scheme + ":"
		if strings.HasPrefix(lower, prefix) {
			return strings.Trim(strings.TrimSpace(s[len(prefix):]), `"'`)
		}
	}
	return s
}

func isStoredAPIKeyHashShape(value string) bool {
	if len(value) != sha256.Size224*2 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

var stats = NewRequestStatistics()

func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		maxDetailsPerModel:            defaultMaxDetailsPerModel,
		retention:                     time.Duration(defaultRetentionDays) * 24 * time.Hour,
		dedupWindow:                   time.Duration(defaultDedupWindowMinutes) * time.Minute,
		seen:                          make(map[requestDedupKey]time.Time),
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
		modelPriceIndex:               make(map[string]ModelPrice),
		modelsDevPricesURL:            defaultRuntimeConfig().ModelsDevPricesURL,
		modelsDevRefresh:              time.Duration(defaultRuntimeConfig().ModelsDevRefreshSeconds) * time.Second,
		modelsDevPrices:               make(map[string]ModelPrice),
		modelsDevPriceIndex:           make(map[string]ModelPrice),
		conditionalRequests:           make(map[string]conditionalRequestCounter),
		startedAt:                     time.Now(),
		pricingTimezone:               defaultPricingTimezone,
		pricingLocation:               mustLoadLocation(defaultPricingTimezone),
		exchangeRateURL:               defaultExchangeRateURL,
		exchangeRateRefresh:           time.Duration(defaultExchangeRateRefreshSeconds) * time.Second,
		exchangeRateTimeout:           time.Duration(defaultExchangeRateTimeoutSeconds) * time.Second,
		exchangeRateFallback:          defaultExchangeRateFallbackUSDCNY,
	}
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
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
		ClaudeCacheRepairEnabled:      boolPtr(cfg.ClaudeCacheRepairEnabled),
		UpdateEnabled:                 boolPtr(cfg.UpdateEnabled),
		UpdateVersion:                 stringPtr(cfg.UpdateVersion),
		PricingTimezone:               stringPtr(cfg.PricingTimezone),
		ExchangeRateEnabled:           boolPtr(cfg.ExchangeRateEnabled),
		ExchangeRateUSD:               stringPtr(cfg.ExchangeRateUSD),
		ExchangeRateRefreshSeconds:    positiveIntPtr(cfg.ExchangeRateRefreshSeconds),
		ExchangeRateTimeoutSeconds:    positiveIntPtr(cfg.ExchangeRateTimeoutSeconds),
		ExchangeRateFallbackUSDCNY:    float64Ptr(cfg.ExchangeRateFallbackUSDCNY),
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
	exchangeConfigTouched := cfg.ExchangeRateEnabled != nil || cfg.ExchangeRateUSD != nil || cfg.ExchangeRateRefreshSeconds != nil || cfg.ExchangeRateTimeoutSeconds != nil || cfg.ExchangeRateFallbackUSDCNY != nil
	if exchangeConfigTouched {
		s.stopExchangeRateWorker()
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
	if cfg.APIKeyHashSalt != nil {
		salt := strings.TrimSpace(*cfg.APIKeyHashSalt)
		if salt == "" {
			salt = defaultAPIKeyHashSalt
		}
		setAPIKeySalt(salt)
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
	if cfg.ClaudeCacheRepairEnabled != nil {
		s.claudeCacheRepairEnabled = *cfg.ClaudeCacheRepairEnabled
	}
	if cfg.ModelsDevPricesEnabled != nil {
		s.modelsDevPricesEnabled = *cfg.ModelsDevPricesEnabled
	}
	if cfg.PricingTimezone != nil {
		name := strings.TrimSpace(*cfg.PricingTimezone)
		if name == "" {
			name = defaultPricingTimezone
		}
		if location, err := time.LoadLocation(name); err != nil {
			// 保留旧时区,但必须把拒绝原因带到运行状态和健康告警里,否则用户改错
			// 时区后看不出计费仍在按旧时区走。
			s.pricingTimezoneError = fmt.Sprintf("pricing_timezone %q 无效(%v),继续使用 %s", name, err, s.pricingTimezone)
		} else {
			s.pricingTimezone = name
			s.pricingLocation = location
			s.pricingTimezoneError = ""
		}
	}
	if cfg.ExchangeRateUSD != nil && strings.TrimSpace(*cfg.ExchangeRateUSD) != "" {
		candidate := strings.TrimSpace(*cfg.ExchangeRateUSD)
		if _, err := configureHTTPSURL(candidate); err != nil {
			s.exchangeRateLastError = err.Error()
		} else {
			s.exchangeRateURL = candidate
		}
	}
	if cfg.ExchangeRateRefreshSeconds != nil && *cfg.ExchangeRateRefreshSeconds > 0 {
		s.exchangeRateRefresh = time.Duration(clampInt(*cfg.ExchangeRateRefreshSeconds, 300, 86400)) * time.Second
	}
	if cfg.ExchangeRateTimeoutSeconds != nil && *cfg.ExchangeRateTimeoutSeconds > 0 {
		s.exchangeRateTimeout = time.Duration(clampInt(*cfg.ExchangeRateTimeoutSeconds, 1, 30)) * time.Second
	}
	if cfg.ExchangeRateFallbackUSDCNY != nil && validFallbackExchangeRate(*cfg.ExchangeRateFallbackUSDCNY) {
		s.exchangeRateFallback = *cfg.ExchangeRateFallbackUSDCNY
	}
	if cfg.ExchangeRateEnabled != nil {
		s.exchangeRateEnabled = *cfg.ExchangeRateEnabled
	}
	if oldModelsDevEnabled != s.modelsDevPricesEnabled || oldModelsDevURL != s.modelsDevPricesURL {
		s.modelsDevPrices = make(map[string]ModelPrice)
		s.modelsDevPriceIndex = make(map[string]ModelPrice)
		s.priceVersion++
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
	s.configureExchangeRateWorkerLocked()
	s.configureStorageLocked()
	// 修复开关可能刚由热重载打开:存储装载(冷恢复或合并)完成后统一对当前
	// 内存明细执行一次历史缓存修复,置于价格加载与成本序列重建之前,保证
	// 货币序列从修复后的 token 序列重建。开关关闭时该调用为空操作。
	s.repairClaudeCacheFallbackDetailsLocked(time.Now())
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
	statsKey := usageGroupKey(record)
	modelName := firstNonEmpty(record.Model, "unknown")
	detail := requestDetailFromUsageRecord(record, timestamp, s.logResponseHeaders)
	var persistDetail *persistedDetail
	s.mu.Lock()

	now := time.Now()
	if s.recordDetailLocked(statsKey, modelName, detail, requestDedupKey{}, now, false) {
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

func requestDetailFromUsageRecord(record UsageRecord, timestamp time.Time, whitelist headerWhitelist) RequestDetail {
	cacheReadTokens, cacheWriteTokens, cacheTokens := usageDetailCacheTokenParts(record.Detail, record.Provider)
	totalTokens := usageDetailTotalTokens(record.Detail, record.Provider)
	detail := RequestDetail{
		Model:          firstNonEmpty(record.Model, "unknown"),
		RequestedModel: strings.TrimSpace(firstNonEmpty(record.Alias, record.Model)),
		Timestamp:      timestamp,
		LatencyMs:      record.Latency.Milliseconds(),
		TTFTMs:         record.TTFT.Milliseconds(),
		APIKey:         maskAPIKey(canonicalClientAPIKey(record.APIKey)),
		APIKeyHash:     hashAPIKey(record.APIKey),
		Source:         usageSource(record),
		Provider:       strings.TrimSpace(record.Provider),
		ExecutorType:   strings.TrimSpace(record.ExecutorType),
		AuthID:         strings.TrimSpace(record.AuthID),
		AuthIndex:      strings.TrimSpace(record.AuthIndex),
		AuthType:       strings.TrimSpace(record.AuthType),
		Endpoint:       strings.TrimSpace(record.Endpoint),
		BaseURL:        strings.TrimSpace(record.BaseURL),
		Stream:         record.Stream,
		Thinking:       usageThinking(record),
		Tokens: TokenStats{
			InputTokens:      record.Detail.InputTokens,
			OutputTokens:     record.Detail.OutputTokens,
			ReasoningTokens:  record.Detail.ReasoningTokens,
			CachedTokens:     cacheReadTokens,
			CacheReadTokens:  cacheReadTokens,
			CacheTokens:      cacheTokens,
			CacheWriteTokens: cacheWriteTokens,
			TotalTokens:      totalTokens,
		},
		Failed:     record.Failed,
		StatusCode: record.Failure.StatusCode,
		Failure:    trimLong(redactSensitiveText(record.Failure.Body), 500),
		Headers:    filterHeaders(record.ResponseHeaders, whitelist),
	}
	if meta := protocolCorrelationMetaForUsageRecord(record); meta != nil {
		detail.Correlation = meta
	}
	return detail
}

// EnrichRecordedUsage merges request metadata that response interceptors have
// but older native CPA usage records do not carry.
func (s *RequestStatistics) EnrichRecordedUsage(record UsageRecord, enrichment UsageRecord) bool {
	if s == nil || record.RequestedAt.IsZero() {
		return false
	}
	apiName := usageGroupKey(record)
	modelName := firstNonEmpty(record.Model, "unknown")
	target := dedupKey(apiName, modelName, requestDetailFromUsageRecord(record, record.RequestedAt, headerWhitelist{}))

	s.mu.Lock()
	apiSt := s.apis[apiName]
	if apiSt == nil || apiSt.Models[modelName] == nil {
		s.mu.Unlock()
		return false
	}
	details := apiSt.Models[modelName].Details
	for i := len(details) - 1; i >= 0; i-- {
		if dedupKey(apiName, modelName, details[i]) != target {
			continue
		}
		update := requestDetailFromUsageRecord(enrichment, details[i].Timestamp, headerWhitelist{})
		changed := enrichRequestDetailMetadata(&details[i], update)
		var persistUpdate *persistedDetail
		if changed {
			apiSt.Models[modelName].Details = details
			s.invalidateSummaryLocked()
			if s.storageEnabled {
				persistUpdate = &persistedDetail{API: apiName, Model: modelName, Detail: details[i], MetadataOnly: true}
			}
		}
		s.mu.Unlock()
		if persistUpdate != nil {
			s.enqueueStorageDetail(*persistUpdate)
		}
		return changed
	}
	s.mu.Unlock()
	return false
}

func enrichRequestDetailMetadata(detail *RequestDetail, update RequestDetail) bool {
	if detail == nil {
		return false
	}
	changed := false
	if detail.Endpoint == "" && strings.TrimSpace(update.Endpoint) != "" {
		detail.Endpoint = strings.TrimSpace(update.Endpoint)
		changed = true
	}
	if strings.TrimSpace(detail.RequestedModel) == "" && strings.TrimSpace(update.RequestedModel) != "" {
		detail.RequestedModel = strings.TrimSpace(update.RequestedModel)
		changed = true
	}
	if detail.Thinking == (UsageThinking{}) && update.Thinking != (UsageThinking{}) {
		detail.Thinking = update.Thinking
		changed = true
	}
	if !detail.Stream && update.Stream {
		detail.Stream = true
		changed = true
	}
	return changed
}

type persistedDetail struct {
	API          string        `json:"api"`
	Model        string        `json:"model"`
	Detail       RequestDetail `json:"detail"`
	MetadataOnly bool          `json:"metadata_only,omitempty"`
	enqueuedAt   time.Time     `json:"-"`
}

type persistedStorageSnapshot struct {
	Version     int                `json:"version"`
	GeneratedAt string             `json:"generated_at"`
	Usage       StatisticsSnapshot `json:"usage"`
}

const currentStorageSnapshotVersion = 2

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

func (s *RequestStatistics) recordDetailLocked(apiName, modelName string, detail RequestDetail, dedup requestDedupKey, now time.Time, useDedupWindow bool) bool {
	if s == nil {
		return false
	}
	if strings.TrimSpace(apiName) == "" {
		apiName = "未知接口"
	}
	// cost_usd 只在查询期附加。导入的导出文件里带着当时的成本,留着会被原样写进
	// JSONL 并且永远不会被读取,反而让存储记录与「后端没算过成本」的语义冲突。
	detail.CostUSD = nil
	if dedup == (requestDedupKey{}) {
		dedup = dedupKey(apiName, modelName, detail)
	}
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

	s.totalRequests = addNonNegativeInt64(s.totalRequests, 1)
	if detail.Failed {
		s.failureCount = addNonNegativeInt64(s.failureCount, 1)
	} else {
		s.successCount = addNonNegativeInt64(s.successCount, 1)
	}
	s.totalTokens = addNonNegativeInt64(s.totalTokens, totals.totalTokens)
	s.inputTokens = addNonNegativeInt64(s.inputTokens, totals.inputTokens)
	s.outputTokens = addNonNegativeInt64(s.outputTokens, totals.outputTokens)
	s.cachedTokens = addNonNegativeInt64(s.cachedTokens, totals.cachedTokens)
	s.cacheWriteTokens = addNonNegativeInt64(s.cacheWriteTokens, totals.cacheWriteTokens)
	s.reasoningTokens = addNonNegativeInt64(s.reasoningTokens, totals.reasoningTokens)
	s.latencySum = addNonNegativeInt64(s.latencySum, totals.latencySum)
	s.latencyN = addNonNegativeInt64(s.latencyN, totals.latencyN)
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()
	cost := s.detailCostLocked(modelName, detail, totals)
	s.requestsByDay[dayKey] = addNonNegativeInt64(s.requestsByDay[dayKey], 1)
	s.requestsByHour[hourKey] = addNonNegativeInt64(s.requestsByHour[hourKey], 1)
	s.tokensByDay[dayKey] = addNonNegativeInt64(s.tokensByDay[dayKey], totals.totalTokens)
	s.tokensByHour[hourKey] = addNonNegativeInt64(s.tokensByHour[hourKey], totals.totalTokens)
	s.costByDay[dayKey] = addNonNegativeCost(s.costByDay[dayKey], cost)
	s.costByHour[hourKey] = addNonNegativeCost(s.costByHour[hourKey], cost)
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
	if persisted.Version < currentStorageSnapshotVersion {
		migrateLegacySnapshotCacheReads(&persisted.Usage)
	}
	if s.hasRecordsLocked() {
		_, _ = s.mergeSnapshotLocked(persisted.Usage, false, now)
	} else {
		s.restoreStorageSnapshotLocked(persisted.Usage, now)
		s.repairMigratedAttributionDetailsLocked(now)
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
	snapshot, _ = reconcileProtocolFallbackSnapshot(snapshot)
	s.totalRequests = nonNegativeInt64(snapshot.TotalRequests)
	s.successCount = nonNegativeInt64(snapshot.SuccessCount)
	s.failureCount = nonNegativeInt64(snapshot.FailureCount)
	s.totalTokens = nonNegativeInt64(snapshot.TotalTokens)
	s.inputTokens = nonNegativeInt64(snapshot.InputTokens)
	s.outputTokens = nonNegativeInt64(snapshot.OutputTokens)
	s.cachedTokens = nonNegativeInt64(snapshot.CachedTokens)
	s.cacheWriteTokens = nonNegativeInt64(snapshot.CacheWriteTokens)
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
		if storageSnapshotHasMultipleAPIGroupNames(apiName, apiSnapshot) {
			restoredDetails = addNonNegativeInt64(restoredDetails, s.restoreStorageSnapshotSplitAPILocked(apiName, apiSnapshot, now))
			continue
		}
		apiName = storageSnapshotAPIName(apiName, apiSnapshot)
		apiSt := &apiStats{
			TotalRequests:    nonNegativeInt64(apiSnapshot.TotalRequests),
			SuccessCount:     nonNegativeInt64(apiSnapshot.SuccessCount),
			FailureCount:     nonNegativeInt64(apiSnapshot.FailureCount),
			TotalTokens:      nonNegativeInt64(apiSnapshot.TotalTokens),
			InputTokens:      nonNegativeInt64(apiSnapshot.InputTokens),
			OutputTokens:     nonNegativeInt64(apiSnapshot.OutputTokens),
			CachedTokens:     nonNegativeInt64(apiSnapshot.CachedTokens),
			CacheWriteTokens: nonNegativeInt64(apiSnapshot.CacheWriteTokens),
			ReasoningTokens:  nonNegativeInt64(apiSnapshot.ReasoningTokens),
			Models:           make(map[string]*modelStats, len(apiSnapshot.Models)),
			Sources:          make(map[string]*sourceStatAccumulator),
		}
		apiSt.latencySum, apiSt.latencyN = restoredLatencyAggregate(apiSnapshot.AvgLatencyMs, apiSt.TotalRequests)
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = normalizeModelName(modelName)
			if storageSnapshotNeedsSplitModelRestore(modelName, modelSnapshot) {
				restoredDetails = addNonNegativeInt64(restoredDetails, s.restoreStorageSnapshotSplitModelLocked(apiSt, modelName, modelSnapshot, now))
				continue
			}
			modelName = storageSnapshotModelName(modelName, modelSnapshot)
			snapshotProviderStats := modelProviderStatsFromSnapshot(modelSnapshot.Providers)
			modelSt := &modelStats{
				TotalRequests:    nonNegativeInt64(modelSnapshot.TotalRequests),
				SuccessCount:     nonNegativeInt64(modelSnapshot.SuccessCount),
				FailureCount:     nonNegativeInt64(modelSnapshot.FailureCount),
				TotalTokens:      nonNegativeInt64(modelSnapshot.TotalTokens),
				InputTokens:      nonNegativeInt64(modelSnapshot.InputTokens),
				OutputTokens:     nonNegativeInt64(modelSnapshot.OutputTokens),
				CachedTokens:     nonNegativeInt64(modelSnapshot.CachedTokens),
				CacheWriteTokens: nonNegativeInt64(modelSnapshot.CacheWriteTokens),
				ReasoningTokens:  nonNegativeInt64(modelSnapshot.ReasoningTokens),
				Details:          make([]RequestDetail, 0, len(modelSnapshot.Details)),
				providerStats:    snapshotProviderStats,
			}
			modelSt.latencySum, modelSt.latencyN = restoredLatencyAggregate(modelSnapshot.AvgLatencyMs, modelSt.TotalRequests)
			var detailAggregates detailTotals
			for _, detail := range modelSnapshot.Details {
				detail = normalizeStorageSnapshotDetail(modelName, detail, now)
				modelSt.Details = append(modelSt.Details, detail)
				restoredDetails = addNonNegativeInt64(restoredDetails, 1)

				totals := detailTotalsFromRequest(detail)
				detailAggregates.totalTokens = addNonNegativeInt64(detailAggregates.totalTokens, totals.totalTokens)
				detailAggregates.inputTokens = addNonNegativeInt64(detailAggregates.inputTokens, totals.inputTokens)
				detailAggregates.outputTokens = addNonNegativeInt64(detailAggregates.outputTokens, totals.outputTokens)
				detailAggregates.cachedTokens = addNonNegativeInt64(detailAggregates.cachedTokens, totals.cachedTokens)
				detailAggregates.cacheWriteTokens = addNonNegativeInt64(detailAggregates.cacheWriteTokens, totals.cacheWriteTokens)
				detailAggregates.reasoningTokens = addNonNegativeInt64(detailAggregates.reasoningTokens, totals.reasoningTokens)
				detailAggregates.latencySum = addNonNegativeInt64(detailAggregates.latencySum, totals.latencySum)
				detailAggregates.latencyN = addNonNegativeInt64(detailAggregates.latencyN, totals.latencyN)
				incrementAPISourceStats(apiSt, detail, totals)
				if len(snapshotProviderStats) == 0 {
					modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
				}
				s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
				s.incrementHealthBucketLocked(detail)
				if detail.Timestamp.After(s.lastRecordedAt) {
					s.lastRecordedAt = detail.Timestamp
				}
			}
			modelSt.InputTokens = maxInt64(modelSt.InputTokens, detailAggregates.inputTokens)
			modelSt.OutputTokens = maxInt64(modelSt.OutputTokens, detailAggregates.outputTokens)
			modelSt.CachedTokens = maxInt64(modelSt.CachedTokens, detailAggregates.cachedTokens)
			modelSt.CacheWriteTokens = maxInt64(modelSt.CacheWriteTokens, detailAggregates.cacheWriteTokens)
			modelSt.ReasoningTokens = maxInt64(modelSt.ReasoningTokens, detailAggregates.reasoningTokens)
			if modelSt.latencyN == 0 && detailAggregates.latencyN > 0 {
				modelSt.latencySum = detailAggregates.latencySum
				modelSt.latencyN = detailAggregates.latencyN
			}
			mergeRestoredModelStats(apiSt, modelName, modelSt)
			s.mergeModelSummaryAggregateLocked(modelName, modelSt)
		}
		restoreAPIAggregatesFromModels(apiSt)
		s.mergeRestoredAPIStatsLocked(apiName, apiSt)
	}
	restoreSnapshotAggregatesFromAPIs(s)
	if s.totalRequests == 0 && restoredDetails > 0 {
		s.rebuildAggregatesLocked()
	} else {
		s.restoreMissingCostSeriesLocked(restoredDetails)
	}
}

func mergeRestoredModelStats(apiSt *apiStats, modelName string, modelSt *modelStats) {
	if apiSt == nil || modelSt == nil {
		return
	}
	if apiSt.Models == nil {
		apiSt.Models = make(map[string]*modelStats)
	}
	existing, ok := apiSt.Models[modelName]
	if !ok || existing == nil {
		apiSt.Models[modelName] = modelSt
		return
	}
	mergeModelStats(existing, modelSt)
}

func (s *RequestStatistics) mergeRestoredAPIStatsLocked(apiName string, apiSt *apiStats) {
	if s == nil || apiSt == nil {
		return
	}
	apiName = strings.TrimSpace(apiName)
	if apiName == "" {
		apiName = "未知接口"
	}
	existing, ok := s.apis[apiName]
	if !ok || existing == nil {
		s.apis[apiName] = apiSt
		return
	}
	mergeAPIStats(existing, apiSt)
}

func mergeAPIStats(dst, src *apiStats) {
	if dst == nil || src == nil {
		return
	}
	dst.TotalRequests = addNonNegativeInt64(dst.TotalRequests, src.TotalRequests)
	dst.SuccessCount = addNonNegativeInt64(dst.SuccessCount, src.SuccessCount)
	dst.FailureCount = addNonNegativeInt64(dst.FailureCount, src.FailureCount)
	dst.TotalTokens = addNonNegativeInt64(dst.TotalTokens, src.TotalTokens)
	dst.InputTokens = addNonNegativeInt64(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = addNonNegativeInt64(dst.OutputTokens, src.OutputTokens)
	dst.CachedTokens = addNonNegativeInt64(dst.CachedTokens, src.CachedTokens)
	dst.CacheWriteTokens = addNonNegativeInt64(dst.CacheWriteTokens, src.CacheWriteTokens)
	dst.ReasoningTokens = addNonNegativeInt64(dst.ReasoningTokens, src.ReasoningTokens)
	dst.latencySum = addNonNegativeInt64(dst.latencySum, src.latencySum)
	dst.latencyN = addNonNegativeInt64(dst.latencyN, src.latencyN)
	if dst.Models == nil {
		dst.Models = make(map[string]*modelStats, len(src.Models))
	}
	for modelName, srcModel := range src.Models {
		if srcModel == nil {
			continue
		}
		dstModel, ok := dst.Models[modelName]
		if !ok || dstModel == nil {
			dst.Models[modelName] = srcModel
			continue
		}
		mergeModelStats(dstModel, srcModel)
	}
	mergeSourceStats(dst, src)
}

func mergeModelStats(dst, src *modelStats) {
	if dst == nil || src == nil {
		return
	}
	dst.TotalRequests = addNonNegativeInt64(dst.TotalRequests, src.TotalRequests)
	dst.SuccessCount = addNonNegativeInt64(dst.SuccessCount, src.SuccessCount)
	dst.FailureCount = addNonNegativeInt64(dst.FailureCount, src.FailureCount)
	dst.TotalTokens = addNonNegativeInt64(dst.TotalTokens, src.TotalTokens)
	dst.InputTokens = addNonNegativeInt64(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = addNonNegativeInt64(dst.OutputTokens, src.OutputTokens)
	dst.CachedTokens = addNonNegativeInt64(dst.CachedTokens, src.CachedTokens)
	dst.CacheWriteTokens = addNonNegativeInt64(dst.CacheWriteTokens, src.CacheWriteTokens)
	dst.ReasoningTokens = addNonNegativeInt64(dst.ReasoningTokens, src.ReasoningTokens)
	dst.latencySum = addNonNegativeInt64(dst.latencySum, src.latencySum)
	dst.latencyN = addNonNegativeInt64(dst.latencyN, src.latencyN)
	dst.Details = append(dst.Details, src.Details...)
	dst.providerStats = mergeModelProviderStats(dst.providerStats, src.providerStats)
}

func (s *RequestStatistics) restoreStorageSnapshotSplitModelLocked(apiSt *apiStats, modelName string, modelSnapshot ModelSnapshot, now time.Time) int64 {
	if s == nil || apiSt == nil {
		return 0
	}
	modelName = normalizeModelName(modelName)
	models := make(map[string]*modelStats)
	var restoredDetails int64
	var detailAggregate storageSnapshotDetailAggregate
	var detailProviderStats map[string]*ModelProviderStat
	for _, detail := range modelSnapshot.Details {
		detailModelName := storageSnapshotDetailModelName(modelName, detail)
		detail = normalizeStorageSnapshotDetail(detailModelName, detail, now)
		modelSt, ok := models[detailModelName]
		if !ok {
			modelSt = &modelStats{}
			models[detailModelName] = modelSt
		}
		totals := incrementModelStats(modelSt, detail)
		detailAggregate.add(detail, totals)
		detailProviderStats = incrementModelProviderStats(detailProviderStats, detail.Provider, detail.Failed, totals)
		incrementAPISourceStats(apiSt, detail, totals)
		s.incrementSummaryDimensionStatsLocked(detailModelName, detail, totals)
		s.incrementHealthBucketLocked(detail)
		if detail.Timestamp.After(s.lastRecordedAt) {
			s.lastRecordedAt = detail.Timestamp
		}
		restoredDetails = addNonNegativeInt64(restoredDetails, 1)
	}
	for detailModelName, modelSt := range models {
		mergeRestoredModelStats(apiSt, detailModelName, modelSt)
		s.mergeModelSummaryAggregateLocked(detailModelName, modelSt)
	}
	if residualModelSt := storageSnapshotResidualModelStats(modelSnapshot, detailAggregate); residualModelSt != nil {
		residualModelSt.providerStats = residualModelProviderStats(modelSnapshot.Providers, detailProviderStats)
		mergeRestoredModelStats(apiSt, modelName, residualModelSt)
		s.mergeModelSummaryAggregateLocked(modelName, residualModelSt)
	}
	return restoredDetails
}

func incrementModelStats(modelSt *modelStats, detail RequestDetail) detailTotals {
	totals := detailTotalsFromRequest(detail)
	modelSt.TotalRequests = addNonNegativeInt64(modelSt.TotalRequests, 1)
	if detail.Failed {
		modelSt.FailureCount = addNonNegativeInt64(modelSt.FailureCount, 1)
	} else {
		modelSt.SuccessCount = addNonNegativeInt64(modelSt.SuccessCount, 1)
	}
	modelSt.TotalTokens = addNonNegativeInt64(modelSt.TotalTokens, totals.totalTokens)
	modelSt.InputTokens = addNonNegativeInt64(modelSt.InputTokens, totals.inputTokens)
	modelSt.OutputTokens = addNonNegativeInt64(modelSt.OutputTokens, totals.outputTokens)
	modelSt.CachedTokens = addNonNegativeInt64(modelSt.CachedTokens, totals.cachedTokens)
	modelSt.CacheWriteTokens = addNonNegativeInt64(modelSt.CacheWriteTokens, totals.cacheWriteTokens)
	modelSt.ReasoningTokens = addNonNegativeInt64(modelSt.ReasoningTokens, totals.reasoningTokens)
	modelSt.latencySum = addNonNegativeInt64(modelSt.latencySum, totals.latencySum)
	modelSt.latencyN = addNonNegativeInt64(modelSt.latencyN, totals.latencyN)
	modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
	modelSt.Details = append(modelSt.Details, detail)
	return totals
}

func mergeModelProviderStats(dst map[string]*ModelProviderStat, src map[string]*ModelProviderStat) map[string]*ModelProviderStat {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]*ModelProviderStat, len(src))
	}
	for provider, srcStat := range src {
		if srcStat == nil {
			continue
		}
		dstStat, ok := dst[provider]
		if !ok || dstStat == nil {
			copy := *srcStat
			dst[provider] = &copy
			continue
		}
		dstStat.TotalRequests = addNonNegativeInt64(dstStat.TotalRequests, srcStat.TotalRequests)
		dstStat.SuccessCount = addNonNegativeInt64(dstStat.SuccessCount, srcStat.SuccessCount)
		dstStat.FailureCount = addNonNegativeInt64(dstStat.FailureCount, srcStat.FailureCount)
		dstStat.TotalTokens = addNonNegativeInt64(dstStat.TotalTokens, srcStat.TotalTokens)
		dstStat.InputTokens = addNonNegativeInt64(dstStat.InputTokens, srcStat.InputTokens)
		dstStat.OutputTokens = addNonNegativeInt64(dstStat.OutputTokens, srcStat.OutputTokens)
		dstStat.CachedTokens = addNonNegativeInt64(dstStat.CachedTokens, srcStat.CachedTokens)
		dstStat.CacheWriteTokens = addNonNegativeInt64(dstStat.CacheWriteTokens, srcStat.CacheWriteTokens)
		dstStat.ReasoningTokens = addNonNegativeInt64(dstStat.ReasoningTokens, srcStat.ReasoningTokens)
	}
	return dst
}

func modelProviderStatsFromSnapshot(values []ModelProviderStat) map[string]*ModelProviderStat {
	var result map[string]*ModelProviderStat
	for _, value := range values {
		value.Provider = strings.TrimSpace(value.Provider)
		value.TotalRequests = nonNegativeInt64(value.TotalRequests)
		value.SuccessCount = nonNegativeInt64(value.SuccessCount)
		value.FailureCount = nonNegativeInt64(value.FailureCount)
		value.TotalTokens = nonNegativeInt64(value.TotalTokens)
		value.InputTokens = nonNegativeInt64(value.InputTokens)
		value.OutputTokens = nonNegativeInt64(value.OutputTokens)
		value.CachedTokens = nonNegativeInt64(value.CachedTokens)
		value.CacheWriteTokens = nonNegativeInt64(value.CacheWriteTokens)
		value.ReasoningTokens = nonNegativeInt64(value.ReasoningTokens)
		if value.TotalRequests <= 0 {
			continue
		}
		key := modelProviderStatsKey(value.Provider)
		if result == nil {
			result = make(map[string]*ModelProviderStat)
		}
		if existing, ok := result[key]; ok {
			existing.TotalRequests = addNonNegativeInt64(existing.TotalRequests, value.TotalRequests)
			existing.SuccessCount = addNonNegativeInt64(existing.SuccessCount, value.SuccessCount)
			existing.FailureCount = addNonNegativeInt64(existing.FailureCount, value.FailureCount)
			existing.TotalTokens = addNonNegativeInt64(existing.TotalTokens, value.TotalTokens)
			existing.InputTokens = addNonNegativeInt64(existing.InputTokens, value.InputTokens)
			existing.OutputTokens = addNonNegativeInt64(existing.OutputTokens, value.OutputTokens)
			existing.CachedTokens = addNonNegativeInt64(existing.CachedTokens, value.CachedTokens)
			existing.CacheWriteTokens = addNonNegativeInt64(existing.CacheWriteTokens, value.CacheWriteTokens)
			existing.ReasoningTokens = addNonNegativeInt64(existing.ReasoningTokens, value.ReasoningTokens)
			continue
		}
		copy := value
		result[key] = &copy
	}
	return result
}

func residualModelProviderStats(values []ModelProviderStat, used map[string]*ModelProviderStat) map[string]*ModelProviderStat {
	result := modelProviderStatsFromSnapshot(values)
	for key, usedStat := range used {
		stat := result[key]
		if stat == nil || usedStat == nil {
			continue
		}
		stat.TotalRequests = subtractNonNegativeInt64(stat.TotalRequests, usedStat.TotalRequests)
		stat.SuccessCount = subtractNonNegativeInt64(stat.SuccessCount, usedStat.SuccessCount)
		stat.FailureCount = subtractNonNegativeInt64(stat.FailureCount, usedStat.FailureCount)
		stat.TotalTokens = subtractNonNegativeInt64(stat.TotalTokens, usedStat.TotalTokens)
		stat.InputTokens = subtractNonNegativeInt64(stat.InputTokens, usedStat.InputTokens)
		stat.OutputTokens = subtractNonNegativeInt64(stat.OutputTokens, usedStat.OutputTokens)
		stat.CachedTokens = subtractNonNegativeInt64(stat.CachedTokens, usedStat.CachedTokens)
		stat.CacheWriteTokens = subtractNonNegativeInt64(stat.CacheWriteTokens, usedStat.CacheWriteTokens)
		stat.ReasoningTokens = subtractNonNegativeInt64(stat.ReasoningTokens, usedStat.ReasoningTokens)
		if stat.TotalRequests <= 0 {
			delete(result, key)
		}
	}
	return result
}

func mergeSourceStats(dst, src *apiStats) {
	if dst == nil || src == nil || len(src.Sources) == 0 {
		return
	}
	if dst.Sources == nil {
		dst.Sources = make(map[string]*sourceStatAccumulator, len(src.Sources))
	}
	for source, srcAgg := range src.Sources {
		if srcAgg == nil {
			continue
		}
		dstAgg, ok := dst.Sources[source]
		if !ok || dstAgg == nil {
			dst.Sources[source] = cloneSourceStatAccumulator(srcAgg)
			continue
		}
		dstAgg.stat.TotalRequests = addNonNegativeInt64(dstAgg.stat.TotalRequests, srcAgg.stat.TotalRequests)
		dstAgg.stat.SuccessCount = addNonNegativeInt64(dstAgg.stat.SuccessCount, srcAgg.stat.SuccessCount)
		dstAgg.stat.FailureCount = addNonNegativeInt64(dstAgg.stat.FailureCount, srcAgg.stat.FailureCount)
		dstAgg.stat.TotalTokens = addNonNegativeInt64(dstAgg.stat.TotalTokens, srcAgg.stat.TotalTokens)
		if dstAgg.stat.Provider == "" {
			dstAgg.stat.Provider = srcAgg.stat.Provider
		}
		if dstAgg.providers == nil {
			dstAgg.providers = make(map[string]int64, len(srcAgg.providers))
		}
		for provider, count := range srcAgg.providers {
			dstAgg.providers[provider] = addNonNegativeInt64(dstAgg.providers[provider], count)
		}
	}
}

func cloneSourceStatAccumulator(src *sourceStatAccumulator) *sourceStatAccumulator {
	if src == nil {
		return nil
	}
	dst := &sourceStatAccumulator{
		stat:      src.stat,
		providers: make(map[string]int64, len(src.providers)),
	}
	for provider, count := range src.providers {
		dst.providers[provider] = count
	}
	return dst
}

func (s *RequestStatistics) restoreStorageSnapshotSplitAPILocked(apiName string, apiSnapshot APISnapshot, now time.Time) int64 {
	if s == nil {
		return 0
	}
	var restoredDetails int64
	residualAPIName := storageSnapshotResidualAPIName(apiName, apiSnapshot)
	for modelName, modelSnapshot := range apiSnapshot.Models {
		modelName = normalizeModelName(modelName)
		var detailAggregate storageSnapshotDetailAggregate
		var detailProviderStats map[string]*ModelProviderStat
		for _, detail := range modelSnapshot.Details {
			detailModelName := storageSnapshotDetailModelName(modelName, detail)
			detailAPIName := storageSnapshotDetailAPIName(apiName, detail)
			detail = normalizeStorageSnapshotDetail(detailModelName, detail, now)
			apiSt, ok := s.apis[detailAPIName]
			if !ok {
				apiSt = &apiStats{Models: make(map[string]*modelStats), Sources: make(map[string]*sourceStatAccumulator)}
				s.apis[detailAPIName] = apiSt
			}

			totals := s.updateAPIStats(apiSt, detailModelName, detail)
			detailAggregate.add(detail, totals)
			detailProviderStats = incrementModelProviderStats(detailProviderStats, detail.Provider, detail.Failed, totals)
			incrementAPISourceStats(apiSt, detail, totals)
			s.incrementModelSummaryStatsLocked(detailModelName, detail, totals)
			s.incrementSummaryDimensionStatsLocked(detailModelName, detail, totals)
			s.incrementHealthBucketLocked(detail)
			if detail.Timestamp.After(s.lastRecordedAt) {
				s.lastRecordedAt = detail.Timestamp
			}
			restoredDetails = addNonNegativeInt64(restoredDetails, 1)
		}
		if residualModelSt := storageSnapshotResidualModelStats(modelSnapshot, detailAggregate); residualModelSt != nil {
			residualModelSt.providerStats = residualModelProviderStats(modelSnapshot.Providers, detailProviderStats)
			s.addStorageSnapshotResidualModelLocked(residualAPIName, modelName, residualModelSt)
		}
	}
	return restoredDetails
}

type storageSnapshotDetailAggregate struct {
	totalRequests    int64
	successCount     int64
	failureCount     int64
	totalTokens      int64
	inputTokens      int64
	outputTokens     int64
	cachedTokens     int64
	cacheWriteTokens int64
	reasoningTokens  int64
	latencySum       int64
	latencyN         int64
}

func (a *storageSnapshotDetailAggregate) add(detail RequestDetail, totals detailTotals) {
	if a == nil {
		return
	}
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
	a.latencySum = addNonNegativeInt64(a.latencySum, totals.latencySum)
	a.latencyN = addNonNegativeInt64(a.latencyN, totals.latencyN)
}

func storageSnapshotResidualModelStats(modelSnapshot ModelSnapshot, detailAggregate storageSnapshotDetailAggregate) *modelStats {
	totalRequests := subtractNonNegativeInt64(modelSnapshot.TotalRequests, detailAggregate.totalRequests)
	successCount := subtractNonNegativeInt64(modelSnapshot.SuccessCount, detailAggregate.successCount)
	failureCount := subtractNonNegativeInt64(modelSnapshot.FailureCount, detailAggregate.failureCount)
	totalTokens := subtractNonNegativeInt64(modelSnapshot.TotalTokens, detailAggregate.totalTokens)
	inputTokens := subtractNonNegativeInt64(modelSnapshot.InputTokens, detailAggregate.inputTokens)
	outputTokens := subtractNonNegativeInt64(modelSnapshot.OutputTokens, detailAggregate.outputTokens)
	cachedTokens := subtractNonNegativeInt64(modelSnapshot.CachedTokens, detailAggregate.cachedTokens)
	cacheWriteTokens := subtractNonNegativeInt64(modelSnapshot.CacheWriteTokens, detailAggregate.cacheWriteTokens)
	reasoningTokens := subtractNonNegativeInt64(modelSnapshot.ReasoningTokens, detailAggregate.reasoningTokens)
	latencySum, latencyN := restoredLatencyAggregate(modelSnapshot.AvgLatencyMs, nonNegativeInt64(modelSnapshot.TotalRequests))
	latencySum = subtractNonNegativeInt64(latencySum, detailAggregate.latencySum)
	latencyN = subtractNonNegativeInt64(latencyN, detailAggregate.latencyN)
	if totalRequests == 0 && successCount == 0 && failureCount == 0 && totalTokens == 0 &&
		inputTokens == 0 && outputTokens == 0 && cachedTokens == 0 && cacheWriteTokens == 0 && reasoningTokens == 0 && latencyN == 0 {
		return nil
	}
	return &modelStats{
		TotalRequests:    totalRequests,
		SuccessCount:     successCount,
		FailureCount:     failureCount,
		TotalTokens:      totalTokens,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CachedTokens:     cachedTokens,
		CacheWriteTokens: cacheWriteTokens,
		ReasoningTokens:  reasoningTokens,
		latencySum:       latencySum,
		latencyN:         latencyN,
	}
}

func (s *RequestStatistics) addStorageSnapshotResidualModelLocked(apiName, modelName string, modelSt *modelStats) {
	if s == nil || modelSt == nil {
		return
	}
	apiName = strings.TrimSpace(apiName)
	if apiName == "" {
		apiName = "未知接口"
	}
	apiSt, ok := s.apis[apiName]
	if !ok {
		apiSt = &apiStats{Models: make(map[string]*modelStats), Sources: make(map[string]*sourceStatAccumulator)}
		s.apis[apiName] = apiSt
	}
	apiSt.TotalRequests = addNonNegativeInt64(apiSt.TotalRequests, modelSt.TotalRequests)
	apiSt.SuccessCount = addNonNegativeInt64(apiSt.SuccessCount, modelSt.SuccessCount)
	apiSt.FailureCount = addNonNegativeInt64(apiSt.FailureCount, modelSt.FailureCount)
	apiSt.TotalTokens = addNonNegativeInt64(apiSt.TotalTokens, modelSt.TotalTokens)
	apiSt.InputTokens = addNonNegativeInt64(apiSt.InputTokens, modelSt.InputTokens)
	apiSt.OutputTokens = addNonNegativeInt64(apiSt.OutputTokens, modelSt.OutputTokens)
	apiSt.CachedTokens = addNonNegativeInt64(apiSt.CachedTokens, modelSt.CachedTokens)
	apiSt.CacheWriteTokens = addNonNegativeInt64(apiSt.CacheWriteTokens, modelSt.CacheWriteTokens)
	apiSt.ReasoningTokens = addNonNegativeInt64(apiSt.ReasoningTokens, modelSt.ReasoningTokens)
	apiSt.latencySum = addNonNegativeInt64(apiSt.latencySum, modelSt.latencySum)
	apiSt.latencyN = addNonNegativeInt64(apiSt.latencyN, modelSt.latencyN)
	if existing, ok := apiSt.Models[modelName]; ok && existing != nil {
		existing.TotalRequests = addNonNegativeInt64(existing.TotalRequests, modelSt.TotalRequests)
		existing.SuccessCount = addNonNegativeInt64(existing.SuccessCount, modelSt.SuccessCount)
		existing.FailureCount = addNonNegativeInt64(existing.FailureCount, modelSt.FailureCount)
		existing.TotalTokens = addNonNegativeInt64(existing.TotalTokens, modelSt.TotalTokens)
		existing.InputTokens = addNonNegativeInt64(existing.InputTokens, modelSt.InputTokens)
		existing.OutputTokens = addNonNegativeInt64(existing.OutputTokens, modelSt.OutputTokens)
		existing.CachedTokens = addNonNegativeInt64(existing.CachedTokens, modelSt.CachedTokens)
		existing.CacheWriteTokens = addNonNegativeInt64(existing.CacheWriteTokens, modelSt.CacheWriteTokens)
		existing.ReasoningTokens = addNonNegativeInt64(existing.ReasoningTokens, modelSt.ReasoningTokens)
		existing.latencySum = addNonNegativeInt64(existing.latencySum, modelSt.latencySum)
		existing.latencyN = addNonNegativeInt64(existing.latencyN, modelSt.latencyN)
		existing.providerStats = mergeModelProviderStats(existing.providerStats, modelSt.providerStats)
	} else {
		apiSt.Models[modelName] = modelSt
	}
	s.mergeModelSummaryAggregateLocked(modelName, modelSt)
}

func normalizeStorageSnapshotDetail(modelName string, detail RequestDetail, now time.Time) RequestDetail {
	detail.Model = normalizeDetailModelName(modelName, detail.Model)
	detail.CostUSD = nil
	if detail.Timestamp.IsZero() {
		detail.Timestamp = now
		detail.TimestampSynthetic = true
	}
	if detail.LatencyMs < 0 {
		detail.LatencyMs = 0
	}
	if detail.TTFTMs < 0 {
		detail.TTFTMs = 0
	}
	detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
	detail.Source = cleanImportedDetailSource(detail)
	// Restored details from native records carry the CPA auth index for their
	// auth ID; seed the learner so fallback records grouped after a restart
	// land in the same credential group.
	authIndexes.Learn(detail.AuthID, detail.AuthIndex)
	return normalizeStoredClientAPIIdentity(detail)
}

func normalizeDetailModelName(fallback string, model string) string {
	modelName := normalizeModelName(model)
	if modelName == "unknown" {
		modelName = normalizeModelName(fallback)
	}
	return modelName
}

func storageSnapshotModelName(modelName string, modelSnapshot ModelSnapshot) string {
	modelName = normalizeModelName(modelName)
	for _, detail := range modelSnapshot.Details {
		return storageSnapshotDetailModelName(modelName, detail)
	}
	return modelName
}

func storageSnapshotDetailModelName(modelName string, detail RequestDetail) string {
	return normalizeDetailModelName(modelName, detail.Model)
}

func storageSnapshotNeedsSplitModelRestore(modelName string, modelSnapshot ModelSnapshot) bool {
	var first string
	modelName = normalizeModelName(modelName)
	for _, detail := range modelSnapshot.Details {
		key := storageSnapshotDetailModelName(modelName, detail)
		if key != modelName {
			return true
		}
		if first == "" {
			first = key
			continue
		}
		if key != first {
			return true
		}
	}
	return false
}

func storageSnapshotHasMultipleAPIGroupNames(apiName string, apiSnapshot APISnapshot) bool {
	var first string
	for _, modelSnapshot := range apiSnapshot.Models {
		for _, detail := range modelSnapshot.Details {
			key := storageSnapshotDetailAPIName(apiName, detail)
			if first == "" {
				first = key
				continue
			}
			if key != first {
				return true
			}
		}
	}
	return false
}

func storageSnapshotResidualAPIName(apiName string, apiSnapshot APISnapshot) string {
	apiName = strings.TrimSpace(apiName)
	for _, modelSnapshot := range apiSnapshot.Models {
		for _, detail := range modelSnapshot.Details {
			if clean := cleanStorageSnapshotFallbackAPIName(apiName, detail); clean != "" {
				return clean
			}
		}
	}
	apiName = stripUpstreamChannelSuffix(stripCredentialSuffix(apiName))
	if apiName == "" || looksLikeSecretKey(apiName) || looksLikeCredentialID(apiName) {
		return "未知接口"
	}
	return apiName
}

func cleanStorageSnapshotFallbackAPIName(apiName string, detail RequestDetail) string {
	clean := stripUpstreamChannelSuffix(stripAPIKeySuffix(apiName, detail.APIKey, detail.AuthIndex, detail.AuthID))
	clean = strings.TrimSpace(clean)
	if clean != "" && !looksLikeSecretKey(clean) && !looksLikeCredentialID(clean) {
		return clean
	}
	if source := cleanImportedDetailSource(detail); source != "" {
		return source
	}
	provider := strings.TrimSpace(detail.Provider)
	if provider != "" && !looksLikeSecretKey(provider) && !looksLikeCredentialID(provider) {
		return provider
	}
	return ""
}

func stripUpstreamChannelSuffix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := splitBySeparators(value)
	if len(parts) > 1 && strings.HasPrefix(strings.TrimSpace(parts[len(parts)-1]), "上游 ") {
		return strings.Join(parts[:len(parts)-1], " · ")
	}
	return value
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
	var cacheWriteTokens int64
	var reasoningTokens int64
	var latencySum int64
	var latencyN int64
	for _, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		inputTokens = addNonNegativeInt64(inputTokens, modelSt.InputTokens)
		outputTokens = addNonNegativeInt64(outputTokens, modelSt.OutputTokens)
		cachedTokens = addNonNegativeInt64(cachedTokens, modelSt.CachedTokens)
		cacheWriteTokens = addNonNegativeInt64(cacheWriteTokens, modelSt.CacheWriteTokens)
		reasoningTokens = addNonNegativeInt64(reasoningTokens, modelSt.ReasoningTokens)
		latencySum = addNonNegativeInt64(latencySum, modelSt.latencySum)
		latencyN = addNonNegativeInt64(latencyN, modelSt.latencyN)
	}
	apiSt.InputTokens = maxInt64(apiSt.InputTokens, inputTokens)
	apiSt.OutputTokens = maxInt64(apiSt.OutputTokens, outputTokens)
	apiSt.CachedTokens = maxInt64(apiSt.CachedTokens, cachedTokens)
	apiSt.CacheWriteTokens = maxInt64(apiSt.CacheWriteTokens, cacheWriteTokens)
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
	var cacheWriteTokens int64
	var reasoningTokens int64
	var latencySum int64
	var latencyN int64
	for _, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		inputTokens = addNonNegativeInt64(inputTokens, apiSt.InputTokens)
		outputTokens = addNonNegativeInt64(outputTokens, apiSt.OutputTokens)
		cachedTokens = addNonNegativeInt64(cachedTokens, apiSt.CachedTokens)
		cacheWriteTokens = addNonNegativeInt64(cacheWriteTokens, apiSt.CacheWriteTokens)
		reasoningTokens = addNonNegativeInt64(reasoningTokens, apiSt.ReasoningTokens)
		latencySum = addNonNegativeInt64(latencySum, apiSt.latencySum)
		latencyN = addNonNegativeInt64(latencyN, apiSt.latencyN)
	}
	s.inputTokens = maxInt64(s.inputTokens, inputTokens)
	s.outputTokens = maxInt64(s.outputTokens, outputTokens)
	s.cachedTokens = maxInt64(s.cachedTokens, cachedTokens)
	s.cacheWriteTokens = maxInt64(s.cacheWriteTokens, cacheWriteTokens)
	s.reasoningTokens = maxInt64(s.reasoningTokens, reasoningTokens)
	if s.latencyN == 0 && latencyN > 0 {
		s.latencySum = latencySum
		s.latencyN = latencyN
	}
}

func storageSnapshotAPIName(apiName string, apiSnapshot APISnapshot) string {
	for _, modelSnapshot := range apiSnapshot.Models {
		for _, detail := range modelSnapshot.Details {
			return storageSnapshotDetailAPIName(apiName, detail)
		}
	}
	return apiName
}

func storageSnapshotDetailAPIName(apiName string, detail RequestDetail) string {
	detail.Source = cleanImportedDetailSource(detail)
	if key := usageGroupKeyFromDetail(apiName, detail); strings.TrimSpace(key) != "" {
		return key
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
	stat.TotalRequests = addNonNegativeInt64(stat.TotalRequests, modelSt.TotalRequests)
	stat.SuccessCount = addNonNegativeInt64(stat.SuccessCount, modelSt.SuccessCount)
	stat.FailureCount = addNonNegativeInt64(stat.FailureCount, modelSt.FailureCount)
	stat.TotalTokens = addNonNegativeInt64(stat.TotalTokens, modelSt.TotalTokens)
	stat.InputTokens = addNonNegativeInt64(stat.InputTokens, modelSt.InputTokens)
	stat.OutputTokens = addNonNegativeInt64(stat.OutputTokens, modelSt.OutputTokens)
	stat.CachedTokens = addNonNegativeInt64(stat.CachedTokens, modelSt.CachedTokens)
	stat.CacheWriteTokens = addNonNegativeInt64(stat.CacheWriteTokens, modelSt.CacheWriteTokens)
	stat.ReasoningTokens = addNonNegativeInt64(stat.ReasoningTokens, modelSt.ReasoningTokens)
	stat.latencySum = addNonNegativeInt64(stat.latencySum, modelSt.latencySum)
	stat.latencyN = addNonNegativeInt64(stat.latencyN, modelSt.latencyN)
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
		existing.TotalRequests = addNonNegativeInt64(existing.TotalRequests, providerStat.TotalRequests)
		existing.SuccessCount = addNonNegativeInt64(existing.SuccessCount, providerStat.SuccessCount)
		existing.FailureCount = addNonNegativeInt64(existing.FailureCount, providerStat.FailureCount)
		existing.TotalTokens = addNonNegativeInt64(existing.TotalTokens, providerStat.TotalTokens)
		existing.InputTokens = addNonNegativeInt64(existing.InputTokens, providerStat.InputTokens)
		existing.OutputTokens = addNonNegativeInt64(existing.OutputTokens, providerStat.OutputTokens)
		existing.CachedTokens = addNonNegativeInt64(existing.CachedTokens, providerStat.CachedTokens)
		existing.CacheWriteTokens = addNonNegativeInt64(existing.CacheWriteTokens, providerStat.CacheWriteTokens)
		existing.ReasoningTokens = addNonNegativeInt64(existing.ReasoningTokens, providerStat.ReasoningTokens)
	}
}

func copyStringInt64Map(values map[string]int64) map[string]int64 {
	if values == nil {
		return make(map[string]int64)
	}
	copied := make(map[string]int64, len(values))
	for key, value := range values {
		copied[key] = nonNegativeInt64(value)
	}
	return copied
}

func copyStringFloat64Map(values map[string]float64) map[string]float64 {
	if values == nil {
		return make(map[string]float64)
	}
	copied := make(map[string]float64, len(values))
	for key, value := range values {
		copied[key] = normalizedNonNegativeCost(value)
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
		result[hour] = nonNegativeInt64(value)
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
		result[hour] = normalizedNonNegativeCost(value)
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
		value.TotalTokens = nonNegativeInt64(value.TotalTokens)
		value.InputTokens = nonNegativeInt64(value.InputTokens)
		value.OutputTokens = nonNegativeInt64(value.OutputTokens)
		value.CachedTokens = nonNegativeInt64(value.CachedTokens)
		value.CacheWriteTokens = nonNegativeInt64(value.CacheWriteTokens)
		value.ReasoningTokens = nonNegativeInt64(value.ReasoningTokens)
		key := timeSeriesTokenKey(value.Model, value.Provider)
		if existing, ok := result[key]; ok {
			existing.TotalTokens = addNonNegativeInt64(existing.TotalTokens, value.TotalTokens)
			existing.InputTokens = addNonNegativeInt64(existing.InputTokens, value.InputTokens)
			existing.OutputTokens = addNonNegativeInt64(existing.OutputTokens, value.OutputTokens)
			existing.CachedTokens = addNonNegativeInt64(existing.CachedTokens, value.CachedTokens)
			existing.CacheWriteTokens = addNonNegativeInt64(existing.CacheWriteTokens, value.CacheWriteTokens)
			existing.ReasoningTokens = addNonNegativeInt64(existing.ReasoningTokens, value.ReasoningTokens)
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
		Version:     currentStorageSnapshotVersion,
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

// migrateLegacySnapshotCacheReads converts snapshot v1 aggregates, where
// CachedTokens stored cache reads plus cache creation, to the v2 meaning of
// cache reads only. Detail records already keep read/write/total separately.
func migrateLegacySnapshotCacheReads(snapshot *StatisticsSnapshot) {
	if snapshot == nil {
		return
	}
	snapshot.CachedTokens = legacyCacheReadTokens(snapshot.CachedTokens, snapshot.CacheWriteTokens)
	for apiName, api := range snapshot.APIs {
		api.CachedTokens = legacyCacheReadTokens(api.CachedTokens, api.CacheWriteTokens)
		for modelName, model := range api.Models {
			model.CachedTokens = legacyCacheReadTokens(model.CachedTokens, model.CacheWriteTokens)
			for i := range model.Providers {
				model.Providers[i].CachedTokens = legacyCacheReadTokens(model.Providers[i].CachedTokens, model.Providers[i].CacheWriteTokens)
			}
			api.Models[modelName] = model
		}
		snapshot.APIs[apiName] = api
	}
	for key, values := range snapshot.CostTokensByDay {
		for i := range values {
			values[i].CachedTokens = legacyCacheReadTokens(values[i].CachedTokens, values[i].CacheWriteTokens)
		}
		snapshot.CostTokensByDay[key] = values
	}
	for key, values := range snapshot.CostTokensByHour {
		for i := range values {
			values[i].CachedTokens = legacyCacheReadTokens(values[i].CachedTokens, values[i].CacheWriteTokens)
		}
		snapshot.CostTokensByHour[key] = values
	}
}

func legacyCacheReadTokens(cachedTokens, cacheWriteTokens int64) int64 {
	return maxInt64(nonNegativeInt64(cachedTokens)-nonNegativeInt64(cacheWriteTokens), 0)
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
	var records []persistedDetail
	var invalidLines int
	readFile := func(path string) {
		path = filepath.Clean(path)
		if _, seen := seenFiles[path]; seen {
			return
		}
		seenFiles[path] = struct{}{}
		fileRecords, invalid, err := readPersistedStorageFile(path)
		if err != nil {
			warnings = append(warnings, err.Error())
			return
		}
		records = append(records, fileRecords...)
		invalidLines += invalid
	}
	if strings.TrimSpace(legacyPath) != "" && snapshotAt.IsZero() {
		readFile(legacyPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(records) > 0 || invalidLines > 0 {
				if err := s.replayPersistedDetailsLocked(records, invalidLines, true, now); err != nil {
					warnings = append(warnings, err.Error())
				}
			}
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
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	for _, path := range files {
		readFile(path)
	}
	// A protocol fallback and its native counterpart are not guaranteed to be
	// written to the same daily shard.  Reconcile the complete replay set once,
	// before adding any of it to the aggregates, so a fallback in one file can
	// consume a native record in another file (or in the snapshot restored just
	// before this function).  Metadata-only records are kept in the same batch so
	// their order-independent pending update handling also spans file boundaries.
	if len(records) > 0 || invalidLines > 0 {
		if err := s.replayPersistedDetailsLocked(records, invalidLines, true, now); err != nil {
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

func readPersistedStorageFile(path string) ([]persistedDetail, int, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	var records []persistedDetail
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
		records = append(records, persisted)
	}
	if err := scanner.Err(); err != nil {
		return nil, invalidLines, fmt.Errorf("scan storage %s: %w", path, err)
	}
	return records, invalidLines, nil
}

func (s *RequestStatistics) replayPersistedDetailsLocked(records []persistedDetail, invalidLines int, reconcileBatch bool, now time.Time) error {
	if s == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if reconcileBatch {
		records, _ = reconcilePersistedProtocolFallbacks(records)
	}
	s.reconcileRecordedProtocolFallbacksLocked(now)
	existing := s.detailKeysLocked()
	pendingMetadata := make(map[requestDedupKey]RequestDetail)
	for _, persisted := range records {
		apiName := strings.TrimSpace(persisted.API)
		detail := persisted.Detail
		modelName := normalizeDetailModelName(persisted.Model, detail.Model)
		detail.Model = modelName
		if detail.Timestamp.IsZero() {
			detail.Timestamp = now
			detail.TimestampSynthetic = true
		}
		detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
		detail.Source = cleanImportedDetailSource(detail)
		apiName = usageGroupKeyFromDetail(apiName, detail)
		detail = normalizeStoredClientAPIIdentity(detail)
		if s.claudeCacheRepairEnabled {
			detail = normalizeClaudeCacheFallbackDetail(detail)
		}
		key := dedupKey(apiName, modelName, detail)
		canonicalKey := claudeCacheCanonicalDedupKey(apiName, modelName, detail)
		if persisted.MetadataOnly {
			if !s.enrichPersistedDetailMetadataLocked(apiName, modelName, key, detail) {
				if pending, ok := pendingMetadata[key]; ok {
					enrichRequestDetailMetadata(&pending, detail)
					pendingMetadata[key] = pending
				} else {
					pendingMetadata[key] = detail
				}
			}
			continue
		}
		if _, ok := existing[canonicalKey]; ok {
			if pending, ok := pendingMetadata[key]; ok {
				s.enrichPersistedDetailMetadataLocked(apiName, modelName, key, pending)
				delete(pendingMetadata, key)
			}
			continue
		}
		if s.recordDetailLocked(apiName, modelName, detail, key, now, false) {
			existing[canonicalKey] = struct{}{}
			if pending, ok := pendingMetadata[key]; ok {
				s.enrichPersistedDetailMetadataLocked(apiName, modelName, key, pending)
				delete(pendingMetadata, key)
			}
		}
	}
	s.repairMigratedAttributionDetailsLocked(now)
	s.reconcileRecordedProtocolFallbacksLocked(now)
	if invalidLines > 0 {
		return fmt.Errorf("replay storage skipped %d invalid line(s)", invalidLines)
	}
	return nil
}

func (s *RequestStatistics) replayStorageLocked(path string) error {
	records, invalidLines, err := readPersistedStorageFile(path)
	if err != nil {
		return fmt.Errorf("read storage %s: %w", path, err)
	}
	return s.replayPersistedDetailsLocked(records, invalidLines, true, time.Now())
}

func (s *RequestStatistics) enrichPersistedDetailMetadataLocked(apiName, modelName string, target requestDedupKey, update RequestDetail) bool {
	if s == nil {
		return false
	}
	apiSt := s.apis[apiName]
	if apiSt == nil || apiSt.Models[modelName] == nil {
		return false
	}
	details := apiSt.Models[modelName].Details
	for i := len(details) - 1; i >= 0; i-- {
		if dedupKey(apiName, modelName, details[i]) != target {
			continue
		}
		if !enrichRequestDetailMetadata(&details[i], update) {
			return false
		}
		apiSt.Models[modelName].Details = details
		s.invalidateSummaryLocked()
		return true
	}
	return false
}

func (s *RequestStatistics) detailKeysLocked() map[requestDedupKey]struct{} {
	keys := make(map[requestDedupKey]struct{}, nonNegativeIntFromInt64(s.countDetailsLocked()))
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				keys[claudeCacheCanonicalDedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}
	return keys
}

// claudeCacheCanonicalDedupKey 在污染形态与修复形态之间取同一个去重键:无论
// claude_cache_repair_enabled 处于何种状态、磁盘上留存的是哪种形态,同一请求
// 在快照与 JSONL 分片间都不会因形态差异被重复入账。
func claudeCacheCanonicalDedupKey(apiName, modelName string, detail RequestDetail) requestDedupKey {
	return dedupKey(apiName, modelName, normalizeClaudeCacheFallbackDetail(detail))
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

func validExchangeRate(value float64) bool {
	return value >= 5 && value <= 12 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validFallbackExchangeRate(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func configureHTTPSURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("exchange rate URL must be HTTPS")
	}
	return parsed, nil
}

// exchangeRateCheckRedirect 阻止汇率接口经由重定向降级到明文 HTTP。配置项允许用户
// 自定义 URL,只在请求发起前校验 scheme 是不够的。
func exchangeRateCheckRedirect(req *http.Request, via []*http.Request) error {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" {
		return errors.New("exchange rate redirect must remain HTTPS")
	}
	if len(via) >= 10 {
		return errors.New("exchange rate redirect chain is too long")
	}
	return nil
}

func (s *RequestStatistics) configureExchangeRateWorkerLocked() {
	if s == nil || !s.exchangeRateEnabled {
		return
	}
	if s.exchangeRateRefresh <= 0 {
		s.exchangeRateRefresh = time.Duration(defaultExchangeRateRefreshSeconds) * time.Second
	}
	if s.exchangeRateTimeout <= 0 {
		s.exchangeRateTimeout = time.Duration(defaultExchangeRateTimeoutSeconds) * time.Second
	}
	if _, err := configureHTTPSURL(s.exchangeRateURL); err != nil {
		s.exchangeRateLastError = err.Error()
		return
	}
	if s.exchangeRateStop != nil {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.exchangeRateStop, s.exchangeRateDone = stop, done
	go s.exchangeRateWorker(stop, done)
}

func (s *RequestStatistics) stopExchangeRateWorker() {
	if s == nil {
		return
	}
	s.mu.Lock()
	stop, done := s.exchangeRateStop, s.exchangeRateDone
	s.exchangeRateStop, s.exchangeRateDone = nil, nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
}

func (s *RequestStatistics) exchangeRateWorker(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	s.refreshExchangeRateOnce(stop)
	for {
		s.mu.RLock()
		interval := s.exchangeRateRefresh
		s.mu.RUnlock()
		if interval <= 0 {
			interval = time.Duration(defaultExchangeRateRefreshSeconds) * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
			s.refreshExchangeRateOnce(stop)
		}
	}
}

func (s *RequestStatistics) refreshExchangeRateOnce(stop <-chan struct{}) {
	if s == nil {
		return
	}
	s.mu.RLock()
	enabled, rawURL, timeout := s.exchangeRateEnabled, s.exchangeRateURL, s.exchangeRateTimeout
	s.mu.RUnlock()
	if !enabled {
		return
	}
	parsed, err := configureHTTPSURL(rawURL)
	if err != nil {
		s.recordExchangeRateFailure(err)
		return
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		s.recordExchangeRateFailure(err)
		return
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: exchangeRateCheckRedirect}
	resp, err := client.Do(req)
	if err != nil {
		s.recordExchangeRateFailure(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.recordExchangeRateFailure(fmt.Errorf("exchange rate returned HTTP %d", resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		s.recordExchangeRateFailure(err)
		return
	}
	rate, err := parseExchangeRate(raw)
	if err != nil {
		s.recordExchangeRateFailure(err)
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	if !s.exchangeRateEnabled || s.exchangeRateURL != rawURL {
		s.mu.Unlock()
		return
	}
	s.exchangeRate = rate
	s.exchangeRateSource = parsed.Host
	s.exchangeRateFetchedAt = now
	s.exchangeRateLastError = ""
	s.exchangeRateFailures = 0
	s.currencyVersion++
	s.invalidateSummaryLocked()
	s.mu.Unlock()
}

func parseExchangeRate(raw []byte) (float64, error) {
	var payload struct {
		Rates           map[string]float64 `json:"rates"`
		ConversionRates map[string]float64 `json:"conversion_rates"`
		Data            map[string]float64 `json:"data"`
		USDCNY          float64            `json:"usd_cny"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, err
	}
	rate := payload.USDCNY
	if rate == 0 {
		rate = payload.Rates["CNY"]
	}
	if rate == 0 {
		rate = payload.ConversionRates["CNY"]
	}
	if rate == 0 {
		rate = payload.Data["CNY"]
	}
	if !validExchangeRate(rate) {
		return 0, errors.New("exchange rate response contains an invalid USD/CNY rate")
	}
	return rate, nil
}

func (s *RequestStatistics) recordExchangeRateFailure(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchangeRateFailures++
	if err != nil {
		s.exchangeRateLastError = err.Error()
	}
	s.currencyVersion++
	s.invalidateSummaryLocked()
}

func (s *RequestStatistics) currencyStateLocked(now time.Time) CurrencyState {
	state := CurrencyState{Base: "USD", SupportedDisplay: []string{"USD"}, Status: "disabled"}
	if !s.exchangeRateEnabled {
		return state
	}
	state.SupportedDisplay = []string{"USD", "CNY"}
	state.USDCNYRate = s.exchangeRate
	state.Source = s.exchangeRateSource
	state.Error = s.exchangeRateLastError
	state.ConsecutiveFails = s.exchangeRateFailures
	if !s.exchangeRateFetchedAt.IsZero() {
		state.FetchedAt = s.exchangeRateFetchedAt.Format(time.RFC3339)
		expires := s.exchangeRateFetchedAt.Add(2 * s.exchangeRateRefresh)
		state.ExpiresAt = expires.Format(time.RFC3339)
		// 过了有效期就是 stale,无论有没有记录到拉取错误:系统休眠、定时器被推迟
		// 或 worker 没被及时调度时都不会留下 error,但汇率确实已经过期。
		switch {
		case !now.Before(expires):
			state.Status = "stale"
		case s.exchangeRateLastError == "":
			state.Status = "fresh"
		default:
			state.Status = "cached"
		}
	} else if validFallbackExchangeRate(s.exchangeRateFallback) {
		state.USDCNYRate = s.exchangeRateFallback
		state.Status = "fallback"
	} else {
		state.Status = "stale"
	}
	return state
}

type modelsDevModelPayload struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	LastUpdated string                `json:"last_updated"`
	Cost        *modelsDevCostPayload `json:"cost"`
}

type modelsDevCostPayload struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
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
		s.modelsDevPriceIndex = make(map[string]ModelPrice)
		// 默认价格表被清空也是一次价格变动:不自增版本号,派生缓存(时段价格标志、
		// summary ETag)会继续沿用清空前的结论。
		s.priceVersion++
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
	s.modelsDevPriceIndex = normalizedModelPriceIndex(prices)
	s.priceVersion++
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
	// Do not infer a cache-write rate when the price source does not publish
	// one. Unknown prices default to zero and can be overridden manually.
	cacheWrite := 0.0
	if cost.CacheWrite != nil {
		cacheWrite = *cost.CacheWrite
	}
	price := ModelPrice{Prompt: *cost.Input, Completion: *cost.Output, Cache: cache, CacheWrite: cacheWrite}
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
	path, abs, legacyAbs, err := resolvePriceStoragePaths(s.priceStoragePath)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return
	}
	if s.priceStorageLoadedPath == abs {
		return
	}
	raw, err := os.ReadFile(abs)
	loadedFromLegacy := false
	if errors.Is(err, os.ErrNotExist) && legacyAbs != "" && legacyAbs != abs {
		raw, err = os.ReadFile(legacyAbs)
		loadedFromLegacy = err == nil
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.priceStoragePath = path
			s.priceStorageLoadedPath = abs
			s.modelPrices = make(map[string]ModelPrice)
			s.modelPriceIndex = make(map[string]ModelPrice)
			s.priceVersion++
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
		// 旧价格文件里的规则可能没有 ID(校验不再代劳补齐),这里统一补上,
		// 否则重叠报错和前端的规则行都会拿到空标识。
		setModelPriceCaseInsensitive(prices, name, normalizeModelPriceRules(price))
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPrices = prices
	s.modelPriceIndex = normalizedModelPriceIndex(prices)
	s.priceVersion++
	s.modelPricesUpdatedAt = parseRFC3339OrZero(persisted.UpdatedAt)
	s.priceStorageLastError = ""
	if loadedFromLegacy {
		// 旧版本默认把价格写在 CPA 工作目录根部。把内容复制到 data 目录后，
		// CPA/插件更新只要保留数据目录就不会再丢失价格；保留旧文件作为用户
		// 可回滚的备份，不做删除操作。
		if err := writeModelPricesFile(abs, s.modelPricesUpdatedAt, s.modelPrices); err != nil {
			s.priceStorageLastError = fmt.Sprintf("migrate legacy price file: %v", err)
		}
	}
}

func (s *RequestStatistics) saveModelPricesLocked() error {
	if s == nil {
		return nil
	}
	path, abs, _, err := resolvePriceStoragePaths(s.priceStoragePath)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	updatedAt := time.Now().UTC()
	if err := writeModelPricesFile(abs, updatedAt, s.modelPrices); err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPricesUpdatedAt = updatedAt
	s.priceStorageLastError = ""
	return nil
}

// resolvePriceStoragePaths makes the new data-directory location the canonical
// target while recognizing the old default path as a one-time migration source.
// Explicit custom paths keep their configured semantics.
func resolvePriceStoragePaths(configured string) (path, abs, legacyAbs string, err error) {
	path = strings.TrimSpace(configured)
	if path == "" {
		path = defaultRuntimeConfig().PriceStoragePath
	}
	abs, err = filepath.Abs(path)
	if err != nil {
		return "", "", "", err
	}
	defaultAbs, err := filepath.Abs(defaultPriceStoragePath)
	if err != nil {
		return "", "", "", err
	}
	legacyAbs, err = filepath.Abs(legacyPriceStoragePath)
	if err != nil {
		return "", "", "", err
	}
	if abs == defaultAbs {
		return defaultPriceStoragePath, defaultAbs, legacyAbs, nil
	}
	return path, abs, "", nil
}

func writeModelPricesFile(abs string, updatedAt time.Time, prices map[string]ModelPrice) error {
	if strings.TrimSpace(abs) == "" {
		return errors.New("price storage path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	payload := struct {
		UpdatedAt string                `json:"updated_at"`
		Prices    map[string]ModelPrice `json:"prices"`
	}{
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Prices:    copyModelPrices(prices),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return err
	}
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
	if !validPriceNumber(price.Prompt) || !validPriceNumber(price.Completion) || !validPriceNumber(price.Cache) || !validPriceNumber(price.CacheWrite) {
		return false
	}
	return validateModelPriceRules(price.TimeRules) == nil
}

func validPriceNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// validateModelPriceRules 必须是纯校验:它曾经在这里给缺失的 rule.ID 赋值,而
// UpsertModelPrice 是在加锁之前调用它的,等于在锁外写调用方(乃至已入库)的切片。
// 补 ID 的动作移到了 normalizeModelPriceRules,在锁内对副本执行。
func validateModelPriceRules(rules []ModelPriceRule) error {
	if len(rules) > 16 {
		return errors.New("time_rules must contain at most 16 rules")
	}
	seenIDs := make(map[string]struct{}, len(rules))
	intervals := make([]modelPriceRuleInterval, 0, len(rules)*2)
	for i := range rules {
		rule := rules[i]
		prefix := fmt.Sprintf("time_rules[%d]", i)
		if strings.TrimSpace(rule.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		// 空 ID 由 normalizeModelPriceRules 补,只在显式给出的 ID 之间查重。
		if id := strings.TrimSpace(rule.ID); id != "" {
			if _, ok := seenIDs[id]; ok {
				return fmt.Errorf("%s.id must be unique", prefix)
			}
			seenIDs[id] = struct{}{}
		}
		if len(rule.Days) > 7 {
			return fmt.Errorf("%s.days must contain at most 7 weekdays", prefix)
		}
		seenDays := make(map[int]struct{}, len(rule.Days))
		for _, day := range rule.Days {
			if day < 0 || day > 6 {
				return fmt.Errorf("%s.days must be between 0 (Sunday) and 6 (Saturday)", prefix)
			}
			if _, dup := seenDays[day]; dup {
				return fmt.Errorf("%s.days must not repeat a weekday", prefix)
			}
			seenDays[day] = struct{}{}
		}
		start, err := parsePricingMinute(rule.Start)
		if err != nil {
			return fmt.Errorf("%s.start must use HH:mm", prefix)
		}
		end, err := parsePricingMinute(rule.End)
		if err != nil {
			return fmt.Errorf("%s.end must use HH:mm", prefix)
		}
		if start == end {
			return fmt.Errorf("%s.start and end must differ", prefix)
		}
		if rule.Prompt != nil && !validPriceNumber(*rule.Prompt) {
			return fmt.Errorf("%s.prompt must be a non-negative finite number", prefix)
		}
		if rule.Completion != nil && !validPriceNumber(*rule.Completion) {
			return fmt.Errorf("%s.completion must be a non-negative finite number", prefix)
		}
		if rule.Cache != nil && !validPriceNumber(*rule.Cache) {
			return fmt.Errorf("%s.cache must be a non-negative finite number", prefix)
		}
		if rule.CacheWrite != nil && !validPriceNumber(*rule.CacheWrite) {
			return fmt.Errorf("%s.cache_write must be a non-negative finite number", prefix)
		}
		if rule.Prompt == nil && rule.Completion == nil && rule.Cache == nil && rule.CacheWrite == nil {
			return fmt.Errorf("%s must override at least one price", prefix)
		}
		days := modelPriceRuleDayMask(rule)
		// 跨午夜的规则拆成两段,两段都带同一份星期掩码:00:00 之后的那半段按它
		// 实际落在的那一天判定(见 ModelPriceRule.Days 的注释)。
		if start < end {
			intervals = append(intervals, modelPriceRuleInterval{start: start, end: end, rule: i, days: days})
		} else {
			intervals = append(intervals,
				modelPriceRuleInterval{start: start, end: 1440, rule: i, days: days},
				modelPriceRuleInterval{start: 0, end: end, rule: i, days: days})
		}
	}
	for i := range intervals {
		for j := i + 1; j < len(intervals); j++ {
			// 星期不相交的两条规则永远不会在同一时刻同时命中,时间段重叠也无妨:
			// 「工作日 22:00-06:00 半价」和「周末 22:00-06:00 全价」必须能共存。
			if intervals[i].days&intervals[j].days == 0 {
				continue
			}
			if intervals[i].start < intervals[j].end && intervals[j].start < intervals[i].end {
				return fmt.Errorf("time_rules overlap: %s and %s", modelPriceRuleLabel(rules[intervals[i].rule]), modelPriceRuleLabel(rules[intervals[j].rule]))
			}
		}
	}
	return nil
}

type modelPriceRuleInterval struct {
	start int
	end   int
	rule  int
	days  uint8
}

const allWeekdaysMask uint8 = 0x7F

// modelPriceRuleDayMask 把 Days 映射成位掩码,bit0 = 周日 … bit6 = 周六。
// 空列表表示每天,这既是历史价格文件的语义,也是无效值的安全回落——校验会先
// 拒掉越界的 day,存量数据万一带上了也按「每天」处理,不会让规则静默失效。
func modelPriceRuleDayMask(rule ModelPriceRule) uint8 {
	if len(rule.Days) == 0 {
		return allWeekdaysMask
	}
	var mask uint8
	for _, day := range rule.Days {
		if day >= 0 && day <= 6 {
			mask |= 1 << uint(day)
		}
	}
	if mask == 0 {
		return allWeekdaysMask
	}
	return mask
}

// modelPriceRuleLabel 用于错误信息:ID 是可选的(新建规则时由后端补),没有 ID 时用
// 名称,避免报出 "time_rules overlap:  and " 这种无从定位的空串。
func modelPriceRuleLabel(rule ModelPriceRule) string {
	if id := strings.TrimSpace(rule.ID); id != "" {
		return id
	}
	if name := strings.TrimSpace(rule.Name); name != "" {
		return name
	}
	return "?"
}

// normalizeModelPriceRules 返回价格的深拷贝:补齐缺失的规则 ID,并复制 TimeRules 切片
// 和其中的 *float64。浅拷贝会让入库的价格与调用方(以及返回给 HTTP 层的响应)共享同
// 一份底层数组和指针,任何一方在锁外改动都会直接改到存储里。
func normalizeModelPriceRules(price ModelPrice) ModelPrice {
	if len(price.TimeRules) == 0 {
		price.TimeRules = nil
		return price
	}
	rules := make([]ModelPriceRule, len(price.TimeRules))
	for i, rule := range price.TimeRules {
		rule.Prompt = copyFloatPtr(rule.Prompt)
		rule.Completion = copyFloatPtr(rule.Completion)
		rule.Cache = copyFloatPtr(rule.Cache)
		rule.CacheWrite = copyFloatPtr(rule.CacheWrite)
		// Days 同样要深拷贝:与 TimeRules 切片一样,浅拷贝会让入库价格和调用方
		// 共享底层数组,任何一方在锁外改动都会直接改到存储里。
		rule.Days = normalizedRuleDays(rule.Days)
		if strings.TrimSpace(rule.ID) == "" {
			rule.ID = newModelPriceRuleID()
		}
		rules[i] = rule
	}
	price.TimeRules = rules
	return price
}

// normalizedRuleDays 去重并排序星期列表;覆盖整周时收敛成 nil,让「每天」在存储
// 和导出里只有一种表示,也让旧价格文件与新写入的全周规则完全同形。
func normalizedRuleDays(days []int) []int {
	if len(days) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(days))
	normalized := make([]int, 0, len(days))
	for _, day := range days {
		if day < 0 || day > 6 {
			continue
		}
		if _, dup := seen[day]; dup {
			continue
		}
		seen[day] = struct{}{}
		normalized = append(normalized, day)
	}
	if len(normalized) == 0 || len(normalized) == 7 {
		return nil
	}
	sort.Ints(normalized)
	return normalized
}

func copyFloatPtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func parsePricingMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, errors.New("invalid HH:mm")
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, errors.New("invalid HH:mm")
	}
	return hour*60 + minute, nil
}

func ruleContainsMinute(rule ModelPriceRule, minute int) bool {
	start, err1 := parsePricingMinute(rule.Start)
	end, err2 := parsePricingMinute(rule.End)
	if err1 != nil || err2 != nil || start == end {
		return false
	}
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

// ruleMatchesInstant 在时间段之上再判星期。跨午夜规则按请求实际落在的那一天
// 判定,而不是按区间起始的那一天(见 ModelPriceRule.Days)。
func ruleMatchesInstant(rule ModelPriceRule, weekday time.Weekday, minute int) bool {
	if modelPriceRuleDayMask(rule)&(1<<uint(weekday)) == 0 {
		return false
	}
	return ruleContainsMinute(rule, minute)
}

func effectivePrice(base ModelPrice, at time.Time, location *time.Location) ModelPrice {
	if at.IsZero() || len(base.TimeRules) == 0 {
		return base
	}
	if location == nil {
		location = time.UTC
	}
	local := at.In(location)
	minute := local.Hour()*60 + local.Minute()
	weekday := local.Weekday()
	for _, rule := range base.TimeRules {
		if !ruleMatchesInstant(rule, weekday, minute) {
			continue
		}
		result := base
		if rule.Prompt != nil {
			result.Prompt = *rule.Prompt
		}
		if rule.Completion != nil {
			result.Completion = *rule.Completion
		}
		if rule.Cache != nil {
			result.Cache = *rule.Cache
		}
		if rule.CacheWrite != nil {
			result.CacheWrite = *rule.CacheWrite
		}
		return result
	}
	return base
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func copyModelPrices(source map[string]ModelPrice) map[string]ModelPrice {
	copy := make(map[string]ModelPrice, len(source))
	for model, price := range source {
		// 深拷贝 TimeRules:浅拷贝会把入库价格的切片和 *float64 交给 HTTP 响应,
		// 调用方在锁外改动就会直接改到存储里。
		copy[model] = normalizeModelPriceRules(price)
	}
	return copy
}

func normalizedModelPriceIndex(source map[string]ModelPrice) map[string]ModelPrice {
	index := make(map[string]ModelPrice, len(source))
	for model, price := range source {
		if key := normalizeModelPriceKey(model); key != "" {
			index[key] = price
		}
	}
	return index
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
		// 必须深拷贝:这份 map 会直接进 HTTP 响应,浅拷贝会把存储里的 TimeRules
		// 切片和 *float64 一并交出去。
		setModelPriceCaseInsensitive(result, model, normalizeModelPriceRules(price))
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

func (s *RequestStatistics) QueryModelPrices(scope, query string, limit, offset int) ModelPricesResponse {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}, ManualPrices: map[string]ModelPrice{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	response := s.modelPricesResponseLocked()
	// The used-price representation depends on modelSummaryStats, which is
	// versioned by summaryVersion. Capture both under the same lock so the
	// response body and its conditional-request validator describe one snapshot.
	response.dashboardVersion = s.summaryVersion
	scope = strings.ToLower(strings.TrimSpace(scope))
	query = normalizeModelPriceKey(query)
	if scope == "" && query == "" && limit <= 0 && offset <= 0 {
		response.Total = len(response.Prices)
		return response
	}
	source := response.Prices
	if scope == "used" {
		source = s.usedModelPricesLocked(response.Prices)
	}
	if scope == "manual" {
		source = response.ManualPrices
	}
	keys := make([]string, 0, len(source))
	for key := range source {
		if query == "" || strings.Contains(normalizeModelPriceKey(key), query) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return normalizeModelPriceKey(keys[i]) < normalizeModelPriceKey(keys[j])
	})
	response.Total = len(keys)
	if (scope == "used" || scope == "manual") && query == "" {
		response.Prices = copyModelPrices(source)
		response.Limit = len(source)
		response.Offset = 0
		return response
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(keys) {
		offset = len(keys)
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	end := offset + limit
	if end > len(keys) {
		end = len(keys)
	}
	response.Prices = make(map[string]ModelPrice, end-offset)
	for _, key := range keys[offset:end] {
		response.Prices[key] = source[key]
	}
	response.Limit = limit
	response.Offset = offset
	return response
}

func (s *RequestStatistics) usedModelPricesLocked(effective map[string]ModelPrice) map[string]ModelPrice {
	used := make(map[string]ModelPrice)
	if s == nil || len(effective) == 0 {
		return used
	}
	index := normalizedModelPriceIndex(effective)
	displayKeys := make(map[string]string, len(effective))
	for key := range effective {
		displayKeys[normalizeModelPriceKey(key)] = key
	}
	add := func(model, provider string) {
		for _, candidate := range modelPriceLookupKeys(model, provider) {
			norm := normalizeModelPriceKey(candidate)
			price, ok := index[norm]
			if !ok {
				continue
			}
			key := displayKeys[norm]
			if key == "" {
				key = candidate
			}
			used[key] = price
			return
		}
	}
	for modelName, model := range s.modelSummaryStats {
		if model == nil || len(model.providerStats) == 0 {
			add(modelName, "")
			continue
		}
		for _, provider := range model.providerStats {
			if provider != nil {
				add(modelName, provider.Provider)
			}
		}
	}
	return used
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
	if !validPriceNumber(price.Prompt) || !validPriceNumber(price.Completion) || !validPriceNumber(price.Cache) || !validPriceNumber(price.CacheWrite) {
		return ModelPricesResponse{}, errors.New("price values must be non-negative finite numbers")
	}
	if err := validateModelPriceRules(price.TimeRules); err != nil {
		return ModelPricesResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	if s.modelPrices == nil {
		s.modelPrices = make(map[string]ModelPrice)
	}
	// 在锁内对副本补 ID 并深拷贝,入库的价格从此不与调用方共享任何底层数据。
	setModelPriceCaseInsensitive(s.modelPrices, model, normalizeModelPriceRules(price))
	s.modelPriceIndex = normalizedModelPriceIndex(s.modelPrices)
	// 内存价格表已经改了,版本号必须在这里自增:落盘失败会提前返回,若把自增留到
	// 后面,失败路径会留下「表变了但版本没变」的状态,派生缓存(时段价格标志、
	// summary ETag)就会继续用旧结论。
	s.priceVersion++
	if err := s.saveModelPricesLocked(); err != nil {
		s.invalidateSummaryLocked()
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
	s.modelPriceIndex = normalizedModelPriceIndex(s.modelPrices)
	s.priceVersion++
	if err := s.saveModelPricesLocked(); err != nil {
		s.invalidateSummaryLocked()
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
		Version:   s.priceVersion,
	}
	if !s.modelPricesUpdatedAt.IsZero() {
		response.UpdatedAt = s.modelPricesUpdatedAt.UTC().Format(time.RFC3339)
	}
	return response
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) detailTotals {
	totals := detailTotalsFromRequest(detail)
	cost := s.detailCostLocked(model, detail, totals)
	apiSt.TotalRequests = addNonNegativeInt64(apiSt.TotalRequests, 1)
	if detail.Failed {
		apiSt.FailureCount = addNonNegativeInt64(apiSt.FailureCount, 1)
	} else {
		apiSt.SuccessCount = addNonNegativeInt64(apiSt.SuccessCount, 1)
	}
	apiSt.TotalTokens = addNonNegativeInt64(apiSt.TotalTokens, totals.totalTokens)
	apiSt.InputTokens = addNonNegativeInt64(apiSt.InputTokens, totals.inputTokens)
	apiSt.OutputTokens = addNonNegativeInt64(apiSt.OutputTokens, totals.outputTokens)
	apiSt.CachedTokens = addNonNegativeInt64(apiSt.CachedTokens, totals.cachedTokens)
	apiSt.CacheWriteTokens = addNonNegativeInt64(apiSt.CacheWriteTokens, totals.cacheWriteTokens)
	apiSt.ReasoningTokens = addNonNegativeInt64(apiSt.ReasoningTokens, totals.reasoningTokens)
	apiSt.latencySum = addNonNegativeInt64(apiSt.latencySum, totals.latencySum)
	apiSt.latencyN = addNonNegativeInt64(apiSt.latencyN, totals.latencyN)
	apiSt.estimatedCost = addNonNegativeCost(apiSt.estimatedCost, cost)

	modelSt, ok := apiSt.Models[model]
	if !ok {
		modelSt = &modelStats{}
		apiSt.Models[model] = modelSt
	}
	modelSt.TotalRequests = addNonNegativeInt64(modelSt.TotalRequests, 1)
	if detail.Failed {
		modelSt.FailureCount = addNonNegativeInt64(modelSt.FailureCount, 1)
	} else {
		modelSt.SuccessCount = addNonNegativeInt64(modelSt.SuccessCount, 1)
	}
	modelSt.TotalTokens = addNonNegativeInt64(modelSt.TotalTokens, totals.totalTokens)
	modelSt.InputTokens = addNonNegativeInt64(modelSt.InputTokens, totals.inputTokens)
	modelSt.OutputTokens = addNonNegativeInt64(modelSt.OutputTokens, totals.outputTokens)
	modelSt.CachedTokens = addNonNegativeInt64(modelSt.CachedTokens, totals.cachedTokens)
	modelSt.CacheWriteTokens = addNonNegativeInt64(modelSt.CacheWriteTokens, totals.cacheWriteTokens)
	modelSt.ReasoningTokens = addNonNegativeInt64(modelSt.ReasoningTokens, totals.reasoningTokens)
	modelSt.latencySum = addNonNegativeInt64(modelSt.latencySum, totals.latencySum)
	modelSt.latencyN = addNonNegativeInt64(modelSt.latencyN, totals.latencyN)
	modelSt.estimatedCost = addNonNegativeCost(modelSt.estimatedCost, cost)
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
	sourceAgg.providers[detail.Provider] = addNonNegativeInt64(sourceAgg.providers[detail.Provider], 1)
	sourceAgg.stat.TotalRequests = addNonNegativeInt64(sourceAgg.stat.TotalRequests, 1)
	if detail.Failed {
		sourceAgg.stat.FailureCount = addNonNegativeInt64(sourceAgg.stat.FailureCount, 1)
	} else {
		sourceAgg.stat.SuccessCount = addNonNegativeInt64(sourceAgg.stat.SuccessCount, 1)
	}
	sourceAgg.stat.TotalTokens = addNonNegativeInt64(sourceAgg.stat.TotalTokens, totals.totalTokens)
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
	sourceAgg.stat.TotalRequests = subtractNonNegativeInt64(sourceAgg.stat.TotalRequests, 1)
	if detail.Failed {
		sourceAgg.stat.FailureCount = subtractNonNegativeInt64(sourceAgg.stat.FailureCount, 1)
	} else {
		sourceAgg.stat.SuccessCount = subtractNonNegativeInt64(sourceAgg.stat.SuccessCount, 1)
	}
	sourceAgg.stat.TotalTokens = subtractNonNegativeInt64(sourceAgg.stat.TotalTokens, totals.totalTokens)
	if sourceAgg.providers != nil {
		sourceAgg.providers[detail.Provider] = subtractNonNegativeInt64(sourceAgg.providers[detail.Provider], 1)
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
	modelStat.TotalRequests = addNonNegativeInt64(modelStat.TotalRequests, 1)
	if detail.Failed {
		modelStat.FailureCount = addNonNegativeInt64(modelStat.FailureCount, 1)
	} else {
		modelStat.SuccessCount = addNonNegativeInt64(modelStat.SuccessCount, 1)
	}
	modelStat.TotalTokens = addNonNegativeInt64(modelStat.TotalTokens, totals.totalTokens)
	modelStat.InputTokens = addNonNegativeInt64(modelStat.InputTokens, totals.inputTokens)
	modelStat.OutputTokens = addNonNegativeInt64(modelStat.OutputTokens, totals.outputTokens)
	modelStat.CachedTokens = addNonNegativeInt64(modelStat.CachedTokens, totals.cachedTokens)
	modelStat.CacheWriteTokens = addNonNegativeInt64(modelStat.CacheWriteTokens, totals.cacheWriteTokens)
	modelStat.ReasoningTokens = addNonNegativeInt64(modelStat.ReasoningTokens, totals.reasoningTokens)
	modelStat.latencySum = addNonNegativeInt64(modelStat.latencySum, totals.latencySum)
	modelStat.latencyN = addNonNegativeInt64(modelStat.latencyN, totals.latencyN)
	modelStat.providerStats = incrementModelProviderStats(modelStat.providerStats, detail.Provider, detail.Failed, totals)
	modelStat.EstimatedCost = addNonNegativeCost(modelStat.EstimatedCost, s.detailCostLocked(modelName, detail, totals))
}

func (s *RequestStatistics) decrementModelSummaryStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	modelStat, ok := s.modelSummaryStats[modelName]
	if !ok {
		return
	}
	modelStat.TotalRequests = subtractNonNegativeInt64(modelStat.TotalRequests, 1)
	if detail.Failed {
		modelStat.FailureCount = subtractNonNegativeInt64(modelStat.FailureCount, 1)
	} else {
		modelStat.SuccessCount = subtractNonNegativeInt64(modelStat.SuccessCount, 1)
	}
	modelStat.TotalTokens = subtractNonNegativeInt64(modelStat.TotalTokens, totals.totalTokens)
	modelStat.InputTokens = subtractNonNegativeInt64(modelStat.InputTokens, totals.inputTokens)
	modelStat.OutputTokens = subtractNonNegativeInt64(modelStat.OutputTokens, totals.outputTokens)
	modelStat.CachedTokens = subtractNonNegativeInt64(modelStat.CachedTokens, totals.cachedTokens)
	modelStat.CacheWriteTokens = subtractNonNegativeInt64(modelStat.CacheWriteTokens, totals.cacheWriteTokens)
	modelStat.ReasoningTokens = subtractNonNegativeInt64(modelStat.ReasoningTokens, totals.reasoningTokens)
	modelStat.latencySum = subtractNonNegativeInt64(modelStat.latencySum, totals.latencySum)
	modelStat.latencyN = subtractNonNegativeInt64(modelStat.latencyN, totals.latencyN)
	decrementModelProviderStats(modelStat.providerStats, detail.Provider, detail.Failed, totals)
	modelStat.EstimatedCost = subtractNonNegativeCost(modelStat.EstimatedCost, s.detailCostLocked(modelName, detail, totals))
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
	sourceAgg.providers[detail.Provider] = addNonNegativeInt64(sourceAgg.providers[detail.Provider], 1)
	sourceAgg.stat.TotalRequests = addNonNegativeInt64(sourceAgg.stat.TotalRequests, 1)
	if detail.Failed {
		sourceAgg.stat.FailureCount = addNonNegativeInt64(sourceAgg.stat.FailureCount, 1)
	} else {
		sourceAgg.stat.SuccessCount = addNonNegativeInt64(sourceAgg.stat.SuccessCount, 1)
	}
	sourceAgg.stat.TotalTokens = addNonNegativeInt64(sourceAgg.stat.TotalTokens, totals.totalTokens)

	credential := summaryCredentialKey(detail)
	credentialStat, ok := s.credentialStats[credential]
	if !ok {
		credentialStat = &CredentialStat{AuthIndex: credential}
		s.credentialStats[credential] = credentialStat
	}
	credentialStat.TotalRequests = addNonNegativeInt64(credentialStat.TotalRequests, 1)
	if detail.Failed {
		credentialStat.FailureCount = addNonNegativeInt64(credentialStat.FailureCount, 1)
	} else {
		credentialStat.SuccessCount = addNonNegativeInt64(credentialStat.SuccessCount, 1)
	}
	credentialStat.TotalTokens = addNonNegativeInt64(credentialStat.TotalTokens, totals.totalTokens)

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
	clientAgg.stat.TotalRequests = addNonNegativeInt64(clientAgg.stat.TotalRequests, 1)
	if detail.Failed {
		clientAgg.stat.FailureCount = addNonNegativeInt64(clientAgg.stat.FailureCount, 1)
	} else {
		clientAgg.stat.SuccessCount = addNonNegativeInt64(clientAgg.stat.SuccessCount, 1)
	}
	clientAgg.stat.TotalTokens = addNonNegativeInt64(clientAgg.stat.TotalTokens, totals.totalTokens)
	clientAgg.stat.InputTokens = addNonNegativeInt64(clientAgg.stat.InputTokens, totals.inputTokens)
	clientAgg.stat.OutputTokens = addNonNegativeInt64(clientAgg.stat.OutputTokens, totals.outputTokens)
	clientAgg.stat.CachedTokens = addNonNegativeInt64(clientAgg.stat.CachedTokens, totals.cachedTokens)
	clientAgg.stat.CacheWriteTokens = addNonNegativeInt64(clientAgg.stat.CacheWriteTokens, totals.cacheWriteTokens)
	clientAgg.stat.ReasoningTokens = addNonNegativeInt64(clientAgg.stat.ReasoningTokens, totals.reasoningTokens)
	clientAgg.stat.EstimatedCost = addNonNegativeCost(clientAgg.stat.EstimatedCost, s.detailCostLocked(modelName, detail, totals))

	clientModel, ok := clientAgg.models[modelName]
	if !ok {
		clientModel = &ClientAPIModelStat{Model: modelName}
		clientAgg.models[modelName] = clientModel
	}
	clientModel.TotalRequests = addNonNegativeInt64(clientModel.TotalRequests, 1)
	if detail.Failed {
		clientModel.FailureCount = addNonNegativeInt64(clientModel.FailureCount, 1)
	} else {
		clientModel.SuccessCount = addNonNegativeInt64(clientModel.SuccessCount, 1)
	}
	clientModel.TotalTokens = addNonNegativeInt64(clientModel.TotalTokens, totals.totalTokens)
	clientModel.InputTokens = addNonNegativeInt64(clientModel.InputTokens, totals.inputTokens)
	clientModel.OutputTokens = addNonNegativeInt64(clientModel.OutputTokens, totals.outputTokens)
	clientModel.CachedTokens = addNonNegativeInt64(clientModel.CachedTokens, totals.cachedTokens)
	clientModel.CacheWriteTokens = addNonNegativeInt64(clientModel.CacheWriteTokens, totals.cacheWriteTokens)
	clientModel.ReasoningTokens = addNonNegativeInt64(clientModel.ReasoningTokens, totals.reasoningTokens)
	clientModel.providerStats = incrementModelProviderStats(clientModel.providerStats, detail.Provider, detail.Failed, totals)
	clientModel.EstimatedCost = addNonNegativeCost(clientModel.EstimatedCost, s.detailCostLocked(modelName, detail, totals))
}

func (s *RequestStatistics) decrementSummaryDimensionStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if sourceAgg, ok := s.sourceStats[summarySourceKey(detail)]; ok {
		sourceAgg.stat.TotalRequests = subtractNonNegativeInt64(sourceAgg.stat.TotalRequests, 1)
		if detail.Failed {
			sourceAgg.stat.FailureCount = subtractNonNegativeInt64(sourceAgg.stat.FailureCount, 1)
		} else {
			sourceAgg.stat.SuccessCount = subtractNonNegativeInt64(sourceAgg.stat.SuccessCount, 1)
		}
		sourceAgg.stat.TotalTokens = subtractNonNegativeInt64(sourceAgg.stat.TotalTokens, totals.totalTokens)
		if sourceAgg.providers != nil {
			sourceAgg.providers[detail.Provider] = subtractNonNegativeInt64(sourceAgg.providers[detail.Provider], 1)
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
		credentialStat.TotalRequests = subtractNonNegativeInt64(credentialStat.TotalRequests, 1)
		if detail.Failed {
			credentialStat.FailureCount = subtractNonNegativeInt64(credentialStat.FailureCount, 1)
		} else {
			credentialStat.SuccessCount = subtractNonNegativeInt64(credentialStat.SuccessCount, 1)
		}
		credentialStat.TotalTokens = subtractNonNegativeInt64(credentialStat.TotalTokens, totals.totalTokens)
		if credentialStat.TotalRequests <= 0 {
			delete(s.credentialStats, summaryCredentialKey(detail))
		}
	}

	clientKey := clientAPIGroupKey(detail)
	if clientAgg, ok := s.clientAPIStats[clientKey]; ok {
		clientAgg.stat.TotalRequests = subtractNonNegativeInt64(clientAgg.stat.TotalRequests, 1)
		if detail.Failed {
			clientAgg.stat.FailureCount = subtractNonNegativeInt64(clientAgg.stat.FailureCount, 1)
		} else {
			clientAgg.stat.SuccessCount = subtractNonNegativeInt64(clientAgg.stat.SuccessCount, 1)
		}
		clientAgg.stat.TotalTokens = subtractNonNegativeInt64(clientAgg.stat.TotalTokens, totals.totalTokens)
		clientAgg.stat.InputTokens = subtractNonNegativeInt64(clientAgg.stat.InputTokens, totals.inputTokens)
		clientAgg.stat.OutputTokens = subtractNonNegativeInt64(clientAgg.stat.OutputTokens, totals.outputTokens)
		clientAgg.stat.CachedTokens = subtractNonNegativeInt64(clientAgg.stat.CachedTokens, totals.cachedTokens)
		clientAgg.stat.CacheWriteTokens = subtractNonNegativeInt64(clientAgg.stat.CacheWriteTokens, totals.cacheWriteTokens)
		clientAgg.stat.ReasoningTokens = subtractNonNegativeInt64(clientAgg.stat.ReasoningTokens, totals.reasoningTokens)
		clientAgg.stat.EstimatedCost = subtractNonNegativeCost(clientAgg.stat.EstimatedCost, s.detailCostLocked(modelName, detail, totals))

		if clientModel, ok := clientAgg.models[modelName]; ok {
			clientModel.TotalRequests = subtractNonNegativeInt64(clientModel.TotalRequests, 1)
			if detail.Failed {
				clientModel.FailureCount = subtractNonNegativeInt64(clientModel.FailureCount, 1)
			} else {
				clientModel.SuccessCount = subtractNonNegativeInt64(clientModel.SuccessCount, 1)
			}
			clientModel.TotalTokens = subtractNonNegativeInt64(clientModel.TotalTokens, totals.totalTokens)
			clientModel.InputTokens = subtractNonNegativeInt64(clientModel.InputTokens, totals.inputTokens)
			clientModel.OutputTokens = subtractNonNegativeInt64(clientModel.OutputTokens, totals.outputTokens)
			clientModel.CachedTokens = subtractNonNegativeInt64(clientModel.CachedTokens, totals.cachedTokens)
			clientModel.CacheWriteTokens = subtractNonNegativeInt64(clientModel.CacheWriteTokens, totals.cacheWriteTokens)
			clientModel.ReasoningTokens = subtractNonNegativeInt64(clientModel.ReasoningTokens, totals.reasoningTokens)
			clientModel.EstimatedCost = subtractNonNegativeCost(clientModel.EstimatedCost, s.detailCostLocked(modelName, detail, totals))
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
		bucket.failure = addNonNegativeInt64(bucket.failure, 1)
	} else {
		bucket.success = addNonNegativeInt64(bucket.success, 1)
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
		bucket.failure = subtractNonNegativeInt64(bucket.failure, 1)
	} else {
		bucket.success = subtractNonNegativeInt64(bucket.success, 1)
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
	s.totalRequests = subtractNonNegativeInt64(s.totalRequests, 1)
	if d.Failed {
		s.failureCount = subtractNonNegativeInt64(s.failureCount, 1)
	} else {
		s.successCount = subtractNonNegativeInt64(s.successCount, 1)
	}
	s.totalTokens = subtractNonNegativeInt64(s.totalTokens, totals.totalTokens)
	s.inputTokens = subtractNonNegativeInt64(s.inputTokens, totals.inputTokens)
	s.outputTokens = subtractNonNegativeInt64(s.outputTokens, totals.outputTokens)
	s.cachedTokens = subtractNonNegativeInt64(s.cachedTokens, totals.cachedTokens)
	s.cacheWriteTokens = subtractNonNegativeInt64(s.cacheWriteTokens, totals.cacheWriteTokens)
	s.reasoningTokens = subtractNonNegativeInt64(s.reasoningTokens, totals.reasoningTokens)
	s.latencySum = subtractNonNegativeInt64(s.latencySum, totals.latencySum)
	s.latencyN = subtractNonNegativeInt64(s.latencyN, totals.latencyN)

	apiSt.TotalRequests = subtractNonNegativeInt64(apiSt.TotalRequests, 1)
	if d.Failed {
		apiSt.FailureCount = subtractNonNegativeInt64(apiSt.FailureCount, 1)
	} else {
		apiSt.SuccessCount = subtractNonNegativeInt64(apiSt.SuccessCount, 1)
	}
	apiSt.TotalTokens = subtractNonNegativeInt64(apiSt.TotalTokens, totals.totalTokens)
	apiSt.InputTokens = subtractNonNegativeInt64(apiSt.InputTokens, totals.inputTokens)
	apiSt.OutputTokens = subtractNonNegativeInt64(apiSt.OutputTokens, totals.outputTokens)
	apiSt.CachedTokens = subtractNonNegativeInt64(apiSt.CachedTokens, totals.cachedTokens)
	apiSt.CacheWriteTokens = subtractNonNegativeInt64(apiSt.CacheWriteTokens, totals.cacheWriteTokens)
	apiSt.ReasoningTokens = subtractNonNegativeInt64(apiSt.ReasoningTokens, totals.reasoningTokens)
	apiSt.latencySum = subtractNonNegativeInt64(apiSt.latencySum, totals.latencySum)
	apiSt.latencyN = subtractNonNegativeInt64(apiSt.latencyN, totals.latencyN)
	decrementAPISourceStats(apiSt, d, totals)

	modelSt.TotalRequests = subtractNonNegativeInt64(modelSt.TotalRequests, 1)
	if d.Failed {
		modelSt.FailureCount = subtractNonNegativeInt64(modelSt.FailureCount, 1)
	} else {
		modelSt.SuccessCount = subtractNonNegativeInt64(modelSt.SuccessCount, 1)
	}
	modelSt.TotalTokens = subtractNonNegativeInt64(modelSt.TotalTokens, totals.totalTokens)
	modelSt.InputTokens = subtractNonNegativeInt64(modelSt.InputTokens, totals.inputTokens)
	modelSt.OutputTokens = subtractNonNegativeInt64(modelSt.OutputTokens, totals.outputTokens)
	modelSt.CachedTokens = subtractNonNegativeInt64(modelSt.CachedTokens, totals.cachedTokens)
	modelSt.CacheWriteTokens = subtractNonNegativeInt64(modelSt.CacheWriteTokens, totals.cacheWriteTokens)
	modelSt.ReasoningTokens = subtractNonNegativeInt64(modelSt.ReasoningTokens, totals.reasoningTokens)
	modelSt.latencySum = subtractNonNegativeInt64(modelSt.latencySum, totals.latencySum)
	modelSt.latencyN = subtractNonNegativeInt64(modelSt.latencyN, totals.latencyN)
	decrementModelProviderStats(modelSt.providerStats, d.Provider, d.Failed, totals)

	dayKey := d.Timestamp.Format("2006-01-02")
	hourKey := d.Timestamp.Hour()
	cost := s.detailCostLocked(modelName, d, totals)
	// 与 updateAPIStats 的累加保持对称。存在时段价格时
	// applySummaryEstimatedCostsLocked 不再从聚合 token 重算 API 成本,漏减会让
	// 已被 retention 淘汰的请求永久留在 API/API 模型成本里。
	apiSt.estimatedCost = subtractNonNegativeCost(apiSt.estimatedCost, cost)
	modelSt.estimatedCost = subtractNonNegativeCost(modelSt.estimatedCost, cost)
	s.requestsByDay[dayKey] = subtractNonNegativeInt64(s.requestsByDay[dayKey], 1)
	s.requestsByHour[hourKey] = subtractNonNegativeInt64(s.requestsByHour[hourKey], 1)
	s.tokensByDay[dayKey] = subtractNonNegativeInt64(s.tokensByDay[dayKey], totals.totalTokens)
	s.tokensByHour[hourKey] = subtractNonNegativeInt64(s.tokensByHour[hourKey], totals.totalTokens)
	if decrementTimeSeriesTokenStats(s.costTokensByDay[dayKey], detailModel(modelName, d), d.Provider, totals) {
		s.costByDay[dayKey] = subtractNonNegativeCost(s.costByDay[dayKey], cost)
	}
	if len(s.costTokensByDay[dayKey]) == 0 {
		delete(s.costTokensByDay, dayKey)
	}
	if decrementTimeSeriesTokenStats(s.costTokensByHour[hourKey], detailModel(modelName, d), d.Provider, totals) {
		s.costByHour[hourKey] = subtractNonNegativeCost(s.costByHour[hourKey], cost)
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
	s.cacheWriteTokens = 0
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
		apiSt.CacheWriteTokens = 0
		apiSt.ReasoningTokens = 0
		apiSt.latencySum = 0
		apiSt.latencyN = 0
		apiSt.estimatedCost = 0
		apiSt.Sources = make(map[string]*sourceStatAccumulator)
		for modelName, modelSt := range apiSt.Models {
			modelSt.TotalRequests = 0
			modelSt.SuccessCount = 0
			modelSt.FailureCount = 0
			modelSt.TotalTokens = 0
			modelSt.InputTokens = 0
			modelSt.OutputTokens = 0
			modelSt.CachedTokens = 0
			modelSt.CacheWriteTokens = 0
			modelSt.ReasoningTokens = 0
			modelSt.latencySum = 0
			modelSt.latencyN = 0
			modelSt.estimatedCost = 0
			modelSt.providerStats = nil
			for _, detail := range modelSt.Details {
				totals := detailTotalsFromRequest(detail)
				s.totalRequests = addNonNegativeInt64(s.totalRequests, 1)
				apiSt.TotalRequests = addNonNegativeInt64(apiSt.TotalRequests, 1)
				modelSt.TotalRequests = addNonNegativeInt64(modelSt.TotalRequests, 1)
				if detail.Failed {
					s.failureCount = addNonNegativeInt64(s.failureCount, 1)
					apiSt.FailureCount = addNonNegativeInt64(apiSt.FailureCount, 1)
					modelSt.FailureCount = addNonNegativeInt64(modelSt.FailureCount, 1)
				} else {
					s.successCount = addNonNegativeInt64(s.successCount, 1)
					apiSt.SuccessCount = addNonNegativeInt64(apiSt.SuccessCount, 1)
					modelSt.SuccessCount = addNonNegativeInt64(modelSt.SuccessCount, 1)
				}
				s.totalTokens = addNonNegativeInt64(s.totalTokens, totals.totalTokens)
				s.inputTokens = addNonNegativeInt64(s.inputTokens, totals.inputTokens)
				s.outputTokens = addNonNegativeInt64(s.outputTokens, totals.outputTokens)
				s.cachedTokens = addNonNegativeInt64(s.cachedTokens, totals.cachedTokens)
				s.cacheWriteTokens = addNonNegativeInt64(s.cacheWriteTokens, totals.cacheWriteTokens)
				s.reasoningTokens = addNonNegativeInt64(s.reasoningTokens, totals.reasoningTokens)
				s.latencySum = addNonNegativeInt64(s.latencySum, totals.latencySum)
				s.latencyN = addNonNegativeInt64(s.latencyN, totals.latencyN)
				apiSt.TotalTokens = addNonNegativeInt64(apiSt.TotalTokens, totals.totalTokens)
				apiSt.InputTokens = addNonNegativeInt64(apiSt.InputTokens, totals.inputTokens)
				apiSt.OutputTokens = addNonNegativeInt64(apiSt.OutputTokens, totals.outputTokens)
				apiSt.CachedTokens = addNonNegativeInt64(apiSt.CachedTokens, totals.cachedTokens)
				apiSt.CacheWriteTokens = addNonNegativeInt64(apiSt.CacheWriteTokens, totals.cacheWriteTokens)
				apiSt.ReasoningTokens = addNonNegativeInt64(apiSt.ReasoningTokens, totals.reasoningTokens)
				apiSt.latencySum = addNonNegativeInt64(apiSt.latencySum, totals.latencySum)
				apiSt.latencyN = addNonNegativeInt64(apiSt.latencyN, totals.latencyN)
				incrementAPISourceStats(apiSt, detail, totals)
				modelSt.TotalTokens = addNonNegativeInt64(modelSt.TotalTokens, totals.totalTokens)
				modelSt.InputTokens = addNonNegativeInt64(modelSt.InputTokens, totals.inputTokens)
				modelSt.OutputTokens = addNonNegativeInt64(modelSt.OutputTokens, totals.outputTokens)
				modelSt.CachedTokens = addNonNegativeInt64(modelSt.CachedTokens, totals.cachedTokens)
				modelSt.CacheWriteTokens = addNonNegativeInt64(modelSt.CacheWriteTokens, totals.cacheWriteTokens)
				modelSt.ReasoningTokens = addNonNegativeInt64(modelSt.ReasoningTokens, totals.reasoningTokens)
				modelSt.latencySum = addNonNegativeInt64(modelSt.latencySum, totals.latencySum)
				modelSt.latencyN = addNonNegativeInt64(modelSt.latencyN, totals.latencyN)
				modelSt.providerStats = incrementModelProviderStats(modelSt.providerStats, detail.Provider, detail.Failed, totals)
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := detail.Timestamp.Hour()
				cost := s.detailCostLocked(modelName, detail, totals)
				apiSt.estimatedCost = addNonNegativeCost(apiSt.estimatedCost, cost)
				modelSt.estimatedCost = addNonNegativeCost(modelSt.estimatedCost, cost)
				s.requestsByDay[dayKey] = addNonNegativeInt64(s.requestsByDay[dayKey], 1)
				s.requestsByHour[hourKey] = addNonNegativeInt64(s.requestsByHour[hourKey], 1)
				s.tokensByDay[dayKey] = addNonNegativeInt64(s.tokensByDay[dayKey], totals.totalTokens)
				s.tokensByHour[hourKey] = addNonNegativeInt64(s.tokensByHour[hourKey], totals.totalTokens)
				s.costByDay[dayKey] = addNonNegativeCost(s.costByDay[dayKey], cost)
				s.costByHour[hourKey] = addNonNegativeCost(s.costByHour[hourKey], cost)
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
	// 时段差额只能叠加在「本次真正从 token 序列重建过」的序列上。若某条序列没有
	// token 基准可重建,它保留的是上一轮已经含 delta 的旧值,再加一次就会每保存一
	// 次价格膨胀一次(day/hour 两条守卫不对称时尤其明显)。
	rebuiltDay := len(s.costTokensByDay) > 0
	rebuiltHour := len(s.costTokensByHour) > 0
	if rebuiltDay {
		s.costByDay = s.costByDayFromTokenSeriesLocked()
	}
	if rebuiltHour {
		s.costByHour = s.costByHourFromTokenSeriesLocked()
	}
	if (rebuiltDay || rebuiltHour) && s.hasTimeBasedPricesLocked() {
		// TimeSeriesTokenStat deliberately aggregates records after their details
		// have been trimmed. It cannot retain each request's local time, so first
		// price the complete aggregate with the base price and then apply the
		// time-rule delta to the still-retained details. This preserves the cost
		// of evicted records instead of silently dropping it during a reprice.
		for _, api := range s.apis {
			for modelName, model := range api.Models {
				for _, detail := range model.Details {
					totals := detailTotalsFromRequest(detail)
					delta := s.detailTimePriceDeltaLocked(modelName, detail, totals)
					if rebuiltDay {
						day := detail.Timestamp.Format("2006-01-02")
						s.costByDay[day] = addCostDelta(s.costByDay[day], delta)
					}
					if rebuiltHour {
						hour := detail.Timestamp.Hour()
						s.costByHour[hour] = addCostDelta(s.costByHour[hour], delta)
					}
				}
			}
		}
	}
	s.rebuildEstimatedCostsLocked()
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
			result[day] = addNonNegativeCost(result[day], s.timeSeriesTokenCostLocked(*stat))
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
			result[hour] = addNonNegativeCost(result[hour], s.timeSeriesTokenCostLocked(*stat))
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
	result.CacheWriteTokens = s.cacheWriteTokens
	result.ReasoningTokens = s.reasoningTokens
	if s.latencyN > 0 {
		result.AvgLatencyMs = float64(s.latencySum) / float64(s.latencyN)
	}

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, apiSt := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests:    apiSt.TotalRequests,
			SuccessCount:     apiSt.SuccessCount,
			FailureCount:     apiSt.FailureCount,
			TotalTokens:      apiSt.TotalTokens,
			InputTokens:      apiSt.InputTokens,
			OutputTokens:     apiSt.OutputTokens,
			CachedTokens:     apiSt.CachedTokens,
			CacheWriteTokens: apiSt.CacheWriteTokens,
			ReasoningTokens:  apiSt.ReasoningTokens,
			Models:           make(map[string]ModelSnapshot, len(apiSt.Models)),
		}
		if apiSt.latencyN > 0 {
			apiSnapshot.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
		}
		for modelName, modelSt := range apiSt.Models {
			details := make([]RequestDetail, len(modelSt.Details))
			for i, detail := range modelSt.Details {
				details[i] = cloneRequestDetail(detail)
			}
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests:    modelSt.TotalRequests,
				SuccessCount:     modelSt.SuccessCount,
				FailureCount:     modelSt.FailureCount,
				TotalTokens:      modelSt.TotalTokens,
				InputTokens:      modelSt.InputTokens,
				OutputTokens:     modelSt.OutputTokens,
				CachedTokens:     modelSt.CachedTokens,
				CacheWriteTokens: modelSt.CacheWriteTokens,
				ReasoningTokens:  modelSt.ReasoningTokens,
				Providers:        finalizedModelProviderStats(modelSt.providerStats, modelSt.TotalRequests, modelSt.SuccessCount, modelSt.FailureCount, modelSt.TotalTokens, modelSt.InputTokens, modelSt.OutputTokens, modelSt.CachedTokens, modelSt.CacheWriteTokens, modelSt.ReasoningTokens),
				Details:          details,
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

func cloneRequestDetail(detail RequestDetail) RequestDetail {
	copy := detail
	copy.Correlation = cloneProtocolCorrelationMeta(detail.Correlation)
	if detail.Headers != nil {
		copy.Headers = make(map[string][]string, len(detail.Headers))
		for name, values := range detail.Headers {
			copy.Headers[name] = append([]string(nil), values...)
		}
	}
	return copy
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
	var reconciledProtocolFallbacks int
	snapshot, reconciledProtocolFallbacks = reconcileProtocolFallbackSnapshot(snapshot)
	result.Skipped = addNonNegativeInt64(result.Skipped, int64(reconciledProtocolFallbacks))
	s.reconcileRecordedProtocolFallbacksLocked(now)
	var cutoff time.Time
	if s.retention > 0 {
		cutoff = now.Add(-s.retention)
	}

	seen := make(map[requestDedupKey]struct{}, nonNegativeIntFromInt64(s.countDetailsLocked()+snapshotImportDetailCapacity(snapshot, cutoff, now)))
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				seen[claudeCacheCanonicalDedupKey(apiName, modelName, detail)] = struct{}{}
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
				importModelName := normalizeDetailModelName(modelName, detail.Model)
				detail.Model = importModelName
				detail.Tokens.TotalTokens = detailTotalTokensForRequest(detail)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = now
					detail.TimestampSynthetic = true
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.TTFTMs < 0 {
					detail.TTFTMs = 0
				}
				detail.Source = cleanImportedDetailSource(detail)
				importAPIName := usageGroupKeyFromDetail(apiName, detail)
				if persist {
					detail = normalizeImportedClientAPIIdentity(detail)
				} else {
					detail = normalizeStoredClientAPIIdentity(detail)
				}
				if s.claudeCacheRepairEnabled {
					detail = normalizeClaudeCacheFallbackDetail(detail)
				}

				if !cutoff.IsZero() && !detail.Timestamp.IsZero() && detail.Timestamp.Before(cutoff) {
					result.IgnoredByRetention = addNonNegativeInt64(result.IgnoredByRetention, 1)
					continue
				}

				key := claudeCacheCanonicalDedupKey(importAPIName, importModelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped = addNonNegativeInt64(result.Skipped, 1)
					continue
				}
				seen[key] = struct{}{}

				if s.recordImported(importAPIName, importModelName, detail, key, now) {
					if persist && s.storageEnabled {
						persisted = append(persisted, persistedDetail{API: importAPIName, Model: importModelName, Detail: detail})
					}
					result.Added = addNonNegativeInt64(result.Added, 1)
				}
			}
		}
	}

	s.pruneLocked(now, true)
	reconciledRecorded := s.reconcileRecordedProtocolFallbacksLocked(now)
	if reconciledRecorded > 0 {
		result.Skipped = addNonNegativeInt64(result.Skipped, int64(reconciledRecorded))
		result.Added = subtractNonNegativeInt64(result.Added, int64(reconciledRecorded))
	}
	if repaired := s.repairMigratedAttributionDetailsLocked(now); len(repaired) > 0 {
		removed := make(map[requestDedupKey]struct{}, len(repaired))
		for _, key := range repaired {
			removed[key] = struct{}{}
		}
		keptPersisted := persisted[:0]
		var droppedAdded int64
		for _, item := range persisted {
			if _, ok := removed[dedupKey(item.API, item.Model, item.Detail)]; ok {
				droppedAdded = addNonNegativeInt64(droppedAdded, 1)
				continue
			}
			keptPersisted = append(keptPersisted, item)
		}
		persisted = keptPersisted
		result.Skipped = addNonNegativeInt64(result.Skipped, int64(len(repaired)))
		result.Added = subtractNonNegativeInt64(result.Added, droppedAdded)
	}
	s.rebuildSeenLocked(now)
	return result, persisted
}

func (s *RequestStatistics) recordImported(apiName, modelName string, detail RequestDetail, dedup requestDedupKey, now time.Time) bool {
	return s.recordDetailLocked(apiName, modelName, detail, dedup, now, false)
}

func snapshotImportDetailCapacity(snapshot StatisticsSnapshot, cutoff time.Time, now time.Time) int64 {
	var count int64
	for apiName, apiSnapshot := range snapshot.APIs {
		if strings.TrimSpace(apiName) == "" {
			continue
		}
		for _, modelSnapshot := range apiSnapshot.Models {
			if cutoff.IsZero() {
				count = addNonNegativeInt64(count, int64(len(modelSnapshot.Details)))
				continue
			}
			for _, detail := range modelSnapshot.Details {
				timestamp := detail.Timestamp
				if timestamp.IsZero() {
					timestamp = now
				}
				if !timestamp.Before(cutoff) {
					count = addNonNegativeInt64(count, 1)
				}
			}
		}
	}
	return count
}

func usageDetailTotalTokens(detail UsageDetail, provider string) int64 {
	_, _, cacheTokens := usageDetailCacheTokenParts(detail, provider)
	inputTokens := nonNegativeInt64(detail.InputTokens)
	outputTokens := nonNegativeInt64(detail.OutputTokens)
	computedTokens := saturatingProtocolAdd(inputTokens, outputTokens)
	if usesExclusiveCacheInput(provider, inputTokens, outputTokens, cacheTokens, detail.TotalTokens) {
		computedTokens = saturatingProtocolAdd(computedTokens, cacheTokens)
	}
	return maxInt64(nonNegativeInt64(detail.TotalTokens), computedTokens)
}

func usageDetailCacheTokenParts(detail UsageDetail, provider string) (int64, int64, int64) {
	cacheReadTokens := maxInt64(nonNegativeInt64(detail.CachedTokens), nonNegativeInt64(detail.CacheReadTokens))
	cacheWriteTokens := nonNegativeInt64(detail.CacheCreationTokens)
	// CPA 的 parseClaudeUsageNode(仅 Claude 家族)在 cache_read 为 0 时把
	// cache_creation 回填进 CachedTokens。若不剔除,同一笔缓存创建会再计入一次
	// 缓存命中,并把总量多算一份。Claude 家族带真实命中的记录 CacheReadTokens
	// 一定非零,不受影响;其余 provider 不做该推断。
	if providerUsesExclusiveCacheInput(provider) &&
		nonNegativeInt64(detail.CacheReadTokens) == 0 && cacheWriteTokens > 0 &&
		nonNegativeInt64(detail.CachedTokens) == cacheWriteTokens {
		cacheReadTokens = 0
	}
	return cacheReadTokens, cacheWriteTokens, saturatingProtocolAdd(cacheReadTokens, cacheWriteTokens)
}

func detailTotalTokens(tokens TokenStats) int64 {
	computedTokens := saturatingProtocolAdd(nonNegativeInt64(tokens.InputTokens), nonNegativeInt64(tokens.OutputTokens))
	return maxInt64(nonNegativeInt64(tokens.TotalTokens), computedTokens)
}

func detailTotalTokensForRequest(detail RequestDetail) int64 {
	totalTokens := detailTotalTokens(detail.Tokens)
	inputTokens := nonNegativeInt64(detail.Tokens.InputTokens)
	outputTokens := nonNegativeInt64(detail.Tokens.OutputTokens)
	cacheTokens := normalizedCacheTokens(detail.Tokens)
	if usesExclusiveCacheInput(detail.Provider, inputTokens, outputTokens, cacheTokens, detail.Tokens.TotalTokens) {
		expandedTokens := saturatingProtocolAdd(inputTokens, outputTokens)
		expandedTokens = saturatingProtocolAdd(expandedTokens, cacheTokens)
		totalTokens = maxInt64(totalTokens, expandedTokens)
	}
	return totalTokens
}

func detailUncachedInputTokensForRequest(detail RequestDetail) int64 {
	inputTokens := nonNegativeInt64(detail.Tokens.InputTokens)
	if providerUsesExclusiveCacheInput(detail.Provider) {
		return inputTokens
	}
	cacheTokens := normalizedCacheTokens(detail.Tokens)
	if usesExclusiveCacheInput(detail.Provider, inputTokens, nonNegativeInt64(detail.Tokens.OutputTokens), cacheTokens, detail.Tokens.TotalTokens) {
		return inputTokens
	}
	return maxInt64(inputTokens-cacheTokens, 0)
}

func usesExclusiveCacheInput(provider string, inputTokens, outputTokens, cacheTokens, totalTokens int64) bool {
	if providerUsesExclusiveCacheInput(provider) {
		return true
	}
	computed := saturatingProtocolAdd(inputTokens, outputTokens)
	computed = saturatingProtocolAdd(computed, cacheTokens)
	return strings.TrimSpace(provider) == "" && totalTokens > 0 && totalTokens >= computed
}

func providerUsesExclusiveCacheInput(provider string) bool {
	return usageProviderFamily(provider) == "claude"
}

func detailTotalsFromRequest(detail RequestDetail) detailTotals {
	totals := detailTotals{
		totalTokens:      detailTotalTokensForRequest(detail),
		inputTokens:      nonNegativeInt64(detail.Tokens.InputTokens),
		outputTokens:     nonNegativeInt64(detail.Tokens.OutputTokens),
		cachedTokens:     normalizedCacheReadTokens(detail.Tokens),
		cacheWriteTokens: nonNegativeInt64(detail.Tokens.CacheWriteTokens),
		reasoningTokens:  nonNegativeInt64(detail.Tokens.ReasoningTokens),
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
	price, ok := s.basePriceForDetailLocked(modelName, detail)
	if !ok {
		return 0
	}
	price = effectiveDetailPrice(price, detail, s.pricingLocation)
	return tokenCostForPrice(detailTimeSeriesTokenStat(modelName, detail, totals), price)
}

// effectiveDetailPrice 在 effectivePrice 之上加一道回落:导入/恢复时被补出时间戳的
// 记录不得套用时段规则(图纸「无时间戳记录维持基础价」)。effectivePrice 自己的
// IsZero 守卫对这些记录已经失效——时间戳在它看到之前就被填上了。
func effectiveDetailPrice(base ModelPrice, detail RequestDetail, location *time.Location) ModelPrice {
	if detail.TimestampSynthetic {
		return base
	}
	return effectivePrice(base, detail.Timestamp, location)
}

// pricingSnapshot 冻结一次后台导出期间使用的价格表与计费时区。分页导出会在页与页
// 之间释放锁,期间用户改价或 models.dev 自动刷新都会让同一个文件前后页用上不同的
// 价格;快照让整份导出保持一致。
type pricingSnapshot struct {
	manualIndex map[string]ModelPrice
	devIndex    map[string]ModelPrice
	location    *time.Location
}

func (s *RequestStatistics) pricingSnapshotLocked() *pricingSnapshot {
	if s == nil {
		return &pricingSnapshot{location: time.UTC}
	}
	if len(s.modelPriceIndex) != len(s.modelPrices) {
		s.modelPriceIndex = normalizedModelPriceIndex(s.modelPrices)
	}
	if len(s.modelsDevPriceIndex) != len(s.modelsDevPrices) {
		s.modelsDevPriceIndex = normalizedModelPriceIndex(s.modelsDevPrices)
	}
	location := s.pricingLocation
	if location == nil {
		location = time.UTC
	}
	// 深拷贝:快照必须免疫后续改价。
	return &pricingSnapshot{
		manualIndex: copyModelPrices(s.modelPriceIndex),
		devIndex:    copyModelPrices(s.modelsDevPriceIndex),
		location:    location,
	}
}

// PricingLocation 返回当前计费时区。pricingLocation 由 s.mu 保护,直接读字段是一次
// 无同步访问(ConfigurePatch 会写它),调用方一律走这个访问器。
func (s *RequestStatistics) PricingLocation() *time.Location {
	if s == nil {
		return time.UTC
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pricingLocation == nil {
		return time.UTC
	}
	return s.pricingLocation
}

// PricingSnapshot 供后台导出在开始时冻结价格。
func (s *RequestStatistics) PricingSnapshot() *pricingSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pricingSnapshotLocked()
}

func (p *pricingSnapshot) detailCost(modelName string, detail RequestDetail, totals detailTotals) float64 {
	if p == nil {
		return 0
	}
	model := detailModel(modelName, detail)
	price, ok := priceForDetailFromIndex(p.manualIndex, model, detail.Provider)
	if !ok {
		if price, ok = priceForDetailFromIndex(p.devIndex, model, detail.Provider); !ok {
			return 0
		}
	}
	price = effectiveDetailPrice(price, detail, p.location)
	return tokenCostForPrice(detailTimeSeriesTokenStat(modelName, detail, totals), price)
}

func detailTimeSeriesTokenStat(modelName string, detail RequestDetail, totals detailTotals) TimeSeriesTokenStat {
	return TimeSeriesTokenStat{
		Model:            detailModel(modelName, detail),
		Provider:         detail.Provider,
		TotalTokens:      totals.totalTokens,
		InputTokens:      totals.inputTokens,
		OutputTokens:     totals.outputTokens,
		CachedTokens:     totals.cachedTokens,
		CacheWriteTokens: totals.cacheWriteTokens,
	}
}

func (s *RequestStatistics) basePriceForDetailLocked(modelName string, detail RequestDetail) (ModelPrice, bool) {
	return s.priceForDetailLocked(detailModel(modelName, detail), detail.Provider)
}

func (s *RequestStatistics) detailBaseCostLocked(modelName string, detail RequestDetail, totals detailTotals) float64 {
	if s == nil {
		return 0
	}
	price, ok := s.basePriceForDetailLocked(modelName, detail)
	if !ok {
		return 0
	}
	return tokenCostForPrice(detailTimeSeriesTokenStat(modelName, detail, totals), price)
}

func (s *RequestStatistics) detailTimePriceDeltaLocked(modelName string, detail RequestDetail, totals detailTotals) float64 {
	return s.detailCostLocked(modelName, detail, totals) - s.detailBaseCostLocked(modelName, detail, totals)
}

func (s *RequestStatistics) timeSeriesTokenCostLocked(stat TimeSeriesTokenStat) float64 {
	if s == nil {
		return 0
	}
	price, ok := s.priceForDetailLocked(stat.Model, stat.Provider)
	if !ok {
		return 0
	}
	return tokenCostForPrice(stat, price)
}

func tokenCostForPrice(stat TimeSeriesTokenStat, price ModelPrice) float64 {
	inputTokens := nonNegativeInt64(stat.InputTokens)
	outputTokens := nonNegativeInt64(stat.OutputTokens)
	totalTokens := nonNegativeInt64(stat.TotalTokens)
	cacheReadTokens := nonNegativeInt64(stat.CachedTokens)
	cacheWriteTokens := nonNegativeInt64(stat.CacheWriteTokens)
	cacheTotal := saturatingProtocolAdd(cacheReadTokens, cacheWriteTokens)
	uncachedInputTokens := maxInt64(inputTokens-cacheTotal, 0)
	if usesExclusiveCacheInput(stat.Provider, inputTokens, outputTokens, cacheTotal, totalTokens) {
		uncachedInputTokens = inputTokens
	}
	cost := 0.0
	cost = addNonNegativeCost(cost, scaledTokenCost(uncachedInputTokens, price.Prompt))
	cost = addNonNegativeCost(cost, scaledTokenCost(outputTokens, price.Completion))
	cost = addNonNegativeCost(cost, scaledTokenCost(cacheReadTokens, price.Cache))
	cost = addNonNegativeCost(cost, scaledTokenCost(cacheWriteTokens, price.CacheWrite))
	return cost
}

func scaledTokenCost(tokens int64, price float64) float64 {
	if tokens <= 0 || price <= 0 || math.IsNaN(price) || math.IsInf(price, -1) {
		return 0
	}
	if math.IsInf(price, 1) {
		return math.MaxFloat64
	}
	cost := float64(tokens) / 1e6 * price
	if math.IsInf(cost, 1) || math.IsNaN(cost) {
		return math.MaxFloat64
	}
	return cost
}

func (s *RequestStatistics) aggregateEstimatedCostLocked(model string, totalTokens, inputTokens, outputTokens, cachedTokens, cacheWriteTokens int64, providers []ModelProviderStat) float64 {
	if len(providers) == 0 {
		return s.timeSeriesTokenCostLocked(TimeSeriesTokenStat{
			Model:            model,
			TotalTokens:      totalTokens,
			InputTokens:      inputTokens,
			OutputTokens:     outputTokens,
			CachedTokens:     cachedTokens,
			CacheWriteTokens: cacheWriteTokens,
		})
	}
	var cost float64
	for _, provider := range providers {
		cost = addNonNegativeCost(cost, s.timeSeriesTokenCostLocked(TimeSeriesTokenStat{
			Model:            model,
			Provider:         provider.Provider,
			TotalTokens:      provider.TotalTokens,
			InputTokens:      provider.InputTokens,
			OutputTokens:     provider.OutputTokens,
			CachedTokens:     provider.CachedTokens,
			CacheWriteTokens: provider.CacheWriteTokens,
		}))
	}
	return cost
}

func (s *RequestStatistics) applySummaryEstimatedCostsLocked(summary *DashboardSummary) {
	if s == nil || summary == nil {
		return
	}
	if s.hasTimeBasedPricesLocked() {
		summary.Usage.TotalCost = 0
		for _, model := range summary.ModelStats {
			summary.Usage.TotalCost = addNonNegativeCost(summary.Usage.TotalCost, model.EstimatedCost)
		}
		return
	}
	summary.Usage.TotalCost = 0
	for i := range summary.ModelStats {
		model := &summary.ModelStats[i]
		model.EstimatedCost = s.aggregateEstimatedCostLocked(model.Model, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.Providers)
		summary.Usage.TotalCost = addNonNegativeCost(summary.Usage.TotalCost, model.EstimatedCost)
	}
	for apiName, api := range summary.Usage.APIs {
		api.EstimatedCost = 0
		for modelName, model := range api.Models {
			model.EstimatedCost = s.aggregateEstimatedCostLocked(modelName, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.Providers)
			api.EstimatedCost = addNonNegativeCost(api.EstimatedCost, model.EstimatedCost)
			api.Models[modelName] = model
		}
		summary.Usage.APIs[apiName] = api
	}
	for i := range summary.ClientAPIStats {
		client := &summary.ClientAPIStats[i]
		client.EstimatedCost = 0
		for j := range client.Models {
			model := &client.Models[j]
			model.EstimatedCost = s.aggregateEstimatedCostLocked(model.Model, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.Providers)
			client.EstimatedCost = addNonNegativeCost(client.EstimatedCost, model.EstimatedCost)
		}
	}
}

// hasTimeBasedPricesLocked 每次 summary / API 详情请求都会被问到,而 modelsDevPrices
// 有上千条,直接全表扫描等于每请求 O(价格表)。这里按 priceVersion 派生:价格表每次
// 变动都会自增该版本号,所以缓存不可能读到过期结论,也不需要在每个改价点手工同步
// 一个布尔标志(那种写法漏一个点就会让时段价格静默失效)。
//
// 注意:本方法会写缓存字段,必须在 s.mu 的**写锁**下调用,不能在 RLock 路径里用。
// TestTimeBasedPriceCacheIsRaceFreeUnderConcurrentPricing 会在 -race 下守住这一点。
func (s *RequestStatistics) hasTimeBasedPricesLocked() bool {
	if s == nil {
		return false
	}
	if !s.timeBasedPricesValid || s.timeBasedPricesVersion != s.priceVersion {
		s.timeBasedPricesVersion = s.priceVersion
		s.timeBasedPricesValid = true
		s.timeBasedPrices = mapHasTimeRules(s.modelPrices) || mapHasTimeRules(s.modelsDevPrices)
	}
	return s.timeBasedPrices
}

func mapHasTimeRules(prices map[string]ModelPrice) bool {
	for _, price := range prices {
		if len(price.TimeRules) > 0 {
			return true
		}
	}
	return false
}

func (s *RequestStatistics) rebuildEstimatedCostsLocked() {
	if s == nil {
		return
	}
	for _, api := range s.apis {
		api.estimatedCost = 0
		for modelName, model := range api.Models {
			model.estimatedCost = s.aggregateEstimatedCostLocked(modelName, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, finalizedModelProviderStats(model.providerStats, model.TotalRequests, model.SuccessCount, model.FailureCount, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.ReasoningTokens))
			api.estimatedCost = addNonNegativeCost(api.estimatedCost, model.estimatedCost)
		}
	}
	for modelName, model := range s.modelSummaryStats {
		model.EstimatedCost = s.aggregateEstimatedCostLocked(modelName, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, finalizedModelProviderStats(model.providerStats, model.TotalRequests, model.SuccessCount, model.FailureCount, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.ReasoningTokens))
	}
	for _, client := range s.clientAPIStats {
		client.stat.EstimatedCost = 0
		for modelName, model := range client.models {
			model.EstimatedCost = s.aggregateEstimatedCostLocked(modelName, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, finalizedModelProviderStats(model.providerStats, model.TotalRequests, model.SuccessCount, model.FailureCount, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.ReasoningTokens))
			client.stat.EstimatedCost = addNonNegativeCost(client.stat.EstimatedCost, model.EstimatedCost)
		}
	}
	if !s.hasTimeBasedPricesLocked() {
		return
	}
	for _, api := range s.apis {
		for modelName, model := range api.Models {
			for _, detail := range model.Details {
				delta := s.detailTimePriceDeltaLocked(modelName, detail, detailTotalsFromRequest(detail))
				api.estimatedCost = addCostDelta(api.estimatedCost, delta)
				model.estimatedCost = addCostDelta(model.estimatedCost, delta)
				if summaryModel := s.modelSummaryStats[modelName]; summaryModel != nil {
					summaryModel.EstimatedCost = addCostDelta(summaryModel.EstimatedCost, delta)
				}
				if client := s.clientAPIStats[clientAPIGroupKey(detail)]; client != nil {
					client.stat.EstimatedCost = addCostDelta(client.stat.EstimatedCost, delta)
					if clientModel := client.models[modelName]; clientModel != nil {
						clientModel.EstimatedCost = addCostDelta(clientModel.EstimatedCost, delta)
					}
				}
			}
		}
	}
}

func (s *RequestStatistics) applyModelEstimatedCostsLocked(models []ModelStat) float64 {
	if s.hasTimeBasedPricesLocked() {
		var total float64
		for i := range models {
			total = addNonNegativeCost(total, models[i].EstimatedCost)
		}
		return total
	}
	var total float64
	for i := range models {
		model := &models[i]
		model.EstimatedCost = s.aggregateEstimatedCostLocked(model.Model, model.TotalTokens, model.InputTokens, model.OutputTokens, model.CachedTokens, model.CacheWriteTokens, model.Providers)
		total = addNonNegativeCost(total, model.EstimatedCost)
	}
	return total
}

func (s *RequestStatistics) priceForDetailLocked(modelName, provider string) (ModelPrice, bool) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if len(s.modelPriceIndex) != len(s.modelPrices) {
		s.modelPriceIndex = normalizedModelPriceIndex(s.modelPrices)
	}
	if len(s.modelsDevPriceIndex) != len(s.modelsDevPrices) {
		s.modelsDevPriceIndex = normalizedModelPriceIndex(s.modelsDevPrices)
	}
	if price, ok := priceForDetailFromIndex(s.modelPriceIndex, modelName, provider); ok {
		return price, true
	}
	return priceForDetailFromIndex(s.modelsDevPriceIndex, modelName, provider)
}

func priceForDetailFromIndex(prices map[string]ModelPrice, modelName, provider string) (ModelPrice, bool) {
	modelName = normalizeModelPriceKey(modelName)
	provider = normalizeModelPriceKey(provider)
	if modelName == "" || len(prices) == 0 {
		return ModelPrice{}, false
	}
	if provider != "" {
		if price, ok := prices[provider+"/"+modelName]; ok {
			return price, true
		}
	}
	if price, ok := prices[modelName]; ok {
		return price, true
	}
	if idx := strings.IndexByte(modelName, '/'); idx > 0 && idx < len(modelName)-1 {
		if price, ok := prices[modelName[idx+1:]]; ok {
			return price, true
		}
	}
	return ModelPrice{}, false
}

// priceForDetailFromMap is kept for callers that provide an ad-hoc display
// map. Runtime cost aggregation uses the prebuilt normalized indexes above.
func priceForDetailFromMap(prices map[string]ModelPrice, modelName, provider string) (ModelPrice, bool) {
	return priceForDetailFromIndex(normalizedModelPriceIndex(prices), modelName, provider)
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
	if source == "" || looksLikeCredentialID(source) || looksLikeSecretKey(source) {
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
	cachedTokens := nonNegativeInt64(tokens.CachedTokens)
	cacheWriteTokens := nonNegativeInt64(tokens.CacheWriteTokens)
	cacheTokens := nonNegativeInt64(tokens.CacheTokens)
	if cacheTokens > 0 {
		return maxInt64(cacheTokens, maxInt64(cachedTokens, cacheWriteTokens))
	}
	return saturatingProtocolAdd(cachedTokens, cacheWriteTokens)
}

// normalizedCacheReadTokens returns cache hits only. CacheTokens stores the
// combined read+write total for compatibility with older snapshots, so using
// it directly as CachedTokens would count cache creation twice in summaries
// and costs.
func normalizedCacheReadTokens(tokens TokenStats) int64 {
	cachedTokens := nonNegativeInt64(tokens.CachedTokens)
	if cachedTokens > 0 {
		return cachedTokens
	}
	cacheTokens := nonNegativeInt64(tokens.CacheTokens)
	cacheWriteTokens := nonNegativeInt64(tokens.CacheWriteTokens)
	return maxInt64(cacheTokens-cacheWriteTokens, 0)
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// addNonNegativeInt64 is used for counters whose domain is non-negative. It
// deliberately saturates instead of wrapping around when a corrupted import
// or a very large long-running total reaches MaxInt64.
func addNonNegativeInt64(left, right int64) int64 {
	return saturatingProtocolAdd(left, right)
}

// subtractNonNegativeInt64 keeps aggregate counters at or above zero. In
// particular, fallback reconciliation and retention pruning must not turn a
// malformed or already-repaired aggregate into a negative statistic.
func subtractNonNegativeInt64(left, right int64) int64 {
	left = nonNegativeInt64(left)
	right = nonNegativeInt64(right)
	if left <= right {
		return 0
	}
	return left - right
}

// addNonNegativeCost is the floating-point counterpart of
// addNonNegativeInt64. Costs are derived from imported/user-controlled data,
// so invalid or overflowing values must not poison all subsequent summaries.
func addNonNegativeCost(left, right float64) float64 {
	left = normalizedNonNegativeCost(left)
	right = normalizedNonNegativeCost(right)
	if left == math.MaxFloat64 || right == math.MaxFloat64 {
		return math.MaxFloat64
	}
	sum := left + right
	if math.IsInf(sum, 1) || math.IsNaN(sum) {
		return math.MaxFloat64
	}
	return sum
}

func normalizedNonNegativeCost(value float64) float64 {
	switch {
	case math.IsNaN(value), value < 0, math.IsInf(value, -1):
		return 0
	case math.IsInf(value, 1):
		return math.MaxFloat64
	default:
		return value
	}
}

// addCostDelta applies a signed repricing delta while keeping the aggregate
// finite and non-negative. Unlike addNonNegativeCost, the delta may be below
// zero because a time rule can be cheaper than the base price.
func addCostDelta(left, delta float64) float64 {
	left = normalizedNonNegativeCost(left)
	if math.IsNaN(delta) || math.IsInf(delta, -1) {
		return 0
	}
	if math.IsInf(delta, 1) {
		return math.MaxFloat64
	}
	value := left + delta
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if math.IsInf(value, 1) {
		return math.MaxFloat64
	}
	return value
}

// subtractNonNegativeCost clamps a cost aggregate to zero. This is important
// when retention removes a record after a partial/malformed import.
func subtractNonNegativeCost(left, right float64) float64 {
	left = normalizedNonNegativeCost(left)
	right = normalizedNonNegativeCost(right)
	if left <= right {
		return 0
	}
	return normalizedNonNegativeCost(left - right)
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
		s.seen = make(map[requestDedupKey]time.Time)
		return
	}
	s.seen = make(map[requestDedupKey]time.Time)
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

func dedupKey(apiName, modelName string, detail RequestDetail) requestDedupKey {
	tokens := detail.Tokens
	key := requestDedupKey{
		apiName:          apiName,
		modelName:        modelName,
		timestamp:        detail.Timestamp.UTC().Round(0),
		source:           detail.Source,
		authIndex:        detail.AuthIndex,
		failure:          detail.Failure,
		failed:           detail.Failed,
		latencyMs:        detail.LatencyMs,
		ttftMs:           detail.TTFTMs,
		statusCode:       detail.StatusCode,
		inputTokens:      tokens.InputTokens,
		outputTokens:     tokens.OutputTokens,
		reasoning:        tokens.ReasoningTokens,
		cachedTokens:     tokens.CachedTokens,
		cacheTokens:      tokens.CacheTokens,
		cacheWriteTokens: tokens.CacheWriteTokens,
		totalTokens:      tokens.TotalTokens,
	}
	if hash := strings.TrimSpace(detail.APIKeyHash); hash != "" {
		key.clientAPIHash = hash
	} else if apiKey := strings.TrimSpace(detail.APIKey); apiKey != "" {
		key.clientAPIKey = apiKey
	}
	return key
}

// ============================================================================
// New P0 Methods: Lightweight Summary + Paginated Events
// ============================================================================

// SummaryWithoutDetails computes a lightweight dashboard summary without detail arrays.
func (s *RequestStatistics) SummaryWithoutDetails() DashboardSummary {
	return s.SummaryWithoutDetailsAt(time.Now())
}

func (s *RequestStatistics) SummaryWithoutDetailsAt(now time.Time) DashboardSummary {
	if s == nil {
		return DashboardSummary{}
	}

	startedAt := time.Now()
	if now.IsZero() {
		now = startedAt
	}
	healthWindow := summaryHealthWindow(now)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.summaryCacheValid && s.summaryCacheVersion == s.summaryVersion && s.summaryCacheWindow.Equal(healthWindow) {
		s.summaryCacheHits++
		s.lastSummaryDuration = time.Since(startedAt)
		return cloneDashboardSummary(s.summaryCache)
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
	return s.SummaryWithoutDetailsForRangeAt(rangeKey, time.Now())
}

func (s *RequestStatistics) SummaryWithoutDetailsForRangeAt(rangeKey string, now time.Time) DashboardSummary {
	return s.SummaryWithoutDetailsForRangeAndClientAPIAt(rangeKey, "", now)
}

func (s *RequestStatistics) SummaryWithoutDetailsForRangeAndClientAPIAt(rangeKey string, clientAPI string, now time.Time) DashboardSummary {
	if s == nil {
		return DashboardSummary{}
	}
	clientAPI = strings.TrimSpace(clientAPI)
	if clientAPI == "" && (rangeKey == "" || rangeKey == "all") {
		return s.SummaryWithoutDetailsAt(now)
	}

	startedAt := time.Now()
	if now.IsZero() {
		now = startedAt
	}
	healthWindow := summaryHealthWindow(now)
	cutoff := dashboardRangeCutoff(rangeKey, now)
	if cutoff.IsZero() && clientAPI == "" {
		return s.SummaryWithoutDetailsAt(now)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.summaryRangeCache == nil {
		s.summaryRangeCache = make(map[string]DashboardSummary)
		s.summaryRangeCacheWindow = make(map[string]time.Time)
	}
	cacheKey := summaryRangeClientAPICacheKey(rangeKey, clientAPI, now)
	cached, ok := s.summaryRangeCache[cacheKey]
	if ok && s.summaryRangeCacheWindow != nil {
		if window, hasWindow := s.summaryRangeCacheWindow[cacheKey]; hasWindow && window.Equal(healthWindow) {
			s.summaryCacheHits++
			s.lastSummaryDuration = time.Since(startedAt)
			return cloneDashboardSummary(cached)
		}
	}

	s.summaryCacheMisses++
	summary := s.buildSummaryWithoutDetailsForRangeLocked(now, healthWindow, cutoff, clientAPI)
	s.summaryRangeCache[cacheKey] = cloneDashboardSummary(summary)
	s.summaryRangeCacheWindow[cacheKey] = healthWindow
	s.pruneSummaryRangeCacheLocked(cacheKey)
	s.lastSummaryDuration = time.Since(startedAt)
	return summary
}

func summaryRangeCacheKey(rangeKey string, now time.Time) string {
	return summaryRangeClientAPICacheKey(rangeKey, "", now)
}

func summaryRangeClientAPICacheKey(rangeKey string, clientAPI string, now time.Time) string {
	return rangeKey + "|" + clientAPI + "|" + strconv.FormatInt(summaryRangeCacheBucket(now).Unix(), 10)
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

func cloneDashboardSummary(summary DashboardSummary) DashboardSummary {
	cloned := summary
	cloned.Usage = cloneStatisticsSnapshotWithoutDetails(summary.Usage)
	cloned.HealthGrid = append([]HealthGridSlot(nil), summary.HealthGrid...)
	if summary.HealthGridV2 != nil {
		grid := *summary.HealthGridV2
		grid.Slots = append([][3]int64(nil), summary.HealthGridV2.Slots...)
		cloned.HealthGridV2 = &grid
	}
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
	summary.Usage.CacheWriteTokens = s.cacheWriteTokens
	summary.Usage.ReasoningTokens = s.reasoningTokens
	if s.latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(s.latencySum) / float64(s.latencyN)
	}

	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(s.apis))

	healthStart := healthWindow.Add(-dashboardHealthSlotCount * dashboardHealthStep)

	for apiName, apiSt := range s.apis {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests:    apiSt.TotalRequests,
			SuccessCount:     apiSt.SuccessCount,
			FailureCount:     apiSt.FailureCount,
			TotalTokens:      apiSt.TotalTokens,
			InputTokens:      apiSt.InputTokens,
			OutputTokens:     apiSt.OutputTokens,
			CachedTokens:     apiSt.CachedTokens,
			CacheWriteTokens: apiSt.CacheWriteTokens,
			ReasoningTokens:  apiSt.ReasoningTokens,
			EstimatedCost:    apiSt.estimatedCost,
			Models:           make(map[string]ModelSnapshotWithoutDetails, len(apiSt.Models)),
		}
		if apiSt.latencyN > 0 {
			apiSnap.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
		}

		for modelName, modelSt := range apiSt.Models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests:    modelSt.TotalRequests,
				SuccessCount:     modelSt.SuccessCount,
				FailureCount:     modelSt.FailureCount,
				TotalTokens:      modelSt.TotalTokens,
				InputTokens:      modelSt.InputTokens,
				OutputTokens:     modelSt.OutputTokens,
				CachedTokens:     modelSt.CachedTokens,
				CacheWriteTokens: modelSt.CacheWriteTokens,
				ReasoningTokens:  modelSt.ReasoningTokens,
				EstimatedCost:    modelSt.estimatedCost,
				Providers:        finalizedModelProviderStats(modelSt.providerStats, modelSt.TotalRequests, modelSt.SuccessCount, modelSt.FailureCount, modelSt.TotalTokens, modelSt.InputTokens, modelSt.OutputTokens, modelSt.CachedTokens, modelSt.CacheWriteTokens, modelSt.ReasoningTokens),
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

	summary.ClientAPIStats = clientAPIStatsFromAccumulators(s.clientAPIStats)

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

// buildSummaryWithoutDetailsForRangeLocked scans all events within the cutoff window
// and builds a fresh DashboardSummary. Caller must hold s.mu.
func (s *RequestStatistics) buildSummaryWithoutDetailsForRangeLocked(now time.Time, healthWindow time.Time, cutoff time.Time, clientAPI string) DashboardSummary {
	summary := DashboardSummary{}

	// Usage accumulators
	var totalRequests, successCount, failureCount int64
	var totalTokens, inputTokens, outputTokens, cachedTokens, cacheWriteTokens, reasoningTokens int64
	var latencySum, latencyN int64

	requestsByDay := make(map[summaryDayKey]int64)
	requestsByHour := make(map[int]int64)
	tokensByDay := make(map[summaryDayKey]int64)
	tokensByHour := make(map[int]int64)
	costByDay := make(map[summaryDayKey]float64)
	costByHour := make(map[int]float64)

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
			for _, detail := range modelSt.Details {
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
				cost := s.detailCostLocked(modelName, detail, totals)
				requestsByDay[dayKey] = addNonNegativeInt64(requestsByDay[dayKey], 1)
				requestsByHour[hourKey] = addNonNegativeInt64(requestsByHour[hourKey], 1)
				tokensByDay[dayKey] = addNonNegativeInt64(tokensByDay[dayKey], totals.totalTokens)
				tokensByHour[hourKey] = addNonNegativeInt64(tokensByHour[hourKey], totals.totalTokens)
				costByDay[dayKey] = addNonNegativeCost(costByDay[dayKey], cost)
				costByHour[hourKey] = addNonNegativeCost(costByHour[hourKey], cost)

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

	// Time series
	summary.Usage.RequestsByDay = make(map[string]int64, len(requestsByDay))
	for k, v := range requestsByDay {
		summary.Usage.RequestsByDay[k.String()] = v
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	for hour, v := range requestsByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.RequestsByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.TokensByDay = make(map[string]int64, len(tokensByDay))
	for k, v := range tokensByDay {
		summary.Usage.TokensByDay[k.String()] = v
	}
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	for hour, v := range tokensByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.TokensByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.CostByDay = make(map[string]float64, len(costByDay))
	for k, v := range costByDay {
		summary.Usage.CostByDay[k.String()] = v
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

// apiRangeAgg and modelRangeAgg are lightweight accumulators used during
// range-scoped summary construction.
type apiRangeAgg struct {
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	estimatedCost    float64
	latencySum       int64
	latencyN         int64
	models           map[string]*modelRangeAgg
}

type modelRangeAgg struct {
	TotalRequests    int64
	SuccessCount     int64
	FailureCount     int64
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	estimatedCost    float64
	latencySum       int64
	latencyN         int64
	providerStats    map[string]*ModelProviderStat
}

type summaryDayKey struct {
	year  int
	month time.Month
	day   int
}

func newSummaryDayKey(value time.Time) summaryDayKey {
	year, month, day := value.Date()
	return summaryDayKey{year: year, month: month, day: day}
}

func (key summaryDayKey) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", key.year, key.month, key.day)
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
	m.TotalRequests = addNonNegativeInt64(m.TotalRequests, 1)
	if detail.Failed {
		m.FailureCount = addNonNegativeInt64(m.FailureCount, 1)
	} else {
		m.SuccessCount = addNonNegativeInt64(m.SuccessCount, 1)
	}
	m.TotalTokens = addNonNegativeInt64(m.TotalTokens, totals.totalTokens)
	m.InputTokens = addNonNegativeInt64(m.InputTokens, totals.inputTokens)
	m.OutputTokens = addNonNegativeInt64(m.OutputTokens, totals.outputTokens)
	m.CachedTokens = addNonNegativeInt64(m.CachedTokens, totals.cachedTokens)
	m.CacheWriteTokens = addNonNegativeInt64(m.CacheWriteTokens, totals.cacheWriteTokens)
	m.ReasoningTokens = addNonNegativeInt64(m.ReasoningTokens, totals.reasoningTokens)
	m.providerStats = incrementModelProviderStats(m.providerStats, detail.Provider, detail.Failed, totals)
	if detail.LatencyMs > 0 {
		m.latencySum = addNonNegativeInt64(m.latencySum, detail.LatencyMs)
		m.latencyN = addNonNegativeInt64(m.latencyN, 1)
	}
}

func rangeIncrementClientModel(client *clientAPIStatAccumulator, modelName string, detail RequestDetail, totals detailTotals) {
	cm, ok := client.models[modelName]
	if !ok {
		cm = &ClientAPIModelStat{Model: modelName}
		client.models[modelName] = cm
	}
	cm.TotalRequests = addNonNegativeInt64(cm.TotalRequests, 1)
	if detail.Failed {
		cm.FailureCount = addNonNegativeInt64(cm.FailureCount, 1)
	} else {
		cm.SuccessCount = addNonNegativeInt64(cm.SuccessCount, 1)
	}
	cm.TotalTokens = addNonNegativeInt64(cm.TotalTokens, totals.totalTokens)
	cm.InputTokens = addNonNegativeInt64(cm.InputTokens, totals.inputTokens)
	cm.OutputTokens = addNonNegativeInt64(cm.OutputTokens, totals.outputTokens)
	cm.CachedTokens = addNonNegativeInt64(cm.CachedTokens, totals.cachedTokens)
	cm.CacheWriteTokens = addNonNegativeInt64(cm.CacheWriteTokens, totals.cacheWriteTokens)
	cm.ReasoningTokens = addNonNegativeInt64(cm.ReasoningTokens, totals.reasoningTokens)
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
	hash := strings.TrimSpace(detail.APIKeyHash)
	if hash != "" {
		return "api_key_hash:" + hash
	}
	label := strings.TrimSpace(detail.APIKey)
	if label != "" {
		return "api_key:" + label
	}
	return "(unknown)"
}

type clientAPIGroupIdentity struct {
	hash  string
	label string
}

func clientAPIIdentity(detail RequestDetail) clientAPIGroupIdentity {
	if hash := strings.TrimSpace(detail.APIKeyHash); hash != "" {
		return clientAPIGroupIdentity{hash: hash}
	}
	return clientAPIGroupIdentity{label: strings.TrimSpace(detail.APIKey)}
}

func clientAPIStatsFromAccumulators(accumulators map[string]*clientAPIStatAccumulator) []ClientAPIStat {
	values := make([]*clientAPIStatAccumulator, 0, len(accumulators))
	for _, accumulator := range accumulators {
		values = append(values, accumulator)
	}
	return clientAPIStatsFromAccumulatorValues(values)
}

func clientAPIStatsFromIdentityAccumulators(accumulators map[clientAPIGroupIdentity]*clientAPIStatAccumulator) []ClientAPIStat {
	values := make([]*clientAPIStatAccumulator, 0, len(accumulators))
	for _, accumulator := range accumulators {
		values = append(values, accumulator)
	}
	return clientAPIStatsFromAccumulatorValues(values)
}

func clientAPIStatsFromAccumulatorValues(accumulators []*clientAPIStatAccumulator) []ClientAPIStat {
	stats := make([]ClientAPIStat, 0, len(accumulators))
	for _, agg := range accumulators {
		if agg == nil {
			continue
		}
		stat := agg.stat
		stat.Models = make([]ClientAPIModelStat, 0, len(agg.models))
		for _, model := range agg.models {
			if model == nil {
				continue
			}
			stat.Models = append(stat.Models, finalizeClientAPIModelStat(*model))
		}
		sortClientAPIModelStats(stat.Models)
		stats = append(stats, stat)
	}
	stats = coalesceMaskedClientAPIStats(stats)
	for i := range stats {
		stats[i].Selector = clientAPISelectorForStat(stats[i])
	}
	sortClientAPIStats(stats)
	return stats
}

func clientAPISelectorForStat(stat ClientAPIStat) string {
	label := strings.TrimSpace(stat.APIKey)
	hash := strings.TrimSpace(stat.APIKeyHash)
	encodedLabel := base64.RawURLEncoding.EncodeToString([]byte(label))
	if hash != "" {
		return "h." + hash + "." + encodedLabel
	}
	if label == "" || label == "未知 API" {
		return "u"
	}
	return "m." + encodedLabel
}

type clientAPISelector struct {
	kind  byte
	hash  string
	label string
}

func parseClientAPISelector(value string) (clientAPISelector, bool) {
	value = strings.TrimSpace(value)
	if value == "u" {
		return clientAPISelector{kind: 'u'}, true
	}
	parts := strings.Split(value, ".")
	if len(parts) == 2 && parts[0] == "m" {
		label, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || strings.TrimSpace(string(label)) == "" {
			return clientAPISelector{}, false
		}
		return clientAPISelector{kind: 'm', label: strings.TrimSpace(string(label))}, true
	}
	if len(parts) == 3 && parts[0] == "h" {
		hash := strings.TrimSpace(parts[1])
		label, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || hash == "" || strings.TrimSpace(string(label)) == "" {
			return clientAPISelector{}, false
		}
		return clientAPISelector{kind: 'h', hash: hash, label: strings.TrimSpace(string(label))}, true
	}
	return clientAPISelector{}, false
}

func clientAPISelectorMatchesDetail(value string, detail RequestDetail) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	selector, ok := parseClientAPISelector(value)
	if !ok {
		return false
	}
	label := strings.TrimSpace(detail.APIKey)
	hash := strings.TrimSpace(detail.APIKeyHash)
	switch selector.kind {
	case 'u':
		return label == "" && hash == ""
	case 'm':
		return label == selector.label
	case 'h':
		return hash == selector.hash || hash == "" && label == selector.label
	default:
		return false
	}
}

func sortClientAPIStats(stats []ClientAPIStat) {
	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
}

func sortClientAPIModelStats(stats []ClientAPIModelStat) {
	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
}

func coalesceMaskedClientAPIStats(stats []ClientAPIStat) []ClientAPIStat {
	if len(stats) < 2 {
		return stats
	}
	type labelGroups struct {
		indices     []int
		hashes      map[string]bool
		hasHashless bool
	}
	byLabel := make(map[string]*labelGroups)
	for i := range stats {
		label := strings.TrimSpace(stats[i].APIKey)
		if label == "" || !strings.Contains(label, redactedMarker) {
			continue
		}
		group, ok := byLabel[label]
		if !ok {
			group = &labelGroups{hashes: make(map[string]bool)}
			byLabel[label] = group
		}
		group.indices = append(group.indices, i)
		if hash := strings.TrimSpace(stats[i].APIKeyHash); hash != "" {
			group.hashes[hash] = true
		} else {
			group.hasHashless = true
		}
	}
	removed := make(map[int]bool)
	for _, group := range byLabel {
		if len(group.indices) < 2 {
			continue
		}
		if len(group.hashes) > 1 && !group.hasHashless {
			continue
		}
		target := clientAPIStatsMergeTarget(stats, group.indices)
		for _, source := range group.indices {
			if source == target {
				continue
			}
			mergeClientAPIStat(&stats[target], stats[source])
			removed[source] = true
		}
		switch len(group.hashes) {
		case 0:
			stats[target].APIKeyHash = ""
		case 1:
			for hash := range group.hashes {
				stats[target].APIKeyHash = hash
			}
		default:
			stats[target].APIKeyHash = ""
		}
	}
	if len(removed) == 0 {
		return stats
	}
	out := stats[:0]
	for i, stat := range stats {
		if removed[i] {
			continue
		}
		out = append(out, stat)
	}
	return out
}

func clientAPIStatsMergeTarget(stats []ClientAPIStat, indices []int) int {
	target := indices[0]
	for _, index := range indices[1:] {
		if stats[index].TotalRequests > stats[target].TotalRequests {
			target = index
			continue
		}
		if stats[index].TotalRequests == stats[target].TotalRequests &&
			strings.TrimSpace(stats[target].APIKeyHash) == "" &&
			strings.TrimSpace(stats[index].APIKeyHash) != "" {
			target = index
		}
	}
	return target
}

func mergeClientAPIStat(dst *ClientAPIStat, src ClientAPIStat) {
	if dst == nil {
		return
	}
	if strings.TrimSpace(dst.APIKey) == "" {
		dst.APIKey = src.APIKey
	}
	if strings.TrimSpace(dst.APIKeyHash) == "" {
		dst.APIKeyHash = src.APIKeyHash
	}
	dst.TotalRequests = addNonNegativeInt64(dst.TotalRequests, src.TotalRequests)
	dst.SuccessCount = addNonNegativeInt64(dst.SuccessCount, src.SuccessCount)
	dst.FailureCount = addNonNegativeInt64(dst.FailureCount, src.FailureCount)
	dst.TotalTokens = addNonNegativeInt64(dst.TotalTokens, src.TotalTokens)
	dst.InputTokens = addNonNegativeInt64(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = addNonNegativeInt64(dst.OutputTokens, src.OutputTokens)
	dst.CachedTokens = addNonNegativeInt64(dst.CachedTokens, src.CachedTokens)
	dst.CacheWriteTokens = addNonNegativeInt64(dst.CacheWriteTokens, src.CacheWriteTokens)
	dst.ReasoningTokens = addNonNegativeInt64(dst.ReasoningTokens, src.ReasoningTokens)

	models := make(map[string]*ClientAPIModelStat, len(dst.Models)+len(src.Models))
	for _, model := range dst.Models {
		modelCopy := model
		models[modelCopy.Model] = &modelCopy
	}
	for _, model := range src.Models {
		key := model.Model
		if existing, ok := models[key]; ok && existing != nil {
			mergeClientAPIModelStat(existing, model)
			continue
		}
		modelCopy := model
		models[key] = &modelCopy
	}
	dst.Models = dst.Models[:0]
	for _, model := range models {
		if model == nil {
			continue
		}
		dst.Models = append(dst.Models, *model)
	}
	sortClientAPIModelStats(dst.Models)
}

func mergeClientAPIModelStat(dst *ClientAPIModelStat, src ClientAPIModelStat) {
	if dst == nil {
		return
	}
	dst.TotalRequests = addNonNegativeInt64(dst.TotalRequests, src.TotalRequests)
	dst.SuccessCount = addNonNegativeInt64(dst.SuccessCount, src.SuccessCount)
	dst.FailureCount = addNonNegativeInt64(dst.FailureCount, src.FailureCount)
	dst.TotalTokens = addNonNegativeInt64(dst.TotalTokens, src.TotalTokens)
	dst.InputTokens = addNonNegativeInt64(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = addNonNegativeInt64(dst.OutputTokens, src.OutputTokens)
	dst.CachedTokens = addNonNegativeInt64(dst.CachedTokens, src.CachedTokens)
	dst.CacheWriteTokens = addNonNegativeInt64(dst.CacheWriteTokens, src.CacheWriteTokens)
	dst.ReasoningTokens = addNonNegativeInt64(dst.ReasoningTokens, src.ReasoningTokens)
	dst.Providers = mergeFinalizedProviderStats(dst.Providers, src.Providers)
}

func mergeFinalizedProviderStats(dst []ModelProviderStat, src []ModelProviderStat) []ModelProviderStat {
	if len(src) == 0 {
		return dst
	}
	merged := make(map[string]*ModelProviderStat, len(dst)+len(src))
	for _, provider := range dst {
		providerCopy := provider
		merged[modelProviderStatsKey(providerCopy.Provider)] = &providerCopy
	}
	for _, provider := range src {
		key := modelProviderStatsKey(provider.Provider)
		existing, ok := merged[key]
		if !ok || existing == nil {
			providerCopy := provider
			merged[key] = &providerCopy
			continue
		}
		existing.TotalRequests = addNonNegativeInt64(existing.TotalRequests, provider.TotalRequests)
		existing.SuccessCount = addNonNegativeInt64(existing.SuccessCount, provider.SuccessCount)
		existing.FailureCount = addNonNegativeInt64(existing.FailureCount, provider.FailureCount)
		existing.TotalTokens = addNonNegativeInt64(existing.TotalTokens, provider.TotalTokens)
		existing.InputTokens = addNonNegativeInt64(existing.InputTokens, provider.InputTokens)
		existing.OutputTokens = addNonNegativeInt64(existing.OutputTokens, provider.OutputTokens)
		existing.CachedTokens = addNonNegativeInt64(existing.CachedTokens, provider.CachedTokens)
		existing.CacheWriteTokens = addNonNegativeInt64(existing.CacheWriteTokens, provider.CacheWriteTokens)
		existing.ReasoningTokens = addNonNegativeInt64(existing.ReasoningTokens, provider.ReasoningTokens)
	}
	out := make([]ModelProviderStat, 0, len(merged))
	for _, provider := range merged {
		if provider == nil {
			continue
		}
		out = append(out, *provider)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalRequests != out[j].TotalRequests {
			return out[i].TotalRequests > out[j].TotalRequests
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func normalizeImportedClientAPIIdentity(detail RequestDetail) RequestDetail {
	label := canonicalClientAPIKey(detail.APIKey)
	hash := strings.TrimSpace(detail.APIKeyHash)
	alreadyMasked := label != "" && strings.Contains(label, redactedMarker)
	if label != "" && !alreadyMasked {
		hash = hashAPIKey(label)
		label = maskAPIKey(label)
	}
	// Imported exports may carry hashes generated with a different instance's
	// salt. For already-masked labels, keep the documented cross-instance merge
	// behavior by grouping on the masked display value instead.
	if alreadyMasked {
		hash = ""
	}
	detail.APIKey = label
	detail.APIKeyHash = hash
	return detail
}

func normalizeStoredClientAPIIdentity(detail RequestDetail) RequestDetail {
	label := canonicalClientAPIKey(detail.APIKey)
	if label == "" {
		detail.APIKey = ""
		detail.APIKeyHash = strings.TrimSpace(detail.APIKeyHash)
		return detail
	}
	if strings.Contains(label, redactedMarker) {
		hash := strings.TrimSpace(detail.APIKeyHash)
		if !isStoredAPIKeyHashShape(hash) {
			hash = ""
		}
		detail.APIKey = label
		detail.APIKeyHash = hash
		return detail
	}
	detail.APIKey = maskAPIKey(label)
	detail.APIKeyHash = hashAPIKey(label)
	return detail
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
	detail      *RequestDetail
	upstreamAPI string
	sortKey     string
	modelName   string
	sequence    int64
}

func (d dashboardEventDetail) requestDetail() RequestDetail {
	if d.detail == nil {
		return RequestDetail{}
	}
	detail := *d.detail
	detail.UpstreamAPI = d.upstreamAPI
	return detail
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
		timeBucket = summaryRangeCacheBucket(now).Unix()
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
		clientAPI:  params.ClientAPI,
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

func (s *RequestStatistics) refreshDashboardEventIndexesLocked() {
	if s == nil {
		return
	}
	if s.eventIndexVersion != s.summaryVersion {
		s.eventIndexVersion = s.summaryVersion
		s.eventIndex = nil
		s.eventAPIIndex = nil
		s.eventModelIndex = nil
		s.eventSourceIndex = nil
		s.eventAuthIndex = nil
	}
}

func (s *RequestStatistics) dashboardEventIndexLocked(api string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	s.refreshDashboardEventIndexesLocked()
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
		events := make([]dashboardEventDetail, 0, nonNegativeIntFromInt64(s.countDetailsLocked()))
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

func (s *RequestStatistics) dashboardEventIndexEntriesLocked() int {
	if s == nil {
		return 0
	}
	entries := len(s.eventIndex)
	for _, events := range s.eventAPIIndex {
		if len(events) > entries {
			entries = len(events)
		}
	}
	for _, events := range s.eventModelIndex {
		if len(events) > entries {
			entries = len(events)
		}
	}
	for _, events := range s.eventSourceIndex {
		if len(events) > entries {
			entries = len(events)
		}
	}
	for _, events := range s.eventAuthIndex {
		if len(events) > entries {
			entries = len(events)
		}
	}
	return entries
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
	s.refreshDashboardEventIndexesLocked()
	if s.eventModelIndex == nil {
		s.eventModelIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventModelIndex[model]; ok {
		return events
	}
	events := buildDashboardEventIndexForFilter(s.apis, dashboardEventIndexFilterModel, model)
	s.eventModelIndex[model] = events
	return events
}

func (s *RequestStatistics) dashboardEventSourceIndexLocked(source string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	s.refreshDashboardEventIndexesLocked()
	if s.eventSourceIndex == nil {
		s.eventSourceIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventSourceIndex[source]; ok {
		return events
	}
	events := buildDashboardEventIndexForFilter(s.apis, dashboardEventIndexFilterSource, source)
	s.eventSourceIndex[source] = events
	return events
}

func (s *RequestStatistics) dashboardEventAuthIndexLocked(authIndex string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	s.refreshDashboardEventIndexesLocked()
	if s.eventAuthIndex == nil {
		s.eventAuthIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventAuthIndex[authIndex]; ok {
		return events
	}
	events := buildDashboardEventIndexForFilter(s.apis, dashboardEventIndexFilterAuth, authIndex)
	s.eventAuthIndex[authIndex] = events
	return events
}

func dashboardEventModelKey(event dashboardEventDetail) string {
	return dashboardEventDetailModelKey(event.detail, event.modelName)
}

func dashboardEventDetailModelKey(detail *RequestDetail, modelName string) string {
	if detail != nil && detail.Model != "" {
		return detail.Model
	}
	return modelName
}

func dashboardEventSourceKey(event dashboardEventDetail) string {
	return dashboardEventDetailSourceKey(event.detail)
}

func dashboardEventDetailSourceKey(detail *RequestDetail) string {
	if detail == nil || detail.Source == "" {
		return "未知来源"
	}
	return detail.Source
}

func dashboardEventAuthKey(event dashboardEventDetail) string {
	return dashboardEventDetailAuthKey(event.detail)
}

func dashboardEventDetailAuthKey(detail *RequestDetail) string {
	if detail == nil {
		return ""
	}
	return detail.AuthIndex
}

type dashboardEventIndexFilter int

const (
	dashboardEventIndexFilterModel dashboardEventIndexFilter = iota + 1
	dashboardEventIndexFilterSource
	dashboardEventIndexFilterAuth
)

func buildDashboardEventIndexForFilter(apis map[string]*apiStats, filter dashboardEventIndexFilter, value string) []dashboardEventDetail {
	events := make([]dashboardEventDetail, 0, countDashboardEventsForFilter(apis, filter, value))
	for apiName, apiSt := range apis {
		events = appendDashboardEventIndexForFilter(events, apiName, apiSt, filter, value)
	}
	sort.Slice(events, func(i, j int) bool {
		return dashboardEventBefore(events[i], events[j])
	})
	return events
}

func countDashboardEventsForFilter(apis map[string]*apiStats, filter dashboardEventIndexFilter, value string) int {
	var count int
	for _, apiSt := range apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for i := range modelSt.Details {
				if dashboardEventIndexFilterMatches(&modelSt.Details[i], modelName, filter, value) {
					count++
				}
			}
		}
	}
	return count
}

func appendDashboardEventIndexForFilter(events []dashboardEventDetail, apiName string, apiSt *apiStats, filter dashboardEventIndexFilter, value string) []dashboardEventDetail {
	if apiSt == nil {
		return events
	}
	sequence := int64(len(events))
	for modelName, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		for i := range modelSt.Details {
			if dashboardEventIndexFilterMatches(&modelSt.Details[i], modelName, filter, value) {
				events = append(events, dashboardEventDetail{detail: &modelSt.Details[i], upstreamAPI: apiName, sortKey: apiName, modelName: modelName, sequence: sequence})
			}
			sequence++
		}
	}
	return events
}

func dashboardEventIndexFilterMatches(detail *RequestDetail, modelName string, filter dashboardEventIndexFilter, value string) bool {
	switch filter {
	case dashboardEventIndexFilterModel:
		return dashboardEventDetailModelKey(detail, modelName) == value
	case dashboardEventIndexFilterSource:
		return dashboardEventDetailSourceKey(detail) == value
	case dashboardEventIndexFilterAuth:
		return dashboardEventDetailAuthKey(detail) == value
	default:
		return false
	}
}

func buildDashboardEventIndexForAPI(apiName string, apiSt *apiStats) []dashboardEventDetail {
	events := make([]dashboardEventDetail, 0, countDetailsForAPI(apiSt))
	events = appendDashboardEventIndexForAPI(events, apiName, apiSt)
	sort.Slice(events, func(i, j int) bool {
		return dashboardEventBefore(events[i], events[j])
	})
	return events
}

func countDetailsForAPI(apiSt *apiStats) int {
	if apiSt == nil {
		return 0
	}
	var count int
	for _, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		count += len(modelSt.Details)
	}
	return count
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
			events = append(events, dashboardEventDetail{detail: &modelSt.Details[i], upstreamAPI: apiName, sortKey: apiName, modelName: modelName, sequence: sequence})
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
		params.AuthIndex != "" ||
		params.ClientAPI != ""
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
	if !clientAPISelectorMatchesDetail(params.ClientAPI, d) {
		return false
	}
	return true
}

// QueryEvents returns paginated, filtered event details.
func (s *RequestStatistics) QueryEvents(params EventsQuery) EventsResult {
	return s.QueryEventsAt(params, time.Now())
}

func (s *RequestStatistics) QueryEventsAt(params EventsQuery, now time.Time) EventsResult {
	return s.queryEventsAt(params, true, 0, now)
}

// QueryAllEvents returns every matching event for backend-generated exports.
func (s *RequestStatistics) QueryAllEvents(params EventsQuery) EventsResult {
	return s.queryEventsAt(params, false, 0, time.Now())
}

// QueryExportEvents returns matching events up to maxRecords while still
// counting the full match total for capped backend-generated exports.
func (s *RequestStatistics) QueryExportEvents(params EventsQuery, maxRecords int) EventsResult {
	return s.QueryExportEventsAt(params, maxRecords, time.Now())
}

func (s *RequestStatistics) QueryExportEventsAt(params EventsQuery, maxRecords int, now time.Time) EventsResult {
	return s.queryEventsAt(params, false, maxRecords, now)
}

// QueryExportEventsPage returns one page of exportable events while still
// counting the full match total. snapshotAt freezes the upper time bound so
// background exports do not shift when new requests arrive while paging.
// pricing 冻结该次导出使用的价格表;传 nil 表示按当前价格计价。
func (s *RequestStatistics) QueryExportEventsPage(params EventsQuery, offset int, pageLimit int, maxRecords int, snapshotAt time.Time, pricing *pricingSnapshot) EventsResult {
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
	events := make([]RequestDetail, 0, exportPageEventCapacity(pageLimit, offset, maxRecords))
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

		dashboardVersion: s.summaryVersion,
	}
	// 后台分页导出不走 queryEventsAt 的 finish(),必须在这里自己附加成本,否则
	// JSON/JSONL 完全没有 cost_usd、CSV 的该列整列为空。pricing 非 nil 时按导出
	// 开始时冻结的价格计价,保证整份文件前后页口径一致。
	if pricing != nil {
		for i := range result.Events {
			cost := pricing.detailCost(result.Events[i].Model, result.Events[i], detailTotalsFromRequest(result.Events[i]))
			result.Events[i].CostUSD = &cost
		}
	} else {
		s.attachEventCostsLocked(result.Events)
	}
	s.lastEventsQueryDuration = time.Since(startedAt)
	s.lastEventsQueryTotal = total
	return result
}

func exportPageEventCapacity(pageLimit int, offset int, maxRecords int) int {
	if pageLimit <= 0 {
		return 0
	}
	if maxRecords <= 0 {
		return pageLimit
	}
	remaining := maxRecords - offset
	if remaining <= 0 {
		return 0
	}
	if remaining < pageLimit {
		return remaining
	}
	return pageLimit
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

func (s *RequestStatistics) queryEventsAt(params EventsQuery, paginate bool, exportLimit int, now time.Time) EventsResult {
	if s == nil {
		return EventsResult{}
	}
	startedAt := time.Now()
	if now.IsZero() {
		now = startedAt
	}

	params = normalizeEventsQuery(params, paginate)

	s.mu.Lock()
	defer s.mu.Unlock()
	finish := func(result EventsResult) EventsResult {
		s.attachEventCostsLocked(result.Events)
		result.dashboardVersion = s.summaryVersion
		s.lastEventsQueryDuration = time.Since(startedAt)
		s.lastEventsQueryTotal = result.Total
		return result
	}

	cutoff := dashboardRangeCutoff(params.Range, now)
	generatedAt := s.dashboardQueryGeneratedAtLocked(params.Range, now).UTC().Format(time.RFC3339)
	var cacheKey dashboardEventCacheKey
	if paginate {
		cacheKey = dashboardEventCacheKeyFor(params, now)
		if cached, ok := s.eventQueryCache[cacheKey]; ok {
			s.eventCacheHits++
			return finish(cloneEventsResult(cached, time.Time{}))
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
				GeneratedAt: generatedAt,
			})
		}
		if params.Offset >= total {
			result := EventsResult{
				Events:      []RequestDetail{},
				Total:       total,
				Limit:       params.Limit,
				Offset:      params.Offset,
				GeneratedAt: generatedAt,
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
			GeneratedAt: generatedAt,
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return finish(result)
	}

	eventsCap := 0
	if paginate {
		eventsCap = params.Limit
	}
	events := make([]RequestDetail, 0, eventsCap)
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
			GeneratedAt: generatedAt,
		})
	}

	if params.Offset >= total {
		result := EventsResult{
			Events:      []RequestDetail{},
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: generatedAt,
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return finish(result)
	}

	result := EventsResult{
		Events:      events,
		Total:       total,
		Limit:       params.Limit,
		Offset:      params.Offset,
		GeneratedAt: generatedAt,
	}
	s.cacheDashboardEventsLocked(cacheKey, result)
	return finish(result)
}

func (s *RequestStatistics) attachEventCostsLocked(events []RequestDetail) {
	for i := range events {
		detail := &events[i]
		cost := s.detailCostLocked(detail.Model, *detail, detailTotalsFromRequest(*detail))
		detail.CostUSD = &cost
	}
}

func exportResultLimit(total int, exportLimit int) int {
	if exportLimit > 0 && total > exportLimit {
		return exportLimit
	}
	return total
}

func (s *RequestStatistics) dashboardQueryGeneratedAtLocked(rangeKey string, now time.Time) time.Time {
	if rangeKey != "" && rangeKey != "all" {
		return summaryRangeCacheBucket(now)
	}
	if s != nil && !s.lastRecordedAt.IsZero() {
		return s.lastRecordedAt.UTC().Truncate(time.Second)
	}
	return time.Unix(0, 0).UTC()
}

// QueryAPIDetail returns range-scoped aggregates and recent events for one API
// without making the browser page through every matching event.
func (s *RequestStatistics) QueryAPIDetail(api string, rangeKey string, recentLimit int, errorLimit int) APIDetailResponse {
	return s.QueryAPIDetailAt(api, rangeKey, recentLimit, errorLimit, time.Now())
}

func (s *RequestStatistics) QueryAPIDetailAt(api string, rangeKey string, recentLimit int, errorLimit int, now time.Time) APIDetailResponse {
	return s.QueryAPIDetailForClientAPIAt(api, rangeKey, "", recentLimit, errorLimit, now)
}

func (s *RequestStatistics) QueryAPIDetailForClientAPIAt(api string, rangeKey string, clientAPI string, recentLimit int, errorLimit int, now time.Time) APIDetailResponse {
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

	index := s.dashboardEventIndexLocked(api)
	if len(index) == 0 {
		return finish(result)
	}

	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*SourceStat)
	errorAgg := make(map[apiDetailErrorKey]*APIDetailErrorStat)
	recentEvents := make(dashboardEventHeap, 0, recentLimit)
	heap.Init(&recentEvents)
	var latencySum int64
	var latencyN int64
	sequence := int64(0)

	for _, dm := range index {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(d, cutoff) {
			break
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
			ms.EstimatedCost = addNonNegativeCost(ms.EstimatedCost, s.detailCostLocked(modelLabel, d, detailTotals{totalTokens: totalTokens, inputTokens: inputTokens, outputTokens: outputTokens, cachedTokens: cachedTokens, cacheWriteTokens: cacheWriteTokens, reasoningTokens: reasoningTokens}))
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

		appendBoundedDashboardEventHeap(&recentEvents, dashboardEventDetail{detail: dm.detail, upstreamAPI: dm.upstreamAPI, sortKey: d.Model, sequence: sequence}, recentLimit)
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
	result.GeneratedAt = generatedAt
	return finish(result)
}

func apiDetailSummaryFromAPIStats(apiSt *apiStats) APIDetailSummary {
	if apiSt == nil {
		return APIDetailSummary{}
	}
	summary := APIDetailSummary{
		TotalRequests:    apiSt.TotalRequests,
		SuccessCount:     apiSt.SuccessCount,
		FailureCount:     apiSt.FailureCount,
		TotalTokens:      apiSt.TotalTokens,
		InputTokens:      apiSt.InputTokens,
		OutputTokens:     apiSt.OutputTokens,
		CachedTokens:     apiSt.CachedTokens,
		CacheWriteTokens: apiSt.CacheWriteTokens,
		ReasoningTokens:  apiSt.ReasoningTokens,
		EstimatedCost:    apiSt.estimatedCost,
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
			Model:            modelName,
			TotalRequests:    modelSt.TotalRequests,
			SuccessCount:     modelSt.SuccessCount,
			FailureCount:     modelSt.FailureCount,
			TotalTokens:      modelSt.TotalTokens,
			InputTokens:      modelSt.InputTokens,
			OutputTokens:     modelSt.OutputTokens,
			CachedTokens:     modelSt.CachedTokens,
			CacheWriteTokens: modelSt.CacheWriteTokens,
			ReasoningTokens:  modelSt.ReasoningTokens,
			EstimatedCost:    modelSt.estimatedCost,
			Providers:        finalizedModelProviderStats(modelSt.providerStats, modelSt.TotalRequests, modelSt.SuccessCount, modelSt.FailureCount, modelSt.TotalTokens, modelSt.InputTokens, modelSt.OutputTokens, modelSt.CachedTokens, modelSt.CacheWriteTokens, modelSt.ReasoningTokens),
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

func (s *RequestStatistics) CurrencyVersion() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currencyVersion
}

func (s *RequestStatistics) Close() {
	if s == nil {
		return
	}
	s.stopStorageWorker()
	s.stopModelsDevPriceWorker()
	s.stopExchangeRateWorker()
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
		PricingTimezone:               s.pricingTimezone,
		ExchangeRateEnabled:           s.exchangeRateEnabled,
		ExchangeRateUSD:               s.exchangeRateURL,
		ExchangeRateRefreshSeconds:    int(s.exchangeRateRefresh.Seconds()),
		ExchangeRateTimeoutSeconds:    int(s.exchangeRateTimeout.Seconds()),
		ExchangeRateFallbackUSDCNY:    s.exchangeRateFallback,
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
		EventIndexEntries:          s.dashboardEventIndexEntriesLocked(),
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
		ExchangeRate:               s.currencyStateLocked(time.Now()),
		PricingTimezone:            s.pricingTimezone,
		PricingTimezoneError:       s.pricingTimezoneError,
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
