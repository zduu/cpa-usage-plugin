package main

import (
	"encoding/json"
	"time"
)

const (
	abiVersion                 uint32 = 1
	defaultMaxDetailsPerModel         = 5000
	defaultRetentionDays              = 30
	defaultDedupWindowMinutes         = 24 * 60
	defaultStorageFlushSeconds        = 30
	defaultPriceStoragePath           = "usage-statistics-prices.json"
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
	MaxDetailsPerModel  int
	RetentionDays       int
	DedupWindowMinutes  int
	LogResponseHeaders  string
	APIKeyHashSalt      string
	StorageEnabled      bool
	StoragePath         string
	StorageFlushSeconds int
	PriceStoragePath    string
	UpdateEnabled       bool
	UpdateVersion       string
}

type runtimeConfigPatch struct {
	MaxDetailsPerModel  *int
	RetentionDays       *int
	DedupWindowMinutes  *int
	LogResponseHeaders  *string
	APIKeyHashSalt      *string
	StorageEnabled      *bool
	StoragePath         *string
	StorageFlushSeconds *int
	PriceStoragePath    *string
	UpdateEnabled       *bool
	UpdateVersion       *string
}

func defaultRuntimeConfig() runtimeConfig {
	return runtimeConfig{
		MaxDetailsPerModel:  defaultMaxDetailsPerModel,
		RetentionDays:       defaultRetentionDays,
		DedupWindowMinutes:  defaultDedupWindowMinutes,
		LogResponseHeaders:  "",
		APIKeyHashSalt:      "",
		StorageEnabled:      false,
		StoragePath:         "usage-statistics.jsonl",
		StorageFlushSeconds: defaultStorageFlushSeconds,
		PriceStoragePath:    defaultPriceStoragePath,
		UpdateEnabled:       false,
		UpdateVersion:       "latest",
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
	BaseURL         string              `json:"base_url"`
	Source          string              `json:"source"`
	ReasoningEffort string              `json:"reasoning_effort"`
	ServiceTier     string              `json:"service_tier"`
	RequestedAt     time.Time           `json:"requested_at"`
	Latency         time.Duration       `json:"latency"`
	TTFT            time.Duration       `json:"ttft"`
	Failed          bool                `json:"failed"`
	Failure         UsageFailure        `json:"failure"`
	Detail          UsageDetail         `json:"detail"`
	ResponseHeaders map[string][]string `json:"response_headers"`
}

func (r *UsageRecord) UnmarshalJSON(data []byte) error {
	type usageRecord UsageRecord
	var current usageRecord
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
		BaseURL         string              `json:"BaseURL"`
		BaseUrl         string              `json:"BaseUrl"`
		Source          string              `json:"Source"`
		ReasoningEffort string              `json:"ReasoningEffort"`
		ServiceTier     string              `json:"ServiceTier"`
		RequestedAt     time.Time           `json:"RequestedAt"`
		Latency         time.Duration       `json:"Latency"`
		TTFT            time.Duration       `json:"TTFT"`
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
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}

	if current.Provider == "" {
		current.Provider = legacy.Provider
	}
	if current.ExecutorType == "" {
		current.ExecutorType = legacy.ExecutorType
	}
	if current.Model == "" {
		current.Model = legacy.Model
	}
	if current.Alias == "" {
		current.Alias = legacy.Alias
	}
	if current.APIKey == "" {
		current.APIKey = legacy.APIKey
	}
	if current.AuthID == "" {
		current.AuthID = legacy.AuthID
	}
	if current.AuthIndex == "" {
		current.AuthIndex = legacy.AuthIndex
	}
	if current.AuthType == "" {
		current.AuthType = legacy.AuthType
	}
	if current.BaseURL == "" {
		current.BaseURL = aliases.BaseURLCamel
	}
	if current.BaseURL == "" {
		current.BaseURL = aliases.BaseUrlCamel
	}
	if current.BaseURL == "" {
		current.BaseURL = legacy.BaseURL
	}
	if current.BaseURL == "" {
		current.BaseURL = legacy.BaseUrl
	}
	if current.Source == "" {
		current.Source = legacy.Source
	}
	if current.ReasoningEffort == "" {
		current.ReasoningEffort = legacy.ReasoningEffort
	}
	if current.ServiceTier == "" {
		current.ServiceTier = legacy.ServiceTier
	}
	if current.RequestedAt.IsZero() {
		current.RequestedAt = legacy.RequestedAt
	}
	if current.Latency == 0 {
		current.Latency = legacy.Latency
	}
	if current.TTFT == 0 {
		current.TTFT = legacy.TTFT
	}
	current.Failed = current.Failed || legacy.Failed
	if current.Failure == (UsageFailure{}) {
		current.Failure = legacy.Failure
	}
	if current.Detail == (UsageDetail{}) {
		current.Detail = legacy.Detail
	}
	if current.ResponseHeaders == nil {
		current.ResponseHeaders = legacy.ResponseHeaders
	}

	*r = UsageRecord(current)
	return nil
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

	if current.InputTokens == 0 {
		current.InputTokens = legacy.InputTokens
	}
	if current.OutputTokens == 0 {
		current.OutputTokens = legacy.OutputTokens
	}
	if current.ReasoningTokens == 0 {
		current.ReasoningTokens = legacy.ReasoningTokens
	}
	if current.CachedTokens == 0 {
		current.CachedTokens = legacy.CachedTokens
	}
	if current.CacheReadTokens == 0 {
		current.CacheReadTokens = legacy.CacheReadTokens
	}
	if current.CacheCreationTokens == 0 {
		current.CacheCreationTokens = legacy.CacheCreationTokens
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
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
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
	RetentionDays      int    `json:"retention_days"`
	MaxDetailsPerModel int    `json:"max_details_per_model"`
	DedupWindowMinutes int    `json:"dedup_window_minutes"`
	LogResponseHeaders string `json:"log_response_headers,omitempty"`
	StorageEnabled     bool   `json:"storage_enabled"`
	StoragePath        string `json:"storage_path,omitempty"`
	PriceStoragePath   string `json:"price_storage_path,omitempty"`
}

type StorageStatus struct {
	Enabled                bool   `json:"enabled"`
	Path                   string `json:"path,omitempty"`
	LoadedPath             string `json:"loaded_path,omitempty"`
	LastFlushAt            string `json:"last_flush_at,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	PendingBufferedRecords int64  `json:"pending_buffered_records,omitempty"`
}

type ModelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
	Cache      float64 `json:"cache"`
}

type ModelPricesResponse struct {
	Prices    map[string]ModelPrice   `json:"prices"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
	Storage   ModelPriceStorageStatus `json:"storage"`
}

type ModelPriceStorageStatus struct {
	Path       string `json:"path,omitempty"`
	LoadedPath string `json:"loaded_path,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type RuntimeStatus struct {
	StartedAt      string         `json:"started_at,omitempty"`
	LastRecordedAt string         `json:"last_recorded_at,omitempty"`
	SeenCount      int            `json:"seen_count"`
	LastImport     *ImportSummary `json:"last_import,omitempty"`
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
	Model      string              `json:"model,omitempty"`
	Timestamp  time.Time           `json:"timestamp"`
	LatencyMs  int64               `json:"latency_ms"`
	TTFTMs     int64               `json:"ttft_ms,omitempty"`
	APIKey     string              `json:"api_key,omitempty"`
	APIKeyHash string              `json:"api_key_hash,omitempty"`
	Source     string              `json:"source"`
	Provider   string              `json:"provider,omitempty"`
	AuthID     string              `json:"auth_id,omitempty"`
	AuthIndex  string              `json:"auth_index"`
	AuthType   string              `json:"auth_type,omitempty"`
	BaseURL    string              `json:"base_url,omitempty"`
	Thinking   UsageThinking       `json:"thinking,omitempty"`
	Tokens     TokenStats          `json:"tokens"`
	Failed     bool                `json:"failed"`
	StatusCode int                 `json:"status_code,omitempty"`
	Failure    string              `json:"failure,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
}

type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	CacheTokens     int64 `json:"cache_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

type UsageThinking struct {
	Intensity string `json:"intensity,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Level     string `json:"level,omitempty"`
	Budget    int64  `json:"budget,omitempty"`
}

type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	SuccessCount  int64                    `json:"success_count"`
	FailureCount  int64                    `json:"failure_count"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	SuccessCount  int64           `json:"success_count"`
	FailureCount  int64           `json:"failure_count"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
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
	TotalRequests   int64                                `json:"total_requests"`
	SuccessCount    int64                                `json:"success_count"`
	FailureCount    int64                                `json:"failure_count"`
	TotalTokens     int64                                `json:"total_tokens"`
	InputTokens     int64                                `json:"input_tokens"`
	OutputTokens    int64                                `json:"output_tokens"`
	CachedTokens    int64                                `json:"cached_tokens"`
	ReasoningTokens int64                                `json:"reasoning_tokens"`
	AvgLatencyMs    float64                              `json:"avg_latency_ms"`
	APIs            map[string]APISnapshotWithoutDetails `json:"apis"`
	RequestsByDay   map[string]int64                     `json:"requests_by_day"`
	RequestsByHour  map[string]int64                     `json:"requests_by_hour"`
	TokensByDay     map[string]int64                     `json:"tokens_by_day"`
	TokensByHour    map[string]int64                     `json:"tokens_by_hour"`
}

type APISnapshotWithoutDetails struct {
	TotalRequests   int64                                  `json:"total_requests"`
	SuccessCount    int64                                  `json:"success_count"`
	FailureCount    int64                                  `json:"failure_count"`
	TotalTokens     int64                                  `json:"total_tokens"`
	InputTokens     int64                                  `json:"input_tokens"`
	OutputTokens    int64                                  `json:"output_tokens"`
	CachedTokens    int64                                  `json:"cached_tokens"`
	ReasoningTokens int64                                  `json:"reasoning_tokens"`
	AvgLatencyMs    float64                                `json:"avg_latency_ms"`
	Models          map[string]ModelSnapshotWithoutDetails `json:"models"`
}

type ModelSnapshotWithoutDetails struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessCount    int64   `json:"success_count"`
	FailureCount    int64   `json:"failure_count"`
	TotalTokens     int64   `json:"total_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
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
	APIKey          string               `json:"api_key"`
	APIKeyHash      string               `json:"api_key_hash,omitempty"`
	TotalRequests   int64                `json:"total_requests"`
	SuccessCount    int64                `json:"success_count"`
	FailureCount    int64                `json:"failure_count"`
	TotalTokens     int64                `json:"total_tokens"`
	InputTokens     int64                `json:"input_tokens"`
	OutputTokens    int64                `json:"output_tokens"`
	CachedTokens    int64                `json:"cached_tokens"`
	ReasoningTokens int64                `json:"reasoning_tokens"`
	Models          []ClientAPIModelStat `json:"models,omitempty"`
}

type ClientAPIModelStat struct {
	Model           string `json:"model"`
	TotalRequests   int64  `json:"total_requests"`
	SuccessCount    int64  `json:"success_count"`
	FailureCount    int64  `json:"failure_count"`
	TotalTokens     int64  `json:"total_tokens"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
}

// ModelStat aggregates request stats by model name across all APIs.
type ModelStat struct {
	Model           string  `json:"model"`
	TotalRequests   int64   `json:"total_requests"`
	SuccessCount    int64   `json:"success_count"`
	FailureCount    int64   `json:"failure_count"`
	TotalTokens     int64   `json:"total_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`

	// Internal accumulators for computing AvgLatencyMs; not serialized.
	latencySum int64 `json:"-"`
	latencyN   int64 `json:"-"`
}

// DashboardSummary is the full lightweight dashboard response with metadata.
type DashboardSummary struct {
	Usage           StatisticsSnapshotWithoutDetails `json:"usage"`
	HealthGrid      []HealthGridSlot                 `json:"health_grid"`
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
	LastRecordedAt     string         `json:"last_recorded_at,omitempty"`
	Storage            StorageStatus  `json:"storage"`
	LastImport         *ImportSummary `json:"last_import,omitempty"`
	EvictedTotal       int64          `json:"evicted_total"`
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
}

// EventsResult is the response from the events endpoint.
type EventsResult struct {
	Events      []RequestDetail `json:"events"`
	Total       int             `json:"total"`
	Limit       int             `json:"limit"`
	Offset      int             `json:"offset"`
	GeneratedAt string          `json:"generated_at"`
}

// APIDetailSummary is the range-scoped summary for one upstream API.
type APIDetailSummary struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessCount    int64   `json:"success_count"`
	FailureCount    int64   `json:"failure_count"`
	TotalTokens     int64   `json:"total_tokens"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
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
}
