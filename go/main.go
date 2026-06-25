package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const abiVersion uint32 = 1

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBody []byte
	if request != nil && requestLen > 0 {
		requestBody = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, errHandle := handleMethod(C.GoString(method), requestBody)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// Cleanup if needed
}

func handleMethod(method string, requestBody []byte) ([]byte, error) {
	switch method {
	case "plugin.register":
		return handleRegister()
	case "plugin.reconfigure":
		return handleReconfigure()
	case "management.register":
		return handleManagementRegister()
	case "usage.handle":
		return handleUsage(requestBody)
	case "management.handle":
		return handleManagement(requestBody)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleRegister() ([]byte, error) {
	metadata := map[string]interface{}{
		"Name":             "用量统计",
		"Version":          "1.0.0",
		"Author":           "本地维护",
		"GitHubRepository": "https://github.com/zduu/cpa-usage-plugin",
		"Logo":             "",
		"ConfigFields":     []interface{}{},
	}

	capabilities := map[string]interface{}{
		"usage_plugin":   true,
		"management_api": true,
	}

	result := map[string]interface{}{
		"schema_version": 1,
		"metadata":       metadata,
		"capabilities":   capabilities,
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	return okEnvelopeJSON(string(resultJSON))
}

func handleReconfigure() ([]byte, error) {
	return handleRegister()
}

func handleUsage(requestBody []byte) ([]byte, error) {
	var usageRecord UsageRecord
	if err := json.Unmarshal(requestBody, &usageRecord); err != nil {
		return nil, fmt.Errorf("failed to parse usage record: %w", err)
	}

	stats.Record(usageRecord)

	return okEnvelopeJSON("{}")
}

func handleManagement(requestBody []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return nil, fmt.Errorf("failed to parse management request: %w", err)
	}

	// Route based on path
	if req.Method == "GET" && strings.HasSuffix(req.Path, "/dashboard") {
		return handleDashboardPage()
	} else if req.Method == "GET" && strings.HasSuffix(req.Path, "/dashboard-data") {
		return handleDashboardData()
	} else if req.Method == "GET" && strings.HasSuffix(req.Path, "/usage") {
		return handleGetUsage()
	} else if req.Method == "GET" && strings.HasSuffix(req.Path, "/usage/export") {
		return handleExportUsage()
	} else if req.Method == "POST" && strings.HasSuffix(req.Path, "/usage/import") {
		return handleImportUsage(req.Body)
	}

	return errorEnvelope("not_found", "endpoint not found"), nil
}

func handleManagementRegister() ([]byte, error) {
	result := map[string]interface{}{
		"routes": []map[string]interface{}{
			{
				"method":      "GET",
				"path":        "/plugins/usage-statistics/usage",
				"description": "获取用量统计数据。",
			},
			{
				"method":      "GET",
				"path":        "/plugins/usage-statistics/usage/export",
				"description": "导出用量统计数据。",
			},
			{
				"method":      "POST",
				"path":        "/plugins/usage-statistics/usage/import",
				"description": "导入用量统计数据。",
			},
		},
		"resources": []map[string]interface{}{
			{
				"path":        "/dashboard",
				"menu":        "用量统计",
				"description": "请求、令牌和模型用量统计。",
			},
			{
				"path":        "/dashboard-data",
				"description": "用量统计看板数据。",
			},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return okEnvelopeJSON(string(raw))
}

func handleDashboardData() ([]byte, error) {
	snapshot := stats.Snapshot()
	responseData := map[string]interface{}{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
	}
	responseJSON, err := json.Marshal(responseData)
	if err != nil {
		return nil, err
	}

	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type":  {"application/json; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: responseJSON,
	}

	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func handleDashboardPage() ([]byte, error) {
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type":  {"text/html; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: []byte(dashboardHTML),
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func handleGetUsage() ([]byte, error) {
	snapshot := stats.Snapshot()

	responseData := map[string]interface{}{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	}

	responseJSON, err := json.Marshal(responseData)
	if err != nil {
		return nil, err
	}

	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: responseJSON,
	}

	return okEnvelopeJSON(string(mustMarshal(resp)))
}

const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>用量统计</title>
<style>
:root{color-scheme:light dark;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f7f8fb;color:#18202f}
*{box-sizing:border-box}
body{margin:0;padding:28px;background:linear-gradient(180deg,#f7f8fb,#eef2f7);min-height:100vh}
.shell{max-width:1120px;margin:0 auto}
.header{display:flex;justify-content:space-between;gap:16px;align-items:flex-end;margin-bottom:20px}
h1{margin:0;font-size:28px;line-height:1.15}
.muted{color:#687386;font-size:13px}
.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin-bottom:16px}
.card,.panel{background:#fff;border:1px solid #dbe2ea;border-radius:8px;box-shadow:0 8px 22px rgba(20,32,50,.06)}
.card{padding:16px}
.label{font-size:12px;color:#687386;margin-bottom:8px}
.value{font-size:26px;font-weight:750;font-variant-numeric:tabular-nums}
.panel{padding:16px;margin-top:12px}
.panel h2{font-size:16px;margin:0 0 12px}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:10px;border-bottom:1px solid #e6ebf1}
th{font-size:11px;text-transform:uppercase;color:#687386}
tr:last-child td{border-bottom:0}
.empty{padding:26px;text-align:center;color:#687386}
.status{display:inline-flex;align-items:center;gap:8px;padding:7px 10px;border:1px solid #dbe2ea;border-radius:999px;background:#fff;font-size:12px;color:#405066}
.dot{width:8px;height:8px;border-radius:999px;background:#22c55e}
@media(max-width:760px){body{padding:16px}.header{display:block}.grid{grid-template-columns:1fr 1fr}.value{font-size:22px}}
@media(max-width:460px){.grid{grid-template-columns:1fr}}
@media(prefers-color-scheme:dark){:root{background:#10141c;color:#eef3f9}body{background:linear-gradient(180deg,#10141c,#151b25)}.card,.panel,.status{background:#171e29;border-color:#2b3544;box-shadow:none}.muted,.label,th,.empty{color:#9ba8ba}td,th{border-color:#2b3544}}
</style>
</head>
<body>
<main class="shell">
  <div class="header">
    <div>
      <h1>用量统计</h1>
      <div class="muted" id="subtitle">正在加载用量统计...</div>
    </div>
    <div class="status"><span class="dot"></span><span id="status">正常</span></div>
  </div>
  <section class="grid">
    <div class="card"><div class="label">总请求数</div><div class="value" id="totalRequests">-</div></div>
    <div class="card"><div class="label">成功请求</div><div class="value" id="successCount">-</div></div>
    <div class="card"><div class="label">失败请求</div><div class="value" id="failureCount">-</div></div>
    <div class="card"><div class="label">总令牌数</div><div class="value" id="totalTokens">-</div></div>
  </section>
  <section class="panel">
    <h2>模型用量</h2>
    <div id="models"></div>
  </section>
</main>
<script>
const fmt = new Intl.NumberFormat();
const setText = (id, value) => { document.getElementById(id).textContent = value; };
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
function collectModels(usage) {
  const rows = [];
  const apis = usage?.apis || {};
  for (const [api, apiStats] of Object.entries(apis)) {
    const models = apiStats?.models || {};
    for (const [model, modelStats] of Object.entries(models)) {
      rows.push({ api, model, requests: modelStats?.total_requests || 0, tokens: modelStats?.total_tokens || 0 });
    }
  }
  rows.sort((a, b) => b.requests - a.requests || b.tokens - a.tokens);
  return rows.slice(0, 50);
}
async function load() {
  try {
    const response = await fetch('./dashboard-data', { cache: 'no-store' });
    if (!response.ok) throw new Error('请求失败：' + response.status);
    const data = await response.json();
    const usage = data.usage || {};
    setText('totalRequests', fmt.format(usage.total_requests || 0));
    setText('successCount', fmt.format(usage.success_count || 0));
    setText('failureCount', fmt.format(usage.failure_count || 0));
    setText('totalTokens', fmt.format(usage.total_tokens || 0));
    setText('subtitle', '更新于 ' + new Date(data.generated_at || Date.now()).toLocaleString());
    const rows = collectModels(usage);
    document.getElementById('models').innerHTML = rows.length
      ? '<table><thead><tr><th>接口</th><th>模型</th><th>请求数</th><th>令牌数</th></tr></thead><tbody>' + rows.map((row) =>
          '<tr><td>' + esc(row.api) + '</td><td>' + esc(row.model) + '</td><td>' + fmt.format(row.requests) + '</td><td>' + fmt.format(row.tokens) + '</td></tr>'
        ).join('') + '</tbody></table>'
      : '<div class="empty">暂无用量记录。</div>';
    setText('status', '正常');
  } catch (error) {
    setText('status', '异常');
    setText('subtitle', error.message || '加载用量统计失败');
  }
}
load();
setInterval(load, 30000);
</script>
</body>
</html>`

func handleExportUsage() ([]byte, error) {
	snapshot := stats.Snapshot()

	exportPayload := map[string]interface{}{
		"version":     1,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"usage":       snapshot,
	}

	exportJSON, err := json.Marshal(exportPayload)
	if err != nil {
		return nil, err
	}

	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: exportJSON,
	}

	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func handleImportUsage(body []byte) ([]byte, error) {
	var importPayload struct {
		Version int                `json:"version"`
		Usage   StatisticsSnapshot `json:"usage"`
	}

	if err := json.Unmarshal(body, &importPayload); err != nil {
		return errorEnvelope("invalid_json", "failed to parse import payload"), nil
	}

	if importPayload.Version != 0 && importPayload.Version != 1 {
		return errorEnvelope("unsupported_version", "unsupported version"), nil
	}

	result := stats.MergeSnapshot(importPayload.Usage)
	snapshot := stats.Snapshot()

	responseData := map[string]interface{}{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	}

	responseJSON, err := json.Marshal(responseData)
	if err != nil {
		return nil, err
	}

	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: responseJSON,
	}

	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func mustMarshal(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}

// ============================================================================
// Data Structures
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

type UsageFailure struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
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

// ============================================================================
// Statistics Storage
// ============================================================================

type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

type RequestDetail struct {
	Timestamp time.Time  `json:"timestamp"`
	LatencyMs int64      `json:"latency_ms"`
	Source    string     `json:"source"`
	AuthIndex string     `json:"auth_index"`
	Tokens    TokenStats `json:"tokens"`
	Failed    bool       `json:"failed"`
}

type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
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
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

var stats = NewRequestStatistics()

func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

func (s *RequestStatistics) Record(record UsageRecord) {
	if s == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}

	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = record.Source
	}
	if statsKey == "" {
		statsKey = "unknown"
	}

	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if record.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	apiSt, ok := s.apis[statsKey]
	if !ok {
		apiSt = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = apiSt
	}

	s.updateAPIStats(apiSt, modelName, RequestDetail{
		Timestamp: timestamp,
		LatencyMs: record.Latency.Milliseconds(),
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens: TokenStats{
			InputTokens:     record.Detail.InputTokens,
			OutputTokens:    record.Detail.OutputTokens,
			ReasoningTokens: record.Detail.ReasoningTokens,
			CachedTokens:    record.Detail.CachedTokens,
			TotalTokens:     totalTokens,
		},
		Failed: record.Failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) {
	apiSt.TotalRequests++
	apiSt.TotalTokens += detail.Tokens.TotalTokens

	modelSt, ok := apiSt.Models[model]
	if !ok {
		modelSt = &modelStats{}
		apiSt.Models[model] = modelSt
	}
	modelSt.TotalRequests++
	modelSt.TotalTokens += detail.Tokens.TotalTokens
	modelSt.Details = append(modelSt.Details, detail)
}

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
			TotalTokens:   apiSt.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSt.Models)),
		}
		for modelName, modelSt := range apiSt.Models {
			details := make([]RequestDetail, len(modelSt.Details))
			copy(details, modelSt.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelSt.TotalRequests,
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

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := fmt.Sprintf("%02d", hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := fmt.Sprintf("%02d", hour)
		result.TokensByHour[key] = v
	}

	return result
}

func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
			if strings.TrimSpace(modelName) == "" {
				modelName = "unknown"
			}

			for _, detail := range modelSnapshot.Details {
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}

				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}

				s.recordImported(apiName, modelName, apiSt, detail)
				result.Added++
			}
		}
	}

	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, apiSt *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
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

	s.updateAPIStats(apiSt, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := detail.Tokens
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
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
		tokens.TotalTokens,
	)
}
