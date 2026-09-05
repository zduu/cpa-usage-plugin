package main

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	abiVersion                        uint32 = 1
	defaultMaxDetailsPerModel                = 5000
	defaultRetentionDays                     = 30
	defaultDedupWindowMinutes                = 24 * 60
	defaultStorageFlushSeconds               = 30
	defaultStorageSnapshotSeconds            = 300
	defaultStorageSnapshotRecords            = 1000
	defaultStorageSyncSeconds                = 0
	defaultStorageSyncRecords                = 0
	defaultStorageWriteQueueSize             = 4096
	defaultStorageWriteBatchSize             = 128
	defaultExportMaxRecords                  = 100000
	defaultPriceStoragePath                  = "data/usage-statistics-prices.json"
	legacyPriceStoragePath                   = "usage-statistics-prices.json"
	defaultModelsDevPricesURL                = "https://models.dev/api.json"
	defaultModelsDevRefreshSeconds           = 12 * 60 * 60
	defaultPricingTimezone                   = "Asia/Shanghai"
	defaultExchangeRateURL                   = "https://open.er-api.com/v6/latest/USD"
	defaultExchangeRateRefreshSeconds        = 6 * 60 * 60
	defaultExchangeRateTimeoutSeconds        = 5
	defaultExchangeRateFallbackUSDCNY        = 7.20
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type runtimeConfig struct {
	MaxDetailsPerModel            int
	RetentionDays                 int
	DedupWindowMinutes            int
	LogResponseHeaders            string
	APIKeyHashSalt                string
	StorageEnabled                bool
	StoragePath                   string
	StorageFlushSeconds           int
	StorageSnapshotSeconds        int
	StorageSnapshotRecordInterval int
	StorageSyncSeconds            int
	StorageSyncRecordInterval     int
	ExportMaxRecords              int
	PriceStoragePath              string
	ModelsDevPricesEnabled        bool
	ModelsDevPricesURL            string
	ModelsDevRefreshSeconds       int
	ClaudeCacheRepairEnabled      bool
	UpdateEnabled                 bool
	UpdateVersion                 string
	PricingTimezone               string
	ExchangeRateEnabled           bool
	ExchangeRateUSD               string
	ExchangeRateRefreshSeconds    int
	ExchangeRateTimeoutSeconds    int
	ExchangeRateFallbackUSDCNY    float64
}

type runtimeConfigPatch struct {
	MaxDetailsPerModel            *int
	RetentionDays                 *int
	DedupWindowMinutes            *int
	LogResponseHeaders            *string
	APIKeyHashSalt                *string
	StorageEnabled                *bool
	StoragePath                   *string
	StorageFlushSeconds           *int
	StorageSnapshotSeconds        *int
	StorageSnapshotRecordInterval *int
	StorageSyncSeconds            *int
	StorageSyncRecordInterval     *int
	ExportMaxRecords              *int
	PriceStoragePath              *string
	ModelsDevPricesEnabled        *bool
	ModelsDevPricesURL            *string
	ModelsDevRefreshSeconds       *int
	ClaudeCacheRepairEnabled      *bool
	UpdateEnabled                 *bool
	UpdateVersion                 *string
	PricingTimezone               *string
	ExchangeRateEnabled           *bool
	ExchangeRateUSD               *string
	ExchangeRateRefreshSeconds    *int
	ExchangeRateTimeoutSeconds    *int
	ExchangeRateFallbackUSDCNY    *float64
}

func defaultRuntimeConfig() runtimeConfig {
	return runtimeConfig{
		MaxDetailsPerModel:            defaultMaxDetailsPerModel,
		RetentionDays:                 defaultRetentionDays,
		DedupWindowMinutes:            defaultDedupWindowMinutes,
		LogResponseHeaders:            "",
		APIKeyHashSalt:                "",
		StorageEnabled:                false,
		StoragePath:                   "usage-statistics.jsonl",
		StorageFlushSeconds:           defaultStorageFlushSeconds,
		StorageSnapshotSeconds:        defaultStorageSnapshotSeconds,
		StorageSnapshotRecordInterval: defaultStorageSnapshotRecords,
		StorageSyncSeconds:            defaultStorageSyncSeconds,
		StorageSyncRecordInterval:     defaultStorageSyncRecords,
		ExportMaxRecords:              defaultExportMaxRecords,
		PriceStoragePath:              defaultPriceStoragePath,
		ModelsDevPricesEnabled:        false,
		ModelsDevPricesURL:            defaultModelsDevPricesURL,
		ModelsDevRefreshSeconds:       defaultModelsDevRefreshSeconds,
		// 历史缓存双计修复默认关闭:旧数据中「真实命中恰好等于创建」的合法
		// 记录与污染签名不可判定,须由用户确认后显式启用(见 claude_cache_repair.go)。
		ClaudeCacheRepairEnabled:   false,
		UpdateEnabled:              false,
		UpdateVersion:              "latest",
		PricingTimezone:            defaultPricingTimezone,
		ExchangeRateEnabled:        true,
		ExchangeRateUSD:            defaultExchangeRateURL,
		ExchangeRateRefreshSeconds: defaultExchangeRateRefreshSeconds,
		ExchangeRateTimeoutSeconds: defaultExchangeRateTimeoutSeconds,
		ExchangeRateFallbackUSDCNY: defaultExchangeRateFallbackUSDCNY,
	}
}

// ============================================================================
// CPA Protocol Types
// ============================================================================

type UsageRecord struct {
	Provider        string              `json:"provider"`
	ExecutorType    string              `json:"executor_type"`
	Model           string              `json:"model"`
	Alias           string              `json:"alias"`
	APIKey          string              `json:"api_key"`
	AuthID          string              `json:"auth_id"`
	AuthIndex       string              `json:"auth_index"`
	AuthType        string              `json:"auth_type"`
	Endpoint        string              `json:"endpoint"`
	BaseURL         string              `json:"base_url"`
	Source          string              `json:"source"`
	ReasoningEffort string              `json:"reasoning_effort"`
	ServiceTier     string              `json:"service_tier"`
	Stream          bool                `json:"stream"`
	RequestedAt     time.Time           `json:"requested_at"`
	Latency         time.Duration       `json:"latency"`
	TTFT            time.Duration       `json:"ttft"`
	Failed          bool                `json:"failed"`
	Failure         UsageFailure        `json:"failure"`
	Detail          UsageDetail         `json:"detail"`
	ResponseHeaders map[string][]string `json:"response_headers"`
	// protocolCorrelation is deliberately kept out of the public UsageRecord
	// JSON.  The response interceptor fills it from the raw usage object before
	// normalisation; native records derive the conservative equivalent in the
	// correlation adapter.  Keeping this sidecar here prevents the accounting
	// fields from having to carry presence/semantic information they never had.
	protocolCorrelation *ProtocolCorrelationMeta `json:"-"`
}

func (r *UsageRecord) UnmarshalJSON(data []byte) error {
	var current struct {
		Provider        string              `json:"provider"`
		ExecutorType    string              `json:"executor_type"`
		Model           string              `json:"model"`
		Alias           string              `json:"alias"`
		APIKey          string              `json:"api_key"`
		AuthID          string              `json:"auth_id"`
		AuthIndex       string              `json:"auth_index"`
		AuthType        string              `json:"auth_type"`
		Endpoint        string              `json:"endpoint"`
		BaseURL         string              `json:"base_url"`
		Source          string              `json:"source"`
		ReasoningEffort string              `json:"reasoning_effort"`
		ServiceTier     string              `json:"service_tier"`
		Stream          bool                `json:"stream"`
		RequestedAt     json.RawMessage     `json:"requested_at"`
		RequestedAtMs   json.RawMessage     `json:"requested_at_ms"`
		Latency         json.RawMessage     `json:"latency"`
		LatencyMs       json.RawMessage     `json:"latency_ms"`
		TTFT            json.RawMessage     `json:"ttft"`
		TTFTMs          json.RawMessage     `json:"ttft_ms"`
		Failed          bool                `json:"failed"`
		Failure         UsageFailure        `json:"failure"`
		Detail          UsageDetail         `json:"detail"`
		ResponseHeaders map[string][]string `json:"response_headers"`
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	var legacy struct {
		Provider        string              `json:"Provider"`
		ExecutorType    string              `json:"ExecutorType"`
		Model           string              `json:"Model"`
		Alias           string              `json:"Alias"`
		APIKey          string              `json:"APIKey"`
		AuthID          string              `json:"AuthID"`
		AuthIndex       string              `json:"AuthIndex"`
		AuthType        string              `json:"AuthType"`
		Endpoint        string              `json:"Endpoint"`
		BaseURL         string              `json:"BaseURL"`
		BaseUrl         string              `json:"BaseUrl"`
		Source          string              `json:"Source"`
		ReasoningEffort string              `json:"ReasoningEffort"`
		ServiceTier     string              `json:"ServiceTier"`
		Stream          bool                `json:"Stream"`
		RequestedAt     json.RawMessage     `json:"RequestedAt"`
		RequestedAtMs   json.RawMessage     `json:"RequestedAtMs"`
		Latency         json.RawMessage     `json:"Latency"`
		LatencyMs       json.RawMessage     `json:"LatencyMs"`
		TTFT            json.RawMessage     `json:"TTFT"`
		TTFTMs          json.RawMessage     `json:"TTFTMs"`
		Failed          bool                `json:"Failed"`
		Failure         UsageFailure        `json:"Failure"`
		Detail          UsageDetail         `json:"Detail"`
		ResponseHeaders map[string][]string `json:"ResponseHeaders"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	var aliases struct {
		BaseURLCamel string `json:"baseURL"`
		BaseUrlCamel string `json:"baseUrl"`
		Streaming    bool   `json:"streaming"`
		StreamingOld bool   `json:"Streaming"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}

	record := UsageRecord{
		Provider:        firstNonEmpty(current.Provider, legacy.Provider),
		ExecutorType:    firstNonEmpty(current.ExecutorType, legacy.ExecutorType),
		Model:           firstNonEmpty(current.Model, legacy.Model),
		Alias:           firstNonEmpty(current.Alias, legacy.Alias),
		APIKey:          firstNonEmpty(current.APIKey, legacy.APIKey),
		AuthID:          firstNonEmpty(current.AuthID, legacy.AuthID),
		AuthIndex:       firstNonEmpty(current.AuthIndex, legacy.AuthIndex),
		AuthType:        firstNonEmpty(current.AuthType, legacy.AuthType),
		Endpoint:        firstNonEmpty(current.Endpoint, legacy.Endpoint),
		BaseURL:         firstNonEmpty(current.BaseURL, aliases.BaseURLCamel, aliases.BaseUrlCamel, legacy.BaseURL, legacy.BaseUrl),
		Source:          firstNonEmpty(current.Source, legacy.Source),
		ReasoningEffort: firstNonEmpty(current.ReasoningEffort, legacy.ReasoningEffort),
		ServiceTier:     firstNonEmpty(current.ServiceTier, legacy.ServiceTier),
		Stream:          current.Stream || legacy.Stream || aliases.Streaming || aliases.StreamingOld,
		RequestedAt:     firstNonZeroTime(parseFlexibleTime(current.RequestedAt), parseFlexibleTime(current.RequestedAtMs), parseFlexibleTime(legacy.RequestedAt), parseFlexibleTime(legacy.RequestedAtMs)),
		Latency:         firstNonZeroDuration(parseFlexibleDuration(current.Latency, time.Nanosecond), parseFlexibleDuration(current.LatencyMs, time.Millisecond), parseFlexibleDuration(legacy.Latency, time.Nanosecond), parseFlexibleDuration(legacy.LatencyMs, time.Millisecond)),
		TTFT:            firstNonZeroDuration(parseFlexibleDuration(current.TTFT, time.Nanosecond), parseFlexibleDuration(current.TTFTMs, time.Millisecond), parseFlexibleDuration(legacy.TTFT, time.Nanosecond), parseFlexibleDuration(legacy.TTFTMs, time.Millisecond)),
		Failed:          current.Failed || legacy.Failed,
		Failure:         current.Failure,
		Detail:          current.Detail,
		ResponseHeaders: current.ResponseHeaders,
	}
	if record.Failure == (UsageFailure{}) {
		record.Failure = legacy.Failure
	}
	if record.Detail == (UsageDetail{}) {
		record.Detail = legacy.Detail
	}
	if record.ResponseHeaders == nil {
		record.ResponseHeaders = legacy.ResponseHeaders
	}

	*r = record
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func firstNonZeroDuration(values ...time.Duration) time.Duration {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func parseFlexibleTime(raw json.RawMessage) time.Time {
	raw = trimJSONRaw(raw)
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return ts
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return unixTimeFromFlexibleNumber(value)
		}
		return time.Time{}
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}
	}
	return unixTimeFromFlexibleNumber(value)
}

func unixTimeFromFlexibleNumber(value float64) time.Time {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return time.Time{}
	}
	abs := math.Abs(value)
	switch {
	case abs >= 1e17:
		sec := math.Trunc(value / 1e9)
		nsec := math.Round(value - sec*1e9)
		return time.Unix(int64(sec), int64(nsec)).UTC()
	case abs >= 1e12:
		sec := math.Trunc(value / 1e3)
		nsec := math.Round((value - sec*1e3) * 1e6)
		return time.Unix(int64(sec), int64(nsec)).UTC()
	default:
		sec := math.Trunc(value)
		nsec := math.Round((value - sec) * 1e9)
		return time.Unix(int64(sec), int64(nsec)).UTC()
	}
}

func parseFlexibleDuration(raw json.RawMessage, numberUnit time.Duration) time.Duration {
	raw = trimJSONRaw(raw)
	if len(raw) == 0 {
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0
		}
		if duration, err := time.ParseDuration(text); err == nil {
			return duration
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return durationFromFlexibleNumber(value, numberUnit)
		}
		return 0
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	return durationFromFlexibleNumber(value, numberUnit)
}

func durationFromFlexibleNumber(value float64, numberUnit time.Duration) time.Duration {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return time.Duration(math.Round(value * float64(numberUnit)))
}

func trimJSONRaw(raw json.RawMessage) json.RawMessage {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	return json.RawMessage(text)
}

type UsageFailure struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func (f *UsageFailure) UnmarshalJSON(data []byte) error {
	type usageFailure UsageFailure
	var current usageFailure
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}

	var legacy struct {
		StatusCode int    `json:"StatusCode"`
		Body       string `json:"Body"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	if current.StatusCode == 0 {
		current.StatusCode = legacy.StatusCode
	}
	if current.Body == "" {
		current.Body = legacy.Body
	}

	*f = UsageFailure(current)
	return nil
}

type UsageDetail struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Protocol correlation metadata describes the observation behind the normal
// accounting counters.  TokenStats/UsageDetail intentionally remain the
// dashboard and billing representation; this optional sidecar only answers
// whether a field was actually present and how a translated protocol may have
// bucketed it.
type ProtocolCorrelationMeta struct {
	SchemaVersion int    `json:"schema_version"`
	KnownFields   uint16 `json:"known_fields"`
	InputMode     string `json:"input_mode,omitempty"`
	OutputMode    string `json:"output_mode,omitempty"`
	CacheMode     string `json:"cache_mode,omitempty"`
}

func (d *UsageDetail) UnmarshalJSON(data []byte) error {
	type usageDetail UsageDetail
	var current usageDetail
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}

	var legacy struct {
		InputTokens         int64 `json:"InputTokens"`
		OutputTokens        int64 `json:"OutputTokens"`
		ReasoningTokens     int64 `json:"ReasoningTokens"`
		CachedTokens        int64 `json:"CachedTokens"`
		CacheReadTokens     int64 `json:"CacheReadTokens"`
		CacheCreationTokens int64 `json:"CacheCreationTokens"`
		TotalTokens         int64 `json:"TotalTokens"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	var aliases struct {
		PromptTokens            int64 `json:"prompt_tokens"`
		CompletionTokens        int64 `json:"completion_tokens"`
		PromptCacheReadTokens   int64 `json:"cache_read_input_tokens"`
		PromptCacheCreateTokens int64 `json:"cache_creation_input_tokens"`
		PromptTokensDetails     struct {
			CachedTokens        int64 `json:"cached_tokens"`
			CacheWriteTokens    int64 `json:"cache_write_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails struct {
			CachedTokens        int64 `json:"cached_tokens"`
			CacheWriteTokens    int64 `json:"cache_write_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_tokens"`
		} `json:"input_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}

	if current.InputTokens == 0 {
		current.InputTokens = firstNonZeroInt64(aliases.PromptTokens, legacy.InputTokens)
	}
	if current.OutputTokens == 0 {
		current.OutputTokens = firstNonZeroInt64(aliases.CompletionTokens, legacy.OutputTokens)
	}
	if current.ReasoningTokens == 0 {
		current.ReasoningTokens = firstNonZeroInt64(aliases.CompletionTokensDetails.ReasoningTokens, legacy.ReasoningTokens)
	}
	if current.CachedTokens == 0 {
		current.CachedTokens = firstNonZeroInt64(aliases.PromptTokensDetails.CachedTokens, aliases.InputTokensDetails.CachedTokens, legacy.CachedTokens)
	}
	if current.CacheReadTokens == 0 {
		current.CacheReadTokens = firstNonZeroInt64(aliases.PromptCacheReadTokens, aliases.PromptTokensDetails.CachedTokens, aliases.InputTokensDetails.CachedTokens, legacy.CacheReadTokens)
	}
	if current.CacheCreationTokens == 0 {
		current.CacheCreationTokens = firstNonZeroInt64(
			aliases.PromptCacheCreateTokens,
			aliases.PromptTokensDetails.CacheWriteTokens,
			aliases.PromptTokensDetails.CacheCreationTokens,
			aliases.InputTokensDetails.CacheWriteTokens,
			aliases.InputTokensDetails.CacheCreationTokens,
			legacy.CacheCreationTokens,
		)
	}
	if current.TotalTokens == 0 {
		current.TotalTokens = legacy.TotalTokens
	}

	*d = UsageDetail(current)
	return nil
}

type ManagementRequest struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Query   map[string][]string `json:"query"`
	Body    []byte              `json:"body"`
}

type ManagementResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

type PluginRegisterResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Metadata      PluginMetadata     `json:"metadata"`
	Capabilities  PluginCapabilities `json:"capabilities"`
}

type PluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string      `json:"Name"`
	Type        string      `json:"Type"`
	Default     interface{} `json:"Default"`
	Description string      `json:"Description"`
}

type PluginCapabilities struct {
	UsagePlugin               bool `json:"usage_plugin"`
	ResponseInterceptor       bool `json:"response_interceptor"`
	ResponseStreamInterceptor bool `json:"response_stream_interceptor"`
	ManagementAPI             bool `json:"management_api"`
}

type ManagementRegisterResponse struct {
	Routes    []ManagementRoute    `json:"routes"`
	Resources []ManagementResource `json:"resources"`
}

type ManagementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type ManagementResource struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description"`
}

type ExportPayload struct {
	Version     int                `json:"version"`
	ExportedAt  string             `json:"exported_at"`
	Plugin      string             `json:"plugin,omitempty"`
	DetailCount int64              `json:"detail_count,omitempty"`
	Config      ExportConfig       `json:"config,omitempty"`
	Usage       StatisticsSnapshot `json:"usage"`
}

type ExportConfig struct {
	RetentionDays                 int     `json:"retention_days"`
	MaxDetailsPerModel            int     `json:"max_details_per_model"`
	DedupWindowMinutes            int     `json:"dedup_window_minutes"`
	LogResponseHeaders            string  `json:"log_response_headers,omitempty"`
	StorageEnabled                bool    `json:"storage_enabled"`
	StoragePath                   string  `json:"storage_path,omitempty"`
	StorageFlushSeconds           int     `json:"storage_flush_interval_seconds,omitempty"`
	StorageSnapshotSeconds        int     `json:"storage_snapshot_interval_seconds,omitempty"`
	StorageSnapshotRecordInterval int     `json:"storage_snapshot_record_interval,omitempty"`
	StorageSyncSeconds            int     `json:"storage_sync_interval_seconds,omitempty"`
	StorageSyncRecordInterval     int     `json:"storage_sync_record_interval,omitempty"`
	ExportMaxRecords              int     `json:"export_max_records,omitempty"`
	PriceStoragePath              string  `json:"price_storage_path,omitempty"`
	ModelsDevPricesEnabled        bool    `json:"models_dev_prices_enabled,omitempty"`
	ModelsDevPricesURL            string  `json:"models_dev_prices_url,omitempty"`
	ModelsDevRefreshSeconds       int     `json:"models_dev_prices_refresh_interval_seconds,omitempty"`
	PricingTimezone               string  `json:"pricing_timezone,omitempty"`
	ExchangeRateEnabled           bool    `json:"exchange_rate_enabled,omitempty"`
	ExchangeRateUSD               string  `json:"exchange_rate_usd_cny_url,omitempty"`
	ExchangeRateRefreshSeconds    int     `json:"exchange_rate_refresh_interval_seconds,omitempty"`
	ExchangeRateTimeoutSeconds    int     `json:"exchange_rate_timeout_seconds,omitempty"`
	ExchangeRateFallbackUSDCNY    float64 `json:"exchange_rate_fallback_usd_cny,omitempty"`
}

type StorageStatus struct {
	Enabled                       bool    `json:"enabled"`
	Path                          string  `json:"path,omitempty"`
	LoadedPath                    string  `json:"loaded_path,omitempty"`
	LastFlushAt                   string  `json:"last_flush_at,omitempty"`
	LastSnapshotAt                string  `json:"last_snapshot_at,omitempty"`
	LastCompactionAt              string  `json:"last_compaction_at,omitempty"`
	LastSyncAt                    string  `json:"last_sync_at,omitempty"`
	LastError                     string  `json:"last_error,omitempty"`
	PendingBufferedRecords        int64   `json:"pending_buffered_records,omitempty"`
	PendingSnapshotRecords        int64   `json:"pending_snapshot_records,omitempty"`
	PendingUnsyncedRecords        int64   `json:"pending_unsynced_records,omitempty"`
	WriteQueueLength              int     `json:"write_queue_length,omitempty"`
	WriteQueueCapacity            int     `json:"write_queue_capacity,omitempty"`
	LastWriteBatchRecords         int     `json:"last_write_batch_records,omitempty"`
	LastWriteBatchDurationMs      float64 `json:"last_write_batch_duration_ms,omitempty"`
	LastWriteQueueWaitMs          float64 `json:"last_write_queue_wait_ms,omitempty"`
	WriteBatchesTotal             int64   `json:"write_batches_total,omitempty"`
	WriteRecordsTotal             int64   `json:"write_records_total,omitempty"`
	WriteBatchAvgDurationMs       float64 `json:"write_batch_avg_duration_ms,omitempty"`
	WriteBatchP95DurationMs       float64 `json:"write_batch_p95_duration_ms,omitempty"`
	WriteBatchP99DurationMs       float64 `json:"write_batch_p99_duration_ms,omitempty"`
	WriteQueueWaitAvgMs           float64 `json:"write_queue_wait_avg_ms,omitempty"`
	WriteQueueWaitP95Ms           float64 `json:"write_queue_wait_p95_ms,omitempty"`
	WriteQueueWaitP99Ms           float64 `json:"write_queue_wait_p99_ms,omitempty"`
	WriteQueueWaitMaxMs           float64 `json:"write_queue_wait_max_ms,omitempty"`
	WritePressure                 string  `json:"write_pressure,omitempty"`
	LastCompactedShards           int     `json:"last_compacted_shards,omitempty"`
	CompactedShardsTotal          int64   `json:"compacted_shards_total,omitempty"`
	SnapshotIntervalSeconds       int     `json:"snapshot_interval_seconds,omitempty"`
	SnapshotRecordIntervalRecords int     `json:"snapshot_record_interval_records,omitempty"`
	SyncIntervalSeconds           int     `json:"sync_interval_seconds,omitempty"`
	SyncRecordIntervalRecords     int     `json:"sync_record_interval_records,omitempty"`
}

type ModelPrice struct {
	Prompt     float64          `json:"prompt"`
	Completion float64          `json:"completion"`
	Cache      float64          `json:"cache"`
	CacheWrite float64          `json:"cache_write"`
	TimeRules  []ModelPriceRule `json:"time_rules,omitempty"`
}

type ModelPriceRule struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Days limits the rule to specific weekdays, 0 = Sunday through 6 = Saturday
	// (matching time.Weekday and JavaScript's Date#getDay). Empty or absent
	// means every day, which is what every rule written before this field
	// existed meant — legacy price files keep their all-week semantics.
	//
	// A rule is matched against the CALENDAR weekday of the request instant in
	// the pricing timezone. For a rule that crosses midnight (22:00–06:00) that
	// means the 00:00–06:00 half is governed by the weekday it falls on, not by
	// the weekday the interval started on: with days = Mon–Fri, Saturday 02:00
	// is NOT covered even though the interval began Friday 22:00.
	Days       []int    `json:"days,omitempty"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Prompt     *float64 `json:"prompt,omitempty"`
	Completion *float64 `json:"completion,omitempty"`
	Cache      *float64 `json:"cache,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

func (p *ModelPrice) UnmarshalJSON(data []byte) error {
	var raw struct {
		Prompt     float64          `json:"prompt"`
		Completion float64          `json:"completion"`
		Cache      float64          `json:"cache"`
		CacheWrite *float64         `json:"cache_write"`
		TimeRules  []ModelPriceRule `json:"time_rules"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Prompt = raw.Prompt
	p.Completion = raw.Completion
	p.Cache = raw.Cache
	// Legacy saved prices did not have a separate cache_write field. Unknown
	// prices default to zero; explicit values, including zero, are preserved.
	p.CacheWrite = 0
	if raw.CacheWrite != nil {
		p.CacheWrite = *raw.CacheWrite
	}
	p.TimeRules = raw.TimeRules
	return nil
}

func newModelPriceRuleID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "rule-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return "" + hexByte(b[0]) + hexByte(b[1]) + hexByte(b[2]) + hexByte(b[3]) + "-" +
		hexByte(b[4]) + hexByte(b[5]) + "-" + hexByte(b[6]) + hexByte(b[7]) + "-" +
		hexByte(b[8]) + hexByte(b[9]) + "-" + hexByte(b[10]) + hexByte(b[11]) + hexByte(b[12]) + hexByte(b[13]) + hexByte(b[14]) + hexByte(b[15])
}

func hexByte(value byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[value>>4], digits[value&0x0f]})
}

type ModelPricesResponse struct {
	Prices       map[string]ModelPrice   `json:"prices"`
	ManualPrices map[string]ModelPrice   `json:"manual_prices"`
	UpdatedAt    string                  `json:"updated_at,omitempty"`
	Storage      ModelPriceStorageStatus `json:"storage"`
	ModelsDev    ModelsDevPriceStatus    `json:"models_dev,omitempty"`
	Version      uint64                  `json:"version,omitempty"`
	Total        int                     `json:"total"`
	Limit        int                     `json:"limit"`
	Offset       int                     `json:"offset"`

	dashboardVersion uint64
}

type ModelPriceStorageStatus struct {
	Path       string `json:"path,omitempty"`
	LoadedPath string `json:"loaded_path,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type ModelsDevPriceStatus struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url,omitempty"`
	RefreshSeconds int    `json:"refresh_seconds,omitempty"`
	LastAttemptAt  string `json:"last_attempt_at,omitempty"`
	LastSuccessAt  string `json:"last_success_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	ETag           string `json:"etag,omitempty"`
	PriceCount     int    `json:"price_count,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

type RuntimeStatus struct {
	StartedAt                  string                              `json:"started_at,omitempty"`
	LastRecordedAt             string                              `json:"last_recorded_at,omitempty"`
	SeenCount                  int                                 `json:"seen_count"`
	SummaryVersion             uint64                              `json:"summary_version,omitempty"`
	SummaryCacheValid          bool                                `json:"summary_cache_valid"`
	SummaryCacheHits           int64                               `json:"summary_cache_hits,omitempty"`
	SummaryCacheMisses         int64                               `json:"summary_cache_misses,omitempty"`
	LastSummaryDurationMs      float64                             `json:"last_summary_duration_ms,omitempty"`
	EventCacheEntries          int                                 `json:"event_cache_entries,omitempty"`
	EventCacheHits             int64                               `json:"event_cache_hits,omitempty"`
	EventCacheMisses           int64                               `json:"event_cache_misses,omitempty"`
	LastEventsQueryDurationMs  float64                             `json:"last_events_query_duration_ms,omitempty"`
	LastEventsQueryTotal       int                                 `json:"last_events_query_total,omitempty"`
	EventIndexVersion          uint64                              `json:"event_index_version,omitempty"`
	EventIndexEntries          int                                 `json:"event_index_entries,omitempty"`
	APIDetailQueries           int64                               `json:"api_detail_queries,omitempty"`
	LastAPIDetailDurationMs    float64                             `json:"last_api_detail_duration_ms,omitempty"`
	LastAPIDetailTotalEvents   int                                 `json:"last_api_detail_total_events,omitempty"`
	EventsExportRequests       int64                               `json:"events_export_requests,omitempty"`
	EventsExportGzipRequests   int64                               `json:"events_export_gzip_requests,omitempty"`
	EventsExportTruncatedTotal int64                               `json:"events_export_truncated_total,omitempty"`
	LastEventsExportDurationMs float64                             `json:"last_events_export_duration_ms,omitempty"`
	LastEventsExportFormat     string                              `json:"last_events_export_format,omitempty"`
	LastEventsExportGzip       bool                                `json:"last_events_export_gzip,omitempty"`
	LastEventsExportTotal      int                                 `json:"last_events_export_total,omitempty"`
	LastEventsExported         int                                 `json:"last_events_exported,omitempty"`
	LastEventsExportTruncated  bool                                `json:"last_events_export_truncated,omitempty"`
	LastEventsExportRawBytes   int                                 `json:"last_events_export_raw_bytes,omitempty"`
	LastEventsExportBodyBytes  int                                 `json:"last_events_export_body_bytes,omitempty"`
	ConditionalRequests        map[string]ConditionalRequestStatus `json:"conditional_requests,omitempty"`
	LastImport                 *ImportSummary                      `json:"last_import,omitempty"`
	ExchangeRate               CurrencyState                       `json:"exchange_rate"`
	PricingTimezone            string                              `json:"pricing_timezone,omitempty"`
	PricingTimezoneError       string                              `json:"pricing_timezone_error,omitempty"`
}

type CurrencyState struct {
	Base             string   `json:"base"`
	SupportedDisplay []string `json:"supported_display"`
	USDCNYRate       float64  `json:"usd_cny_rate,omitempty"`
	Source           string   `json:"source,omitempty"`
	FetchedAt        string   `json:"fetched_at,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	Status           string   `json:"status"`
	Error            string   `json:"error,omitempty"`
	ConsecutiveFails int      `json:"consecutive_failures,omitempty"`
}

type ConditionalRequestStatus struct {
	Requests    int64   `json:"requests"`
	NotModified int64   `json:"not_modified"`
	Misses      int64   `json:"misses"`
	HitRate     float64 `json:"hit_rate"`
}

type ImportResponse struct {
	InputRecords       int64 `json:"input_records"`
	AcceptedRecords    int64 `json:"accepted_records"`
	RejectedRecords    int64 `json:"rejected_records"`
	Added              int64 `json:"added"`
	Skipped            int64 `json:"skipped"`
	IgnoredByRetention int64 `json:"ignored_by_retention"`
	TotalRequests      int64 `json:"total_requests"`
	FailedRequests     int64 `json:"failed_requests"`
}

// ============================================================================
// Statistics Types
// ============================================================================

type RequestDetail struct {
	// UpstreamAPI is the exact dashboard grouping key for the upstream
	// interface, for example "codex · 上游 b374b8e7c98ca23c". It is populated
	// only on query result copies and is not stored with recorded details.
	UpstreamAPI    string                   `json:"api,omitempty"`
	Model          string                   `json:"model,omitempty"`
	RequestedModel string                   `json:"requested_model,omitempty"`
	Timestamp      time.Time                `json:"timestamp"`
	LatencyMs      int64                    `json:"latency_ms"`
	TTFTMs         int64                    `json:"ttft_ms,omitempty"`
	APIKey         string                   `json:"api_key,omitempty"`
	APIKeyHash     string                   `json:"api_key_hash,omitempty"`
	Source         string                   `json:"source"`
	Provider       string                   `json:"provider,omitempty"`
	ExecutorType   string                   `json:"executor_type,omitempty"`
	Correlation    *ProtocolCorrelationMeta `json:"correlation,omitempty"`
	AuthID         string                   `json:"auth_id,omitempty"`
	AuthIndex      string                   `json:"auth_index"`
	AuthType       string                   `json:"auth_type,omitempty"`
	Endpoint       string                   `json:"endpoint,omitempty"`
	BaseURL        string                   `json:"base_url,omitempty"`
	Stream         bool                     `json:"stream,omitempty"`
	Thinking       UsageThinking            `json:"thinking,omitempty"`
	Tokens         TokenStats               `json:"tokens"`
	Failed         bool                     `json:"failed"`
	StatusCode     int                      `json:"status_code,omitempty"`
	Failure        string                   `json:"failure,omitempty"`
	Headers        map[string][]string      `json:"headers,omitempty"`
	// TimestampSynthetic 标记 Timestamp 是导入/恢复时补出来的,不是真实请求时间。
	// 分桶、排序、retention 仍需要一个可用的时间,所以时间戳照样填,但时段价格必须
	// 按图纸「无时间戳记录维持基础价」回落,不能拿服务器当时的钟点去套峰谷规则。
	// 需要持久化:否则重启后这条记录看起来就是一条普通的带时间戳记录。
	TimestampSynthetic bool `json:"timestamp_synthetic,omitempty"`
	// CostUSD 是查询期附加的后端估算成本,存储记录里为 nil。用指针而不是
	// float64+omitempty:合法的零价时段成本必须以 "cost_usd":0 出现在事件接口和
	// 导出里,否则看板会当作「后端没给成本」而按基础价格重算,把免费时段显示成
	// 收费。nil(未计算)与 0(确为零价)必须可区分。
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	// CacheReadTokens 保留原生记录的权威缓存命中值。旧版本不写该字段;它的存在
	// 证明明细由修复后的入口(已剔除 CPA CachedTokens 回填)写入,历史缓存修复
	// 据此免除误判(见 claude_cache_repair.go)。
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheTokens      int64 `json:"cache_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type UsageThinking struct {
	Intensity string `json:"intensity,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Level     string `json:"level,omitempty"`
	Budget    int64  `json:"budget,omitempty"`
}

type StatisticsSnapshot struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessCount     int64   `json:"success_count"`
	FailureCount     int64   `json:"failure_count"`
	TotalTokens      int64   `json:"total_tokens"`
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	CachedTokens     int64   `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	AvgLatencyMs     float64 `json:"avg_latency_ms,omitempty"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay    map[string]int64                 `json:"requests_by_day"`
	RequestsByHour   map[string]int64                 `json:"requests_by_hour"`
	TokensByDay      map[string]int64                 `json:"tokens_by_day"`
	TokensByHour     map[string]int64                 `json:"tokens_by_hour"`
	CostByDay        map[string]float64               `json:"cost_by_day,omitempty"`
	CostByHour       map[string]float64               `json:"cost_by_hour,omitempty"`
	CostTokensByDay  map[string][]TimeSeriesTokenStat `json:"cost_tokens_by_day,omitempty"`
	CostTokensByHour map[string][]TimeSeriesTokenStat `json:"cost_tokens_by_hour,omitempty"`
}

type APISnapshot struct {
	TotalRequests    int64                    `json:"total_requests"`
	SuccessCount     int64                    `json:"success_count"`
	FailureCount     int64                    `json:"failure_count"`
	TotalTokens      int64                    `json:"total_tokens"`
	InputTokens      int64                    `json:"input_tokens,omitempty"`
	OutputTokens     int64                    `json:"output_tokens,omitempty"`
	CachedTokens     int64                    `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64                    `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64                    `json:"reasoning_tokens,omitempty"`
	AvgLatencyMs     float64                  `json:"avg_latency_ms,omitempty"`
	Models           map[string]ModelSnapshot `json:"models"`
}

type TimeSeriesTokenStat struct {
	Model            string `json:"model"`
	Provider         string `json:"provider,omitempty"`
	TotalTokens      int64  `json:"total_tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
}

type ModelSnapshot struct {
	TotalRequests    int64               `json:"total_requests"`
	SuccessCount     int64               `json:"success_count"`
	FailureCount     int64               `json:"failure_count"`
	TotalTokens      int64               `json:"total_tokens"`
	InputTokens      int64               `json:"input_tokens,omitempty"`
	OutputTokens     int64               `json:"output_tokens,omitempty"`
	CachedTokens     int64               `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64               `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64               `json:"reasoning_tokens,omitempty"`
	AvgLatencyMs     float64             `json:"avg_latency_ms,omitempty"`
	Providers        []ModelProviderStat `json:"providers,omitempty"`
	Details          []RequestDetail     `json:"details"`
}

type MergeResult struct {
	Added              int64 `json:"added"`
	Skipped            int64 `json:"skipped"`
	IgnoredByRetention int64 `json:"ignored_by_retention"`
}

// ============================================================================
// New Types for P0 Lightweight Dashboard & P1 Observability
// ============================================================================

// StatisticsSnapshotWithoutDetails mirrors StatisticsSnapshot but models
// carry only aggregated counts -- no details arrays.
type StatisticsSnapshotWithoutDetails struct {
	TotalRequests    int64                                `json:"total_requests"`
	SuccessCount     int64                                `json:"success_count"`
	FailureCount     int64                                `json:"failure_count"`
	TotalTokens      int64                                `json:"total_tokens"`
	InputTokens      int64                                `json:"input_tokens"`
	OutputTokens     int64                                `json:"output_tokens"`
	CachedTokens     int64                                `json:"cached_tokens"`
	CacheWriteTokens int64                                `json:"cache_write_tokens"`
	ReasoningTokens  int64                                `json:"reasoning_tokens"`
	AvgLatencyMs     float64                              `json:"avg_latency_ms"`
	TotalCost        float64                              `json:"total_cost"`
	APIs             map[string]APISnapshotWithoutDetails `json:"apis"`
	RequestsByDay    map[string]int64                     `json:"requests_by_day"`
	RequestsByHour   map[string]int64                     `json:"requests_by_hour"`
	TokensByDay      map[string]int64                     `json:"tokens_by_day"`
	TokensByHour     map[string]int64                     `json:"tokens_by_hour"`
	CostByDay        map[string]float64                   `json:"cost_by_day,omitempty"`
	CostByHour       map[string]float64                   `json:"cost_by_hour,omitempty"`
}

type APISnapshotWithoutDetails struct {
	TotalRequests    int64                                  `json:"total_requests"`
	SuccessCount     int64                                  `json:"success_count"`
	FailureCount     int64                                  `json:"failure_count"`
	TotalTokens      int64                                  `json:"total_tokens"`
	InputTokens      int64                                  `json:"input_tokens"`
	OutputTokens     int64                                  `json:"output_tokens"`
	CachedTokens     int64                                  `json:"cached_tokens"`
	CacheWriteTokens int64                                  `json:"cache_write_tokens"`
	ReasoningTokens  int64                                  `json:"reasoning_tokens"`
	AvgLatencyMs     float64                                `json:"avg_latency_ms"`
	EstimatedCost    float64                                `json:"estimated_cost"`
	Models           map[string]ModelSnapshotWithoutDetails `json:"models"`
}

type ModelSnapshotWithoutDetails struct {
	TotalRequests    int64               `json:"total_requests"`
	SuccessCount     int64               `json:"success_count"`
	FailureCount     int64               `json:"failure_count"`
	TotalTokens      int64               `json:"total_tokens"`
	InputTokens      int64               `json:"input_tokens"`
	OutputTokens     int64               `json:"output_tokens"`
	CachedTokens     int64               `json:"cached_tokens"`
	CacheWriteTokens int64               `json:"cache_write_tokens"`
	ReasoningTokens  int64               `json:"reasoning_tokens"`
	AvgLatencyMs     float64             `json:"avg_latency_ms"`
	EstimatedCost    float64             `json:"estimated_cost"`
	Providers        []ModelProviderStat `json:"providers,omitempty"`
}

// HealthGridSlot is one 15-minute bucket for the health grid (672 slots = 7 days).
type HealthGridSlot struct {
	Slot    int    `json:"slot"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Failure int64  `json:"failure"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// CompactHealthGrid avoids repeating timestamps and empty slots in every
// dashboard summary. Slots contain [index, success, failure].
type CompactHealthGrid struct {
	Start       string     `json:"start"`
	StepSeconds int64      `json:"step_seconds"`
	Count       int        `json:"count"`
	Slots       [][3]int64 `json:"slots"`
}

// SourceStat aggregates request stats by source label.
type SourceStat struct {
	Source        string `json:"source"`
	Provider      string `json:"provider,omitempty"`
	TotalRequests int64  `json:"total_requests"`
	SuccessCount  int64  `json:"success_count"`
	FailureCount  int64  `json:"failure_count"`
	TotalTokens   int64  `json:"total_tokens"`
}

// CredentialStat aggregates request stats by CPA credential (auth_index).
type CredentialStat struct {
	AuthIndex     string `json:"auth_index"`
	TotalRequests int64  `json:"total_requests"`
	SuccessCount  int64  `json:"success_count"`
	FailureCount  int64  `json:"failure_count"`
	TotalTokens   int64  `json:"total_tokens"`
}

// ClientAPIStat aggregates request stats by the API key used to call CPA.
// The key value is masked; APIKeyHash is only a grouping/debug identifier.
type ClientAPIStat struct {
	APIKey           string               `json:"api_key"`
	APIKeyHash       string               `json:"api_key_hash,omitempty"`
	Selector         string               `json:"selector,omitempty"`
	TotalRequests    int64                `json:"total_requests"`
	SuccessCount     int64                `json:"success_count"`
	FailureCount     int64                `json:"failure_count"`
	TotalTokens      int64                `json:"total_tokens"`
	InputTokens      int64                `json:"input_tokens"`
	OutputTokens     int64                `json:"output_tokens"`
	CachedTokens     int64                `json:"cached_tokens"`
	CacheWriteTokens int64                `json:"cache_write_tokens"`
	ReasoningTokens  int64                `json:"reasoning_tokens"`
	EstimatedCost    float64              `json:"estimated_cost"`
	Models           []ClientAPIModelStat `json:"models,omitempty"`
}

type ClientAPIModelStat struct {
	Model            string              `json:"model"`
	TotalRequests    int64               `json:"total_requests"`
	SuccessCount     int64               `json:"success_count"`
	FailureCount     int64               `json:"failure_count"`
	TotalTokens      int64               `json:"total_tokens"`
	InputTokens      int64               `json:"input_tokens"`
	OutputTokens     int64               `json:"output_tokens"`
	CachedTokens     int64               `json:"cached_tokens"`
	CacheWriteTokens int64               `json:"cache_write_tokens"`
	ReasoningTokens  int64               `json:"reasoning_tokens"`
	EstimatedCost    float64             `json:"estimated_cost"`
	Providers        []ModelProviderStat `json:"providers,omitempty"`

	providerStats map[string]*ModelProviderStat `json:"-"`
}

type ModelProviderStat struct {
	Provider         string `json:"provider"`
	TotalRequests    int64  `json:"total_requests"`
	SuccessCount     int64  `json:"success_count"`
	FailureCount     int64  `json:"failure_count"`
	TotalTokens      int64  `json:"total_tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
}

// ModelStat aggregates request stats by model name across all APIs.
type ModelStat struct {
	Model            string              `json:"model"`
	TotalRequests    int64               `json:"total_requests"`
	SuccessCount     int64               `json:"success_count"`
	FailureCount     int64               `json:"failure_count"`
	TotalTokens      int64               `json:"total_tokens"`
	InputTokens      int64               `json:"input_tokens"`
	OutputTokens     int64               `json:"output_tokens"`
	AvgLatencyMs     float64             `json:"avg_latency_ms"`
	CachedTokens     int64               `json:"cached_tokens"`
	CacheWriteTokens int64               `json:"cache_write_tokens"`
	ReasoningTokens  int64               `json:"reasoning_tokens"`
	EstimatedCost    float64             `json:"estimated_cost"`
	Providers        []ModelProviderStat `json:"providers,omitempty"`

	// Internal accumulators for computing AvgLatencyMs; not serialized.
	latencySum    int64                         `json:"-"`
	latencyN      int64                         `json:"-"`
	providerStats map[string]*ModelProviderStat `json:"-"`
}

// DashboardSummary is the full lightweight dashboard response with metadata.
type DashboardSummary struct {
	Usage           StatisticsSnapshotWithoutDetails `json:"usage"`
	HealthGrid      []HealthGridSlot                 `json:"health_grid,omitempty"`
	HealthGridV2    *CompactHealthGrid               `json:"health_grid_v2,omitempty"`
	SourceStats     []SourceStat                     `json:"source_stats"`
	CredentialStats []CredentialStat                 `json:"credential_stats"`
	ClientAPIStats  []ClientAPIStat                  `json:"client_api_stats"`

	ModelStats  []ModelStat   `json:"model_stats"`
	GeneratedAt string        `json:"generated_at"`
	Meta        DashboardMeta `json:"_meta"`
}

// DashboardMeta carries observability metadata.
type DashboardMeta struct {
	RetentionDays      int            `json:"retention_days"`
	MaxDetailsPerModel int            `json:"max_details_per_model"`
	CurrentDetailCount int64          `json:"current_detail_count"`
	CurrentHour        int            `json:"current_hour"`
	LastRecordedAt     string         `json:"last_recorded_at,omitempty"`
	SummaryVersion     uint64         `json:"summary_version,omitempty"`
	PriceVersion       uint64         `json:"price_version,omitempty"`
	Storage            StorageStatus  `json:"storage"`
	LastImport         *ImportSummary `json:"last_import,omitempty"`
	EvictedTotal       int64          `json:"evicted_total"`
	Currency           CurrencyState  `json:"currency"`
}

// ImportSummary is a lightweight snapshot of the last import result.
type ImportSummary struct {
	Added              int64 `json:"added"`
	Skipped            int64 `json:"skipped"`
	IgnoredByRetention int64 `json:"ignored_by_retention"`
}

// EventsQuery represents query parameters for the paginated events endpoint.
type EventsQuery struct {
	Limit     int
	Offset    int
	Range     string // "", "7h", "24h", "7d", "all"
	Model     string
	Source    string
	AuthIndex string
	API       string
	ClientAPI string
}

// EventsResult is the response from the events endpoint.
type EventsResult struct {
	Events      []RequestDetail `json:"events"`
	Total       int             `json:"total"`
	Limit       int             `json:"limit"`
	Offset      int             `json:"offset"`
	Truncated   bool            `json:"truncated,omitempty"`
	GeneratedAt string          `json:"generated_at"`

	dashboardVersion uint64
}

// APIDetailSummary is the range-scoped summary for one upstream API.
type APIDetailSummary struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessCount     int64   `json:"success_count"`
	FailureCount     int64   `json:"failure_count"`
	TotalTokens      int64   `json:"total_tokens"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// APIDetailErrorStat aggregates failures by status code and redacted body.
type APIDetailErrorStat struct {
	StatusCode int    `json:"status_code,omitempty"`
	Count      int64  `json:"count"`
	Failure    string `json:"failure"`
}

// APIDetailResponse is a compact backend-rendered detail payload for one API.
type APIDetailResponse struct {
	API          string               `json:"api"`
	Summary      APIDetailSummary     `json:"summary"`
	ModelStats   []ModelStat          `json:"model_stats"`
	SourceStats  []SourceStat         `json:"source_stats"`
	ErrorStats   []APIDetailErrorStat `json:"error_stats"`
	RecentEvents []RequestDetail      `json:"recent_events"`
	TotalEvents  int                  `json:"total_events"`
	GeneratedAt  string               `json:"generated_at"`

	dashboardVersion uint64
}
