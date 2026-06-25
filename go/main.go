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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	abiVersion                uint32 = 1
	defaultMaxDetailsPerModel        = 5000
	defaultRetentionDays             = 30
	defaultDedupWindowMinutes        = 24 * 60
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
		return handleRegister(requestBody)
	case "plugin.reconfigure":
		return handleReconfigure(requestBody)
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

func handleRegister(requestBody []byte) ([]byte, error) {
	applyRuntimeConfig(requestBody)

	result := PluginRegisterResponse{
		SchemaVersion: 1,
		Metadata: PluginMetadata{
			Name:             "用量统计",
			Version:          "1.0.0",
			Author:           "本地维护",
			GitHubRepository: "https://github.com/zduu/cpa-usage-plugin",
			Logo:             "",
			ConfigFields: []ConfigField{
				{
					Name:        "max_details_per_model",
					Type:        "integer",
					Default:     defaultMaxDetailsPerModel,
					Description: "每个上游接口/模型最多保留的请求明细条数。",
				},
				{
					Name:        "retention_days",
					Type:        "integer",
					Default:     defaultRetentionDays,
					Description: "内存统计最多保留的天数，0 表示不按时间淘汰。",
				},
				{
					Name:        "dedup_window_minutes",
					Type:        "integer",
					Default:     defaultDedupWindowMinutes,
					Description: "usage 记录去重窗口分钟数，0 表示关闭去重。",
				},
				{
					Name:        "log_response_headers",
					Type:        "string",
					Default:     "",
					Description: "允许记录的响应头名称列表（逗号分隔），支持 * 通配符。留空不记录任何响应头。",
				},
			},
		},
		Capabilities: PluginCapabilities{
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	return okEnvelopeJSON(string(resultJSON))
}

func handleReconfigure(requestBody []byte) ([]byte, error) {
	return handleRegister(requestBody)
}

type runtimeConfig struct {
	MaxDetailsPerModel int
	RetentionDays      int
	DedupWindowMinutes int
	LogResponseHeaders string // comma-separated header name patterns ("*" wildcard)
}

func defaultRuntimeConfig() runtimeConfig {
	return runtimeConfig{
		MaxDetailsPerModel: defaultMaxDetailsPerModel,
		RetentionDays:      defaultRetentionDays,
		DedupWindowMinutes: defaultDedupWindowMinutes,
		LogResponseHeaders: "",
	}
}

func applyRuntimeConfig(requestBody []byte) {
	stats.Configure(parseRuntimeConfig(requestBody))
}

func parseRuntimeConfig(requestBody []byte) runtimeConfig {
	cfg := defaultRuntimeConfig()
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(requestBody) == 0 || json.Unmarshal(requestBody, &req) != nil || len(req.ConfigYAML) == 0 {
		return cfg
	}
	yamlText := string(req.ConfigYAML)
	cfg.MaxDetailsPerModel = yamlInt(yamlText, "max_details_per_model", cfg.MaxDetailsPerModel)
	cfg.RetentionDays = yamlInt(yamlText, "retention_days", cfg.RetentionDays)
	cfg.DedupWindowMinutes = yamlInt(yamlText, "dedup_window_minutes", cfg.DedupWindowMinutes)
	if s := yamlString(yamlText, "log_response_headers"); s != "" {
		cfg.LogResponseHeaders = s
	}
	return cfg
}

func yamlInt(yamlText, key string, fallback int) int {
	for _, line := range strings.Split(yamlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Match "key:" anywhere in the line to support nested YAML.
		prefix := key + ":"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		// Ensure the key token starts at the beginning or after whitespace.
		if idx > 0 && line[idx-1] != ' ' && line[idx-1] != '\t' {
			continue
		}
		value := strings.TrimSpace(line[idx+len(prefix):])
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "" {
			return fallback
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return fallback
		}
		return parsed
	}
	return fallback
}

func yamlString(yamlText, key string) string {
	for _, line := range strings.Split(yamlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix := key + ":"
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		if idx > 0 && line[idx-1] != ' ' && line[idx-1] != '\t' {
			continue
		}
		value := strings.TrimSpace(line[idx+len(prefix):])
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		return strings.TrimSpace(value)
	}
	return ""
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
	result := ManagementRegisterResponse{
		Routes: []ManagementRoute{
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/usage",
				Description: "获取用量统计数据。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/usage/export",
				Description: "导出用量统计数据。",
			},
			{
				Method:      "POST",
				Path:        "/plugins/usage-statistics/usage/import",
				Description: "导入用量统计数据。",
			},
		},
		Resources: []ManagementResource{
			{
				Path:        "/dashboard",
				Menu:        "用量统计",
				Description: "请求、token 和模型用量统计。",
			},
			{
				Path:        "/dashboard-data",
				Description: "用量统计看板数据。",
			},
			{
				Path:        "/usage/export",
				Description: "用量统计导出数据。",
			},
			{
				Path:        "/usage/import",
				Description: "用量统计导入数据。",
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
:root{color-scheme:light dark;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Arial,sans-serif;background:#f6f7f9;color:#262626}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;background:#f6f7f9;color:#262626}
button,input,select{font:inherit}
button{cursor:pointer}
.shell{width:min(100%,1640px);margin:0 auto;padding:20px}
.header{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:16px;flex-wrap:wrap;padding-right:200px}
h1{margin:0;font-size:22px;line-height:1.2;font-weight:700;letter-spacing:0}
.toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
.toolbar label{font-size:12px;color:#7a756f;font-weight:700}
.select,.input{height:36px;border:1px solid #dedede;border-radius:8px;background:#fff;color:#262626;padding:0 10px;min-width:0;font-size:13px}
.btn{height:36px;border:1px solid #d9d9d9;border-radius:8px;background:#fff;color:#333;padding:0 12px;font-weight:700;font-size:13px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
.btn:hover{border-color:#b8b8b8}
.btn.primary{background:#262626;color:#fff;border-color:#262626}
.btn.danger{color:#b42318;border-color:#f1c5bf}
.updated{color:#9a948d;font-size:12px;white-space:nowrap}
.cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin-bottom:14px}
.stat{background:#fff;border:1px solid #dedede;border-radius:12px;padding:16px;min-height:130px;box-shadow:0 10px 30px rgba(0,0,0,.04);position:relative;overflow:hidden}
.stat:before{content:"";position:absolute;left:0;right:0;top:0;height:3px;background:#8b8680}
.stat.green:before{background:#22c55e}.stat.purple:before{background:#8b5cf6}.stat.orange:before{background:#f97316}.stat.amber:before{background:#f59e0b}
.label{color:#9a948d;font-size:12px;font-weight:700;margin-bottom:12px}
.value{font-size:26px;line-height:1;font-weight:800;letter-spacing:0;font-variant-numeric:tabular-nums}
.meta{margin-top:10px;color:#6f6963;font-size:12px;display:flex;gap:10px;flex-wrap:wrap}
.ok{color:#10b981}.bad{color:#c65746}.neutral{color:#7a756f}
.spark{width:100%;height:44px;margin-top:14px;border:1px solid #e2e2e2;border-radius:8px;background:#f8f8f8}
.layout{display:grid;grid-template-columns:1.05fr .95fr;gap:14px}
.full{grid-column:1/-1}
.panel{background:#fff;border:1px solid #dedede;border-radius:12px;padding:16px;box-shadow:0 10px 30px rgba(0,0,0,.035);min-width:0}
.panel h2{margin:0;font-size:16px;line-height:1.2}
.panelHead{display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:12px;flex-wrap:wrap}
.subtle{color:#8b8680;font-size:12px}
.tableWrap{overflow:auto;max-height:520px}
table{width:100%;border-collapse:collapse;font-size:12px}
th,td{text-align:left;padding:10px 10px;border-bottom:1px solid #e5e5e5;vertical-align:middle;white-space:nowrap}
th{color:#9a948d;font-size:11px;font-weight:800;background:#fff;position:sticky;top:0;z-index:1}
tr:last-child td{border-bottom:0}
.nameCell{font-weight:700;color:#2f2f2f;white-space:normal;min-width:160px}
.pill{display:inline-flex;align-items:center;border:1px solid #dedede;background:#f7f7f7;border-radius:999px;padding:2px 7px;font-size:11px;font-weight:700;color:#6f6963;margin-left:6px}
.empty{padding:24px;text-align:center;color:#8b8680;background:#f8f8f8;border-radius:8px;font-size:13px}
.clickableRow{cursor:pointer}
.clickableRow:hover td{background:#f8f8f8}
.selectedRow td{background:#f1f5f9}
.detailGrid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:10px;margin-bottom:14px}
.metric{border:1px solid #e5e5e5;border-radius:8px;padding:10px;background:#fafafa;min-height:74px}
.metricLabel{font-size:11px;color:#8b8680;font-weight:800;margin-bottom:8px}
.metricValue{font-size:20px;font-weight:800;font-variant-numeric:tabular-nums;line-height:1.1}
.splitGrid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-bottom:14px}
.barList{display:grid;gap:8px}
.barItem{display:grid;grid-template-columns:minmax(120px,1fr) minmax(160px,2fr) auto;gap:10px;align-items:center;font-size:12px}
.barTrack{height:8px;background:#eeeeee;border-radius:999px;overflow:hidden}
.barFill{height:100%;background:#8b8680;border-radius:999px}
.errorText{max-width:360px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.apiCardGrid{display:grid;gap:12px}
.apiCard{border:1px solid #dedede;border-radius:8px;padding:16px;background:#fff;display:grid;grid-template-columns:1fr auto;gap:12px;align-items:center}
.apiName{font-size:18px;font-weight:800;color:#2f2f2f;margin-bottom:10px}
.apiChips{display:flex;gap:8px;flex-wrap:wrap}
.chip{display:inline-flex;align-items:center;border:1px solid #e0e0e0;background:#fafafa;border-radius:999px;padding:6px 12px;color:#6f6963;font-size:13px}
.apiArrow{font-size:22px;color:#6f6963}
.segmented{display:flex;gap:8px;flex-wrap:wrap}
.segmented .btn.active{border-color:#8b8680;background:#f7f7f7;color:#262626}
.priceGrid{display:grid;grid-template-columns:2fr repeat(3,1fr) auto;gap:8px;align-items:end}
.priceList{display:grid;gap:6px;margin-top:12px}
.priceItem{display:flex;justify-content:space-between;gap:10px;align-items:center;border:1px solid #e5e5e5;border-radius:8px;padding:8px 10px}
.priceMeta{display:flex;gap:10px;color:#6f6963;font-size:12px;flex-wrap:wrap}
.healthSummary{display:flex;gap:14px;align-items:baseline;flex-wrap:wrap}
.healthRate{font-size:18px;font-weight:800}
.healthScroller{overflow-x:auto;overflow-y:hidden;padding-bottom:8px;scrollbar-width:thin;scrollbar-color:#c0c0c0 transparent}
.healthScroller::-webkit-scrollbar{height:6px}
.healthScroller::-webkit-scrollbar-track{background:transparent}
.healthScroller::-webkit-scrollbar-thumb{background:#c0c0c0;border-radius:3px}
.healthGrid{display:grid;grid-template-columns:repeat(96,12px);grid-template-rows:repeat(7,12px);gap:4px;min-width:max-content}
.healthCell{width:12px;height:12px;border-radius:2px;background:#ececec;border:1px solid rgba(0,0,0,.04);cursor:pointer}
.legend{display:flex;gap:6px;align-items:center;color:#8b8680;font-size:11px;margin-top:8px;flex-wrap:wrap}
.legendDot{width:12px;height:12px;border-radius:2px;display:inline-block}
.filters{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:10px}
.eventsMeta{display:flex;justify-content:space-between;color:#8b8680;font-size:12px;margin-bottom:6px;gap:8px;flex-wrap:wrap}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono",monospace}
.nowrap{white-space:nowrap}
.tooltip{position:fixed;z-index:20;background:#262626;color:#fff;border-radius:8px;padding:8px 10px;font-size:11px;pointer-events:none;box-shadow:0 12px 28px rgba(0,0,0,.22);max-width:260px;line-height:1.6;white-space:nowrap}
.tooltip .ok{color:#10b981}.tooltip .bad{color:#c65746}
.hidden{display:none!important}
@media(max-width:1120px){.cards{grid-template-columns:repeat(2,minmax(0,1fr))}.layout{grid-template-columns:1fr}.detailGrid{grid-template-columns:repeat(3,minmax(0,1fr))}.splitGrid{grid-template-columns:1fr}}
@media(max-width:640px){.shell{padding:14px}h1{font-size:20px}.cards{grid-template-columns:1fr}.value{font-size:24px}.priceGrid{grid-template-columns:1fr}.detailGrid{grid-template-columns:1fr}.barItem{grid-template-columns:1fr}.apiCard{grid-template-columns:1fr}.btn,.select,.input{width:100%}.toolbar{width:100%}.toolbar>*{flex:1 1 150px}}
@media(prefers-color-scheme:dark){:root{background:#111315;color:#f2f2f2}body{background:#111315;color:#f2f2f2}.panel,.stat,th,.btn,.select,.input,.apiCard{background:#181b1f;border-color:#30343a;color:#f2f2f2}.spark,.empty,.metric,.chip{background:#121417;border-color:#30343a}.clickableRow:hover td,.selectedRow td{background:#20242a}.barTrack{background:#2a2f35}.label,th,.subtle,.updated,.neutral,.meta,.legend,.metricLabel,.chip{color:#a8a29b}td{border-color:#30343a}.nameCell,.apiName{color:#f2f2f2}.pill{background:#20242a;border-color:#3a3f46;color:#c9c3bc}.healthCell{background:#2a2f35}.tooltip{background:#f2f2f2;color:#1b1b1b}}
</style>
</head>
<body>
<main class="shell">
  <div class="header">
    <h1>使用统计</h1>
    <div class="toolbar">
      <label for="range">时间范围</label>
      <select id="range" class="select">
        <option value="7h">最近7小时</option>
        <option value="24h" selected>最近24小时</option>
        <option value="7d">最近7天</option>
        <option value="all">全部</option>
      </select>
      <button id="exportBtn" class="btn">导出数据</button>
      <button id="importBtn" class="btn">导入数据</button>
      <button id="refreshBtn" class="btn primary">刷新</button>
      <input id="importFile" type="file" accept="application/json,.json" class="hidden">
      <span class="updated" id="updated">正在加载...</span>
    </div>
  </div>
  <section class="cards">
    <div class="stat"><div class="label">总请求数</div><div class="value" id="totalRequests">-</div><div class="meta"><span class="ok" id="successText">成功请求：-</span><span class="bad" id="failureText">失败请求：-</span><span id="avgLatency">平均延迟：-</span></div><svg id="requestSpark" class="spark"></svg></div>
    <div class="stat purple"><div class="label">总 token 数</div><div class="value" id="totalTokens">-</div><div class="meta"><span id="cachedText">缓存 token：-</span><span id="reasoningText">思考 token：-</span></div><svg id="tokenSpark" class="spark"></svg></div>
    <div class="stat green"><div class="label">每分钟请求</div><div class="value" id="rpm">-</div><div class="meta"><span id="rpmMeta">最近30分钟请求：-</span></div><svg id="rpmSpark" class="spark"></svg></div>
    <div class="stat amber"><div class="label">总花费</div><div class="value" id="totalCost">-</div><div class="meta"><span id="costMeta">按本页模型价格估算</span></div><svg id="costSpark" class="spark"></svg></div>
  </section>
  <section class="panel full">
    <div class="panelHead">
      <div><h2>服务健康监测</h2><div class="subtle">最近7天，15分钟一个网格；绿色代表成功率高，红色代表失败较多。</div></div>
      <div class="healthSummary"><span>成功率</span><span class="healthRate" id="healthRate">-</span><span class="ok" id="healthSuccess">成功 -</span><span class="bad" id="healthFailure">失败 -</span></div>
    </div>
    <div class="healthScroller"><div class="healthGrid" id="healthGrid"></div></div>
    <div class="legend"><span>少</span><span class="legendDot" style="background:#ef4444"></span><span class="legendDot" style="background:#facc15"></span><span class="legendDot" style="background:#22c55e"></span><span>多</span><span>灰色为无请求</span></div>
  </section>
  <section class="layout">
    <div class="panel">
      <div class="panelHead"><h2>模型价格设置</h2><span class="subtle">单位：美元 / 百万 token，保存在当前浏览器</span></div>
      <div class="priceGrid">
        <div><label class="subtle">模型</label><select id="priceModel" class="select"></select></div>
        <div><label class="subtle">输入价格</label><input id="pricePrompt" class="input" type="number" min="0" step="0.0001" placeholder="0.0000"></div>
        <div><label class="subtle">输出价格</label><input id="priceCompletion" class="input" type="number" min="0" step="0.0001" placeholder="0.0000"></div>
        <div><label class="subtle">缓存价格</label><input id="priceCache" class="input" type="number" min="0" step="0.0001" placeholder="默认同输入"></div>
        <button id="savePrice" class="btn primary">保存</button>
      </div>
      <div class="priceList" id="priceList"></div>
    </div>
    <div class="panel">
      <div class="panelHead"><h2>来源统计</h2><span class="subtle">按上游来源聚合成功率</span></div>
      <div class="tableWrap" id="credentialStats"></div>
    </div>
    <div class="panel full">
      <div class="panelHead">
        <div><h2>API 详细统计</h2><div class="subtle">按调用 CPA 服务的 API key 聚合。</div></div>
        <div class="segmented">
          <button class="btn active" data-api-sort="requests">请求次数</button>
          <button class="btn" data-api-sort="tokens">Token数量</button>
          <button class="btn" data-api-sort="cost">总花费</button>
        </div>
      </div>
      <div id="clientApiStats"></div>
    </div>
    <div class="panel">
      <div class="panelHead"><h2>上游接口统计</h2><span class="subtle">按上游提供商和来源聚合</span></div>
      <div class="tableWrap" id="apiStats"></div>
    </div>
    <div class="panel full">
      <div class="panelHead">
        <div><h2>上游接口详情</h2><div class="subtle" id="apiDetailTitle">选择一个上游接口查看模型、来源、错误和最近请求。</div></div>
        <div class="toolbar"><label for="apiSelect">上游接口</label><select id="apiSelect" class="select"></select><button id="exportApiCsv" class="btn">导出当前接口表格</button><button id="exportApiJson" class="btn">导出当前接口明细</button></div>
      </div>
      <div id="apiDetail"></div>
    </div>
    <div class="panel">
      <div class="panelHead"><h2>模型统计</h2><span class="subtle">请求数、token、平均延迟、成功率和估算花费</span></div>
      <div class="tableWrap" id="modelStats"></div>
    </div>
    <div class="panel full">
      <div class="panelHead">
        <div><h2>请求事件明细</h2><div class="subtle">最多显示最近500条，可按模型、来源、凭证筛选并导出。</div></div>
        <div class="toolbar"><button id="clearFilters" class="btn">清除筛选</button><button id="exportRowsCsv" class="btn">导出表格</button><button id="exportRowsJson" class="btn">导出明细</button></div>
      </div>
      <div class="filters">
        <select id="filterModel" class="select"></select>
        <select id="filterSource" class="select"></select>
        <select id="filterAuth" class="select"></select>
      </div>
      <div class="eventsMeta"><span id="eventsCount">-</span><span>延迟单位：毫秒</span></div>
      <div class="tableWrap" id="events"></div>
    </div>
  </section>
</main>
<div id="tooltip" class="tooltip hidden"></div>
<script>
const storeKey='cpa-usage-model-prices-v1';
const rangeKey='cpa-usage-range-v1';
const fmt=new Intl.NumberFormat('zh-CN');
const money=new Intl.NumberFormat('zh-CN',{style:'currency',currency:'USD',maximumFractionDigits:2});
let rawUsage=null, usage=null, details=[], modelPrices=loadPrices();
const $=(id)=>document.getElementById(id);
const setText=(id,value)=>{$(id).textContent=value};
const esc=(value)=>String(value??'').replace(/[&<>"']/g,(ch)=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
const num=(value)=>Number.isFinite(Number(value))?Number(value):0;
function compact(value){const n=num(value),abs=Math.abs(n);const trim=(v)=>v.toFixed(1).replace(/\.0$/,'');if(abs>=1e6)return trim(n/1e6)+'M';if(abs>=1e3)return trim(n/1e3)+'k';return fmt.format(n)}
const pct=(value)=>Number.isFinite(value)?value.toFixed(1)+'%':'-';
const formatMs=(value)=>Number.isFinite(value)&&value>0?(value>=1000?(value/1000).toFixed(2)+'秒':Math.round(value)+'毫秒'):'-';
let selectedApi='';
let clientApiSort='requests';
let pollTimer=null, pollFailures=0;
function loadPrices(){try{return JSON.parse(localStorage.getItem(storeKey)||'{}')||{}}catch{return {}}}
function savePrices(){localStorage.setItem(storeKey,JSON.stringify(modelPrices))}
function timestampMs(value){const ms=Date.parse(value);return Number.isFinite(ms)?ms:0}
function totalTokens(detail){const t=detail.tokens||{};return num(t.total_tokens)||num(t.input_tokens)+num(t.output_tokens)+num(t.reasoning_tokens)+Math.max(num(t.cached_tokens),num(t.cache_tokens))}
function detailCost(detail){const p=modelPrices[detail.model];if(!p)return 0;const t=detail.tokens||{};const cached=Math.max(num(t.cached_tokens),num(t.cache_tokens));const input=Math.max(num(t.input_tokens)-cached,0);const output=Math.max(num(t.output_tokens),0);return input/1e6*num(p.prompt)+output/1e6*num(p.completion)+cached/1e6*num(p.cache)}
function looksLikeKey(v){return typeof v==='string'&&(v.startsWith('sk-')||v.startsWith('AIza')||v.startsWith('hf_')||v.startsWith('pk_')||v.startsWith('rk_')||v.length>=80)}
function looksLikeCredentialId(v){const s=String(v||'').trim();return /^[a-f0-9]{8,}$/i.test(s)||/^[0-9a-f]{12,}$/i.test(s)||s.length>=32}
function isCredentialMarker(v){return /^(api[-_ ]?key|apikey|key|credential|auth)$/i.test(String(v||'').trim())}
function trimCredentialSuffix(value){
  let s=String(value??'').trim();if(!s)return '';
  const dot=s.split(' · ').map((p)=>p.trim()).filter(Boolean);
  const marker=dot.findIndex(isCredentialMarker);
  if(marker>0)return dot.slice(0,marker).join(' · ');
  if(dot.length>1&&looksLikeCredentialId(dot[dot.length-1]))return dot.slice(0,-1).join(' · ');
  const colon=s.split(':').map((p)=>p.trim()).filter(Boolean);
  if(colon.length>=3&&looksLikeCredentialId(colon[colon.length-1]))return colon.slice(0,-1).join(':');
  return s;
}
function sourceLabel(detail){const s=trimCredentialSuffix(detail.source);if(s&&!looksLikeKey(s))return s;const p=trimCredentialSuffix(detail.provider);if(p&&!looksLikeKey(p))return p;return '未知来源'}
function sourceKey(detail){return sourceLabel(detail)}
function friendlyApiName(apiName){const clean=trimCredentialSuffix(apiName);if(!clean)return'未知接口';const parts=clean.split(' · ').filter(function(p){return !looksLikeKey(p)&&!isCredentialMarker(p)&&!looksLikeCredentialId(p)});return parts.length?parts.join(' · '):clean}
function clientApiLabel(detail){return detail.api_key||'未知 API'}
function avg(values){const xs=values.map(num).filter((v)=>v>0);return xs.length?xs.reduce((a,b)=>a+b,0)/xs.length:0}
function collectDetails(data){
  const rows=[];const apis=data?.apis||{};
  Object.entries(apis).forEach(([api,apiData])=>{
    Object.entries(apiData?.models||{}).forEach(([model,modelData])=>{
      (modelData?.details||[]).forEach((d,index)=>{
        const tokens=d.tokens||{};
        rows.push({...d,api,model,index,timestamp_ms:timestampMs(d.timestamp),total_tokens:totalTokens(d),cached_tokens:Math.max(num(tokens.cached_tokens),num(tokens.cache_tokens)),reasoning_tokens:num(tokens.reasoning_tokens),cost:0});
      });
    });
  });
  rows.forEach((row)=>{row.cost=detailCost(row)});
  return rows.sort((a,b)=>b.timestamp_ms-a.timestamp_ms);
}
function filteredUsage(data,range){
  if(!data||range==='all')return data;
  const ms={ '7h':7*3600e3, '24h':24*3600e3, '7d':7*24*3600e3 }[range]||24*3600e3;
  const start=Date.now()-ms;
  const copy={...data,total_requests:0,success_count:0,failure_count:0,total_tokens:0,apis:{}};
  Object.entries(data.apis||{}).forEach(([api,apiData])=>{
    const apiCopy={...apiData,total_requests:0,success_count:0,failure_count:0,total_tokens:0,models:{}};
    Object.entries(apiData.models||{}).forEach(([model,modelData])=>{
      const ds=(modelData.details||[]).filter((d)=>timestampMs(d.timestamp)>=start&&timestampMs(d.timestamp)<=Date.now());
      if(!ds.length)return;
      const success=ds.filter((d)=>!d.failed).length;
      const failure=ds.length-success;
      const tokens=ds.reduce((sum,d)=>sum+totalTokens(d),0);
      apiCopy.models[model]={...modelData,details:ds,total_requests:ds.length,success_count:success,failure_count:failure,total_tokens:tokens};
      apiCopy.total_requests+=ds.length;apiCopy.success_count+=success;apiCopy.failure_count+=failure;apiCopy.total_tokens+=tokens;
    });
    if(apiCopy.total_requests>0){copy.apis[api]=apiCopy;copy.total_requests+=apiCopy.total_requests;copy.success_count+=apiCopy.success_count;copy.failure_count+=apiCopy.failure_count;copy.total_tokens+=apiCopy.total_tokens}
  });
  return copy;
}
function bucketSeries(rows,metric,minutes,count){
  const now=Date.now();const step=minutes*60e3;const start=now-step*count;const arr=new Array(count).fill(0);
  rows.forEach((d)=>{const idx=Math.floor((d.timestamp_ms-start)/step);if(idx>=0&&idx<count)arr[idx]+=metric==='tokens'?d.total_tokens:metric==='cost'?d.cost:1});
  return arr;
}
function drawSpark(id,values,color){
  const svg=$(id); const w=svg.clientWidth||320, h=54; const max=Math.max(...values,1); const points=values.map((v,i)=>[i*(w/(Math.max(values.length-1,1))),h-8-(v/max)*(h-16)]);
  const d=points.map((p,i)=>(i?'L':'M')+p[0].toFixed(1)+' '+p[1].toFixed(1)).join(' ');
  svg.setAttribute('viewBox','0 0 '+w+' '+h);
  svg.innerHTML='<path d="'+d+'" fill="none" stroke="'+color+'" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>';
}
function renderStats(){
  const success=num(usage?.success_count), failure=num(usage?.failure_count), total=num(usage?.total_requests);
  const cached=details.reduce((s,d)=>s+d.cached_tokens,0), reasoning=details.reduce((s,d)=>s+d.reasoning_tokens,0), cost=details.reduce((s,d)=>s+d.cost,0);
  const latencies=details.map((d)=>num(d.latency_ms)).filter((v)=>v>0);
  const avg=latencies.length?latencies.reduce((a,b)=>a+b,0)/latencies.length:0;
  const recent=details.filter((d)=>d.timestamp_ms>=Date.now()-30*60e3);
  setText('totalRequests',fmt.format(total));setText('successText','成功请求：'+fmt.format(success));setText('failureText','失败请求：'+fmt.format(failure));setText('avgLatency','平均延迟：'+formatMs(avg));
  setText('totalTokens',compact(num(usage?.total_tokens)));setText('cachedText','缓存 token：'+compact(cached));setText('reasoningText','思考 token：'+compact(reasoning));
  setText('rpm',(recent.length/30).toFixed(2));setText('rpmMeta','最近30分钟请求：'+fmt.format(recent.length));
  setText('totalCost',money.format(cost));setText('costMeta','总 token 数：'+compact(num(usage?.total_tokens)));
  drawSpark('requestSpark',bucketSeries(details,'requests',60,48),'#8b8680');drawSpark('tokenSpark',bucketSeries(details,'tokens',60,48),'#8b5cf6');drawSpark('rpmSpark',bucketSeries(details,'requests',5,48),'#22c55e');drawSpark('costSpark',bucketSeries(details,'cost',60,48),'#f59e0b');
}
function healthColor(rate){if(rate<0)return ''; const stops=[[239,68,68],[250,204,21],[34,197,94]]; const seg=rate<.5?0:1; const t=seg===0?rate*2:(rate-.5)*2; const a=stops[seg],b=stops[seg+1]; return 'rgb('+a.map((v,i)=>Math.round(v+(b[i]-v)*t)).join(',')+')'}
function healthCellStyle(i,count,total,rate){const rows=7,cols=Math.ceil(count/rows),age=count-1-i,col=cols-Math.floor(age/rows),row=rows-(age%rows);return 'grid-column:'+col+';grid-row:'+row+';'+(total?'background:'+healthColor(rate):'')}
function renderHealth(){
  const count=672, step=15*60e3, now=Date.now(), start=now-count*step; const stats=Array.from({length:count},()=>({s:0,f:0}));
  details.forEach((d)=>{if(d.timestamp_ms<start||d.timestamp_ms>now)return; const idx=count-1-Math.floor((now-d.timestamp_ms)/step); if(idx>=0&&idx<count){d.failed?stats[idx].f++:stats[idx].s++}});
  let totalS=0,totalF=0; const cells=[]; const tooltips=[];
  stats.forEach((x,i)=>{
    totalS+=x.s;totalF+=x.f;const total=x.s+x.f;const rate=total?x.s/total:-1;
    const t0=new Date(start+i*step),t1=new Date(start+(i+1)*step);
    const timeRange=t0.toLocaleString()+' - '+t1.toLocaleString();
    const tip='<span>'+timeRange+'</span><br>'+(total?'<span class="ok">成功 '+x.s+'</span> <span class="bad">失败 '+x.f+'</span> <span>成功率 '+pct(rate*100)+'</span>':'<span>无请求</span>');
    tooltips.push(tip);
    cells.push('<div class="healthCell '+(total?'active':'')+'" data-health-idx="'+i+'" style="'+healthCellStyle(i,count,total,rate)+'"></div>');
  });
  $('healthGrid').innerHTML=cells.join('');
  const tip=$('tooltip');
  const showTip=function(cell){
    if(!cell)return;
    const idx=parseInt(cell.dataset.healthIdx);if(isNaN(idx)||idx<0||idx>=count){tip.classList.add('hidden');return}
    tip.innerHTML=tooltips[idx];tip.classList.remove('hidden');
    const r=cell.getBoundingClientRect();let left=r.right+8,top=r.top-6;
    if(left+260>window.innerWidth)left=r.left-268;if(top+64>window.innerHeight)top=window.innerHeight-74;if(top<6)top=6;
    tip.style.left=left+'px';tip.style.top=top+'px';
  };
  $('healthGrid').onmouseover=function(e){
    const cell=e.target.closest('.healthCell');
    if(!cell){tip.classList.add('hidden');return}
    showTip(cell);
  };
  $('healthGrid').onmouseleave=function(e){
    if(!e.relatedTarget||!e.relatedTarget.closest('.healthCell'))tip.classList.add('hidden');
  };
  $('healthGrid').onmouseout=function(e){const t=e.relatedTarget;if(!t||!t.closest('.healthCell'))tip.classList.add('hidden')};
  const total=totalS+totalF; setText('healthRate',total?pct(totalS/total*100):'-'); setText('healthSuccess','成功 '+fmt.format(totalS)); setText('healthFailure','失败 '+fmt.format(totalF));
}
function modelNames(){return [...new Set(details.map((d)=>d.model).filter(Boolean))].sort((a,b)=>a.localeCompare(b))}
function renderPrices(){
  const selected=$('priceModel').value; $('priceModel').innerHTML='<option value="">选择模型</option>'+modelNames().map((m)=>'<option value="'+esc(m)+'">'+esc(m)+'</option>').join(''); $('priceModel').value=selected;
  const entries=Object.entries(modelPrices);
  $('priceList').innerHTML=entries.length?entries.map(([m,p])=>'<div class="priceItem"><div><strong>'+esc(m)+'</strong><div class="priceMeta"><span>输入 '+num(p.prompt).toFixed(4)+'</span><span>输出 '+num(p.completion).toFixed(4)+'</span><span>缓存 '+num(p.cache).toFixed(4)+'</span></div></div><button class="btn danger" data-del-price="'+esc(m)+'">删除</button></div>').join(''):'<div class="empty">暂无价格设置，设置后会显示估算花费。</div>';
  document.querySelectorAll('[data-del-price]').forEach((btn)=>btn.onclick=()=>{delete modelPrices[btn.dataset.delPrice];savePrices();rerender()});
}
function renderCredentials(){
  const map=new Map(); details.forEach((d)=>{const key=sourceKey(d); const row=map.get(key)||{name:sourceLabel(d),type:trimCredentialSuffix(d.provider||''),success:0,failure:0,total:0}; d.failed?row.failure++:row.success++; row.total=row.success+row.failure; map.set(key,row)});
  const rows=[...map.values()].sort((a,b)=>b.total-a.total);
  $('credentialStats').innerHTML=rows.length?'<table><thead><tr><th>来源</th><th>请求次数</th><th>成功率</th></tr></thead><tbody>'+rows.map((r)=>{const rate=r.total?r.success/r.total*100:100;return '<tr><td class="nameCell">'+esc(r.name)+(r.type&&r.type!==r.name?'<span class="pill">'+esc(r.type)+'</span>':'')+'</td><td>'+fmt.format(r.total)+' <span class="ok">('+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+')</span></td><td class="'+(rate>=95?'ok':rate>=80?'neutral':'bad')+'">'+pct(rate)+'</td></tr>'}).join('')+'</tbody></table>':'<div class="empty">暂无来源数据</div>';
}
function renderClientApiStats(){
  const rows=groupedRows(details,clientApiLabel,clientApiLabel).sort((a,b)=>clientApiSort==='tokens'?b.tokens-a.tokens:clientApiSort==='cost'?b.cost-a.cost:b.requests-a.requests);
  document.querySelectorAll('[data-api-sort]').forEach((btn)=>btn.classList.toggle('active',btn.dataset.apiSort===clientApiSort));
  $('clientApiStats').innerHTML=rows.length?'<div class="apiCardGrid">'+rows.map((r)=>'<div class="apiCard"><div><div class="apiName">'+esc(r.name)+'</div><div class="apiChips"><span class="chip">请求次数: '+fmt.format(r.requests)+'（<span class="ok">'+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+'</span>）</span><span class="chip">Token数量: '+compact(r.tokens)+'</span><span class="chip">总花费: '+money.format(r.cost)+'</span></div></div><div class="apiArrow">▶</div></div>').join('')+'</div>':'<div class="empty">暂无 API key 请求数据</div>';
}
function renderApiStats(){
  const rows=Object.entries(usage?.apis||{}).map(([api,a])=>({api,requests:num(a.total_requests),success:num(a.success_count),failure:num(a.failure_count),tokens:num(a.total_tokens),models:a.models||{},details:collectDetails({apis:{[api]:a}})})).sort((a,b)=>b.requests-a.requests);
  rows.forEach((r)=>{r.cost=r.details.reduce((s,d)=>s+d.cost,0);r.avgLatency=avg(r.details.map((d)=>d.latency_ms));r.successRate=r.requests?r.success/r.requests*100:100});
  if(rows.length&&(!selectedApi||!rows.some((r)=>r.api===selectedApi)))selectedApi=rows[0].api;
  if(!rows.length)selectedApi='';
  $('apiSelect').innerHTML=rows.length?rows.map((r)=>'<option value="'+esc(r.api)+'">'+esc(friendlyApiName(r.api))+'</option>').join(''):'<option value="">暂无上游接口</option>';
  $('apiSelect').value=selectedApi;
  $('apiSelect').disabled=!rows.length;
  $('apiSelect').onchange=()=>{selectedApi=$('apiSelect').value;renderApiStats();renderApiDetail()};
  $('apiStats').innerHTML=rows.length?'<table><thead><tr><th>接口</th><th>请求</th><th>成功率</th><th>token</th><th>平均延迟</th><th>花费</th><th>模型</th></tr></thead><tbody>'+rows.map((r)=>'<tr class="clickableRow '+(r.api===selectedApi?'selectedRow':'')+'" data-api="'+esc(r.api)+'"><td class="nameCell">'+esc(friendlyApiName(r.api))+'</td><td>'+fmt.format(r.requests)+' <span class="ok">('+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+')</span></td><td class="'+(r.successRate>=95?'ok':r.successRate>=80?'neutral':'bad')+'">'+pct(r.successRate)+'</td><td>'+compact(r.tokens)+'</td><td>'+formatMs(r.avgLatency)+'</td><td>'+money.format(r.cost)+'</td><td>'+Object.keys(r.models).slice(0,4).map(esc).join('、')+'</td></tr>').join('')+'</tbody></table>':'<div class="empty">暂无接口数据</div>';
  document.querySelectorAll('[data-api]').forEach((row)=>row.onclick=()=>{selectedApi=row.getAttribute('data-api')||'';renderApiStats();renderApiDetail()});
}
function groupedRows(rows,keyFn,nameFn){
  const map=new Map();
  rows.forEach((d)=>{const key=keyFn(d);const r=map.get(key)||{name:nameFn(d),requests:0,success:0,failure:0,tokens:0,cached:0,reasoning:0,cost:0,latency:[],ttft:[]};r.requests++;d.failed?r.failure++:r.success++;r.tokens+=d.total_tokens;r.cached+=d.cached_tokens;r.reasoning+=d.reasoning_tokens;r.cost+=d.cost;if(num(d.latency_ms)>0)r.latency.push(num(d.latency_ms));if(num(d.ttft_ms)>0)r.ttft.push(num(d.ttft_ms));map.set(key,r)});
  return [...map.values()].sort((a,b)=>b.requests-a.requests);
}
function bars(title,rows,total,emptyText){
  if(!rows.length)return '<div><div class="subtle" style="margin-bottom:8px">'+esc(title)+'</div><div class="empty">'+esc(emptyText)+'</div></div>';
  return '<div><div class="subtle" style="margin-bottom:8px">'+esc(title)+'</div><div class="barList">'+rows.slice(0,8).map((r)=>{const width=total?Math.max(4,Math.round(r.requests/total*100)):0;return '<div class="barItem"><div class="nameCell">'+esc(r.name)+'</div><div class="barTrack"><div class="barFill" style="width:'+width+'%"></div></div><div>'+fmt.format(r.requests)+' 次</div></div>'}).join('')+'</div></div>';
}
function metric(label,value,extra){return '<div class="metric"><div class="metricLabel">'+esc(label)+'</div><div class="metricValue">'+value+'</div>'+(extra?'<div class="subtle" style="margin-top:6px">'+extra+'</div>':'')+'</div>'}
function currentApiDetails(){
  if(!selectedApi||!usage?.apis?.[selectedApi])return [];
  return collectDetails({apis:{[selectedApi]:usage.apis[selectedApi]}});
}
function renderApiDetail(){
  const api=selectedApi, apiData=api&&usage?.apis?.[api], rows=currentApiDetails();
  if(!apiData||!rows.length){setText('apiDetailTitle','选择一个上游接口查看模型、来源、错误和最近请求。');$('apiDetail').innerHTML='<div class="empty">暂无接口详情</div>';return}
  const requests=rows.length, success=rows.filter((d)=>!d.failed).length, failure=requests-success, rate=requests?success/requests*100:100;
  const tokens=rows.reduce((s,d)=>s+d.total_tokens,0), cached=rows.reduce((s,d)=>s+d.cached_tokens,0), reasoning=rows.reduce((s,d)=>s+d.reasoning_tokens,0), cost=rows.reduce((s,d)=>s+d.cost,0);
  const models=groupedRows(rows,(d)=>d.model,(d)=>d.model||'unknown');
  const sources=groupedRows(rows,sourceKey,sourceLabel);
  const errors=rows.filter((d)=>d.failed).reduce((map,d)=>{const key=(d.status_code||0)+'|'+(d.failure||'');const r=map.get(key)||{status:d.status_code||'-',failure:d.failure||'未返回错误内容',count:0};r.count++;map.set(key,r);return map},new Map());
  const errorRows=[...errors.values()].sort((a,b)=>b.count-a.count);
  setText('apiDetailTitle',friendlyApiName(api));
  $('apiDetail').innerHTML='<div class="detailGrid">'+
    metric('请求数',fmt.format(requests),'<span class="ok">成功 '+fmt.format(success)+'</span> <span class="bad">失败 '+fmt.format(failure)+'</span>')+
    metric('成功率','<span class="'+(rate>=95?'ok':rate>=80?'neutral':'bad')+'">'+pct(rate)+'</span>')+
    metric('总 token',compact(tokens),'缓存 '+compact(cached)+' · 思考 '+compact(reasoning))+
    metric('平均延迟',formatMs(avg(rows.map((d)=>d.latency_ms))),'TTFT '+formatMs(avg(rows.map((d)=>d.ttft_ms))))+
    metric('估算花费',money.format(cost))+
    metric('模型数',fmt.format(models.length),'来源 '+fmt.format(sources.length))+
    '</div>'+
    '<div class="splitGrid">'+bars('模型分布',models,requests,'暂无模型数据')+bars('来源分布',sources,requests,'暂无来源数据')+'</div>'+
    '<div class="splitGrid"><div><div class="subtle" style="margin-bottom:8px">错误统计</div>'+
    (errorRows.length?'<div class="tableWrap"><table><thead><tr><th>状态码</th><th>次数</th><th>错误</th></tr></thead><tbody>'+errorRows.slice(0,10).map((r)=>'<tr><td class="bad">'+esc(r.status)+'</td><td>'+fmt.format(r.count)+'</td><td class="errorText">'+esc(r.failure)+'</td></tr>').join('')+'</tbody></table></div>':'<div class="empty">暂无失败请求</div>')+
    '</div><div><div class="subtle" style="margin-bottom:8px">最近请求</div>'+
    '<div class="tableWrap"><table><thead><tr><th>时间</th><th>模型</th><th>结果</th><th>延迟</th><th>token</th><th>来源</th></tr></thead><tbody>'+rows.slice(0,120).map((d)=>'<tr><td>'+new Date(d.timestamp_ms).toLocaleString()+'</td><td class="nameCell">'+esc(d.model)+'</td><td class="'+(d.failed?'bad':'ok')+'">'+(d.failed?'失败':'成功')+'</td><td>'+formatMs(num(d.latency_ms))+'</td><td>'+fmt.format(d.total_tokens)+'</td><td>'+esc(sourceLabel(d))+'</td></tr>').join('')+'</tbody></table></div></div></div>';
}
function renderModelStats(){
  const map=new Map(); details.forEach((d)=>{const r=map.get(d.model)||{model:d.model,requests:0,success:0,failure:0,tokens:0,cost:0,latency:[]}; r.requests++; d.failed?r.failure++:r.success++; r.tokens+=d.total_tokens; r.cost+=d.cost; if(num(d.latency_ms)>0)r.latency.push(num(d.latency_ms)); map.set(d.model,r)});
  const rows=[...map.values()].sort((a,b)=>b.requests-a.requests);
  $('modelStats').innerHTML=rows.length?'<table><thead><tr><th>模型</th><th>请求</th><th>token</th><th>平均延迟</th><th>成功率</th><th>花费</th></tr></thead><tbody>'+rows.map((r)=>{const rate=r.requests?r.success/r.requests*100:100; const avg=r.latency.length?r.latency.reduce((a,b)=>a+b,0)/r.latency.length:0; return '<tr><td class="nameCell">'+esc(r.model)+'</td><td>'+fmt.format(r.requests)+' <span class="ok">('+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+')</span></td><td>'+compact(r.tokens)+'</td><td>'+formatMs(avg)+'</td><td class="'+(rate>=95?'ok':rate>=80?'neutral':'bad')+'">'+pct(rate)+'</td><td>'+money.format(r.cost)+'</td></tr>'}).join('')+'</tbody></table>':'<div class="empty">暂无模型数据</div>';
}
function renderFilters(){
  const fill=(id,label,values)=>{const old=$(id).value;$(id).innerHTML='<option value="">全部'+label+'</option>'+values.map((v)=>'<option value="'+esc(v)+'">'+esc(v)+'</option>').join('');$(id).value=[...values,''].includes(old)?old:''};
  fill('filterModel','模型',modelNames()); fill('filterSource','来源',[...new Set(details.map(sourceLabel))].sort()); fill('filterAuth','凭证',[...new Set(details.map((d)=>d.auth_index||'-'))].sort());
}
function renderEvents(){
  const fm=$('filterModel').value, fs=$('filterSource').value, fa=$('filterAuth').value;
  const rows=details.filter((d)=>(!fm||d.model===fm)&&(!fs||sourceLabel(d)===fs)&&(!fa||(d.auth_index||'-')===fa));
  setText('eventsCount','共 '+fmt.format(rows.length)+' 条，显示 '+fmt.format(Math.min(rows.length,500))+' 条');
  $('events').innerHTML=rows.length?'<table><thead><tr><th>时间</th><th>模型</th><th>来源</th><th>凭证</th><th>结果</th><th>延迟</th><th>输入</th><th>输出</th><th>思考</th><th>缓存</th><th>总计</th></tr></thead><tbody>'+rows.slice(0,500).map((d)=>'<tr><td>'+new Date(d.timestamp_ms).toLocaleString()+'</td><td class="nameCell">'+esc(d.model)+'</td><td>'+esc(sourceLabel(d))+'</td><td>'+(esc(d.auth_index||'-'))+'</td><td class="'+(d.failed?'bad':'ok')+'">'+(d.failed?'失败':'成功')+'</td><td>'+formatMs(num(d.latency_ms))+'</td><td>'+fmt.format(num(d.tokens?.input_tokens))+'</td><td>'+fmt.format(num(d.tokens?.output_tokens))+'</td><td>'+fmt.format(num(d.tokens?.reasoning_tokens))+'</td><td>'+fmt.format(d.cached_tokens)+'</td><td>'+fmt.format(d.total_tokens)+'</td></tr>').join('')+'</tbody></table>':'<div class="empty">暂无请求事件</div>';
}
function download(name,text,type){const a=document.createElement('a');a.href=URL.createObjectURL(new Blob([text],{type}));a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(a.href),1000)}
function rowsCsv(rows){const head=['时间','接口','模型','来源','凭证','结果','延迟毫秒','TTFT毫秒','输入 token','输出 token','思考 token','缓存 token','总 token','状态码','错误'];return [head,...rows.map((d)=>[d.timestamp,d.api,d.model,sourceLabel(d),d.auth_index||'',d.failed?'失败':'成功',num(d.latency_ms),num(d.ttft_ms),num(d.tokens?.input_tokens),num(d.tokens?.output_tokens),num(d.tokens?.reasoning_tokens),d.cached_tokens,d.total_tokens,d.status_code||'',d.failure||''])].map((row)=>row.map((v)=>'"'+String(v??'').replace(/"/g,'""')+'"').join(',')).join('\\n')}
function exportRows(kind){const rows=[...details]; const stamp=new Date().toISOString().replace(/[:.]/g,'-'); if(kind==='json'){download('usage-events-'+stamp+'.json',JSON.stringify(rows,null,2),'application/json;charset=utf-8');return} download('usage-events-'+stamp+'.csv',rowsCsv(rows),'text/csv;charset=utf-8')}
function exportApiRows(kind){const rows=currentApiDetails();if(!rows.length)return;const stamp=new Date().toISOString().replace(/[:.]/g,'-');const name=(friendlyApiName(selectedApi)||'api').replace(/[\\\\/:*?"<>|\\s]+/g,'-').slice(0,80);if(kind==='json'){download('usage-api-'+name+'-'+stamp+'.json',JSON.stringify(rows,null,2),'application/json;charset=utf-8');return}download('usage-api-'+name+'-'+stamp+'.csv',rowsCsv(rows),'text/csv;charset=utf-8')}
function rerender(){details=collectDetails(usage);renderPrices();renderStats();renderHealth();renderCredentials();renderClientApiStats();renderApiStats();renderApiDetail();renderModelStats();renderFilters();renderEvents()}
function schedulePoll(delayMs){if(pollTimer)clearTimeout(pollTimer);pollTimer=setTimeout(load,delayMs)}
function nextFailureDelay(){return Math.min(300000,[5000,15000,45000,90000,180000][Math.min(pollFailures-1,4)]||300000)}
async function load() {
  try {
    const response = await fetch('./dashboard-data', { cache: 'no-store' });
    if (!response.ok) throw new Error('请求失败：' + response.status);
    const data = await response.json();
    rawUsage=data.usage||{}; usage=filteredUsage(rawUsage,$('range').value); setText('updated','更新于 '+new Date(data.generated_at||Date.now()).toLocaleTimeString()); rerender();
    pollFailures=0;schedulePoll(30000);
  } catch (error) {
    setText('updated', error.message || '加载用量统计失败');
    pollFailures++;schedulePoll(nextFailureDelay());
  }
}
$('range').value=localStorage.getItem(rangeKey)||'24h'; $('range').onchange=()=>{localStorage.setItem(rangeKey,$('range').value); usage=filteredUsage(rawUsage,$('range').value); rerender()};
$('refreshBtn').onclick=load;
$('savePrice').onclick=()=>{const m=$('priceModel').value;if(!m)return;const prompt=num($('pricePrompt').value), completion=num($('priceCompletion').value), cache=$('priceCache').value===''?prompt:num($('priceCache').value);modelPrices[m]={prompt,completion,cache};savePrices();$('pricePrompt').value='';$('priceCompletion').value='';$('priceCache').value='';rerender()};
$('priceModel').onchange=()=>{const p=modelPrices[$('priceModel').value]||{};$('pricePrompt').value=p.prompt??'';$('priceCompletion').value=p.completion??'';$('priceCache').value=p.cache??''};
document.querySelectorAll('[data-api-sort]').forEach((btn)=>btn.onclick=()=>{clientApiSort=btn.dataset.apiSort||'requests';renderClientApiStats()});
['filterModel','filterSource','filterAuth'].forEach((id)=>$(id).onchange=renderEvents); $('clearFilters').onclick=()=>{['filterModel','filterSource','filterAuth'].forEach((id)=>$(id).value='');renderEvents()};
$('exportRowsCsv').onclick=()=>exportRows('csv'); $('exportRowsJson').onclick=()=>exportRows('json');
$('exportApiCsv').onclick=()=>exportApiRows('csv'); $('exportApiJson').onclick=()=>exportApiRows('json');
$('exportBtn').onclick=async()=>{const r=await fetch('./usage/export',{cache:'no-store'});download('usage-export-'+new Date().toISOString().replace(/[:.]/g,'-')+'.json',JSON.stringify(await r.json(),null,2),'application/json;charset=utf-8')};
$('importBtn').onclick=()=>$('importFile').click(); $('importFile').onchange=async(e)=>{const file=e.target.files?.[0]; if(!file)return; const text=await file.text(); const r=await fetch('./usage/import',{method:'POST',headers:{'Content-Type':'application/json'},body:text}); if(!r.ok)alert('导入失败'); await load(); e.target.value=''};
load();
</script>
</body>
</html>`

func handleExportUsage() ([]byte, error) {
	snapshot := stats.Snapshot()

	exportPayload := ExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Usage:      snapshot,
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
	const maxBodySize = 50 * 1024 * 1024 // 50 MB
	const maxRecordCount = 200000

	if len(body) > maxBodySize {
		return errorEnvelope("payload_too_large",
			fmt.Sprintf("import body exceeds max size of %d bytes", maxBodySize)), nil
	}

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

	// Pre-count records and validate bounds.
	recordCount := 0
	for _, apiSnapshot := range importPayload.Usage.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			recordCount += len(modelSnapshot.Details)
		}
	}
	if recordCount > maxRecordCount {
		return errorEnvelope("too_many_records",
			fmt.Sprintf("import payload contains %d records, max allowed is %d", recordCount, maxRecordCount)), nil
	}

	// Validate individual records for boundary issues.
	for _, apiSnapshot := range importPayload.Usage.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.TTFTMs < 0 {
					detail.TTFTMs = 0
				}
				t := detail.Tokens
				if t.TotalTokens < 0 || t.InputTokens < 0 || t.OutputTokens < 0 ||
					t.ReasoningTokens < 0 || t.CachedTokens < 0 || t.CacheTokens < 0 {
					return errorEnvelope("invalid_record",
						"negative token count found in import payload"), nil
				}
			}
		}
	}

	result := stats.MergeSnapshot(importPayload.Usage)
	snapshot := stats.Snapshot()

	responseData := ImportResponse{
		Added:              result.Added,
		Skipped:            result.Skipped,
		IgnoredByRetention: result.IgnoredByRetention,
		TotalRequests:      snapshot.TotalRequests,
		FailedRequests:     snapshot.FailureCount,
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

// PluginRegisterResponse is the structured response for plugin.register.
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

// ManagementRegisterResponse is the structured response for management.register.
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

// ExportPayload is the structured response for /usage/export.
type ExportPayload struct {
	Version    int                `json:"version"`
	ExportedAt string             `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

// ImportResponse is the structured response for /usage/import.
type ImportResponse struct {
	Added              int64 `json:"added"`
	Skipped            int64 `json:"skipped"`
	IgnoredByRetention int64 `json:"ignored_by_retention"`
	TotalRequests      int64 `json:"total_requests"`
	FailedRequests     int64 `json:"failed_requests"`
}

// ============================================================================
// Statistics Storage
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

	// Header filtering: only write response headers whose lowercase name
	// appears in this set (or "*" for all). Empty means log none.
	logResponseHeaders map[string]bool
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

type RequestDetail struct {
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

// apiKeySalt is a per-process random salt used to produce stable grouping IDs
// for API keys without leaking the original key.  Two keys that happen to share
// the same masked prefix will produce different hashes.
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
		// Fallback: deterministic salt
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
	if cfg.LogResponseHeaders != "" {
		s.logResponseHeaders = parseHeaderWhitelist(cfg.LogResponseHeaders)
	}
	s.pruneLocked(time.Now(), true) // sort here: config may shrink retention/max
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

	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}

	statsKey := usageGroupKey(record)

	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	detail := RequestDetail{
		Timestamp: timestamp,
		LatencyMs: record.Latency.Milliseconds(),
		TTFTMs:    record.TTFT.Milliseconds(),
		APIKey:     maskAPIKey(record.APIKey),
		APIKeyHash: hashAPIKey(record.APIKey),
		Source:    usageSource(record),
		Provider:  strings.TrimSpace(record.Provider),
		AuthID:    strings.TrimSpace(record.AuthID),
		AuthIndex: strings.TrimSpace(record.AuthIndex),
		AuthType:  strings.TrimSpace(record.AuthType),
		Thinking:  usageThinking(record),
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

	// Increment global top-level and time-series counters (incremental path).
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

	// Prune (retention + max-details) with decrement-on-eviction; no sort on normal write.
	s.pruneLocked(now, false)
	// Prune stale seen entries; the seen map is maintained incrementally.
	s.pruneSeenLocked(now)
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) {
	apiSt.TotalRequests++
	if detail.Failed {
		apiSt.FailureCount++
	} else {
		apiSt.SuccessCount++
	}
	apiSt.TotalTokens += detail.Tokens.TotalTokens

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
	modelSt.TotalTokens += detail.Tokens.TotalTokens
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
			// Filter by retention and decrement counters for removed entries.
			if !cutoff.IsZero() {
				kept := details[:0]
				for _, d := range details {
					if d.Timestamp.IsZero() || !d.Timestamp.Before(cutoff) {
						kept = append(kept, d)
					} else {
						s.decrementCounters(d, apiSt, modelSt)
					}
				}
				details = kept
			}
			// Only sort when explicitly requested (e.g. after import or reconfigure).
			if sortNeeded {
				sort.SliceStable(details, func(i, j int) bool {
					return details[i].Timestamp.Before(details[j].Timestamp)
				})
			}
			// Trim to max and decrement counters for removed entries.
			if s.maxDetailsPerModel >= 0 && len(details) > s.maxDetailsPerModel {
				keep := s.maxDetailsPerModel
				removed := details[:len(details)-keep]
				for _, d := range removed {
					s.decrementCounters(d, apiSt, modelSt)
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

// decrementCounters subtracts one detail's contribution from the global,
// API-level, model-level, and time-series counters. It must only be called
// when a detail is removed from storage (retention expiry or max-detail trim).
func (s *RequestStatistics) decrementCounters(d RequestDetail, apiSt *apiStats, modelSt *modelStats) {
	totalTokens := d.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}
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
				totalTokens := detail.Tokens.TotalTokens
				if totalTokens < 0 {
					totalTokens = 0
				}
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

func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Pre-compute retention cutoff so expired records are counted as ignored
	// rather than added then immediately pruned.
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
			if strings.TrimSpace(modelName) == "" {
				modelName = "unknown"
			}

			for _, detail := range modelSnapshot.Details {
				if detail.Timestamp.IsZero() {
					detail.Timestamp = now
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}

				// Pre-filter by retention: expired records never enter storage.
				if !cutoff.IsZero() && !detail.Timestamp.IsZero() && detail.Timestamp.Before(cutoff) {
					result.IgnoredByRetention++
					continue
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

	s.pruneLocked(now, true) // sort after import: records may arrive out of order
	s.rebuildAggregatesLocked()
	s.rebuildSeenLocked(now)
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

// looksLikeSecretKey returns true when raw looks like an API key rather than
// a human-readable identifier.  CPA may pass the actual key (or a masked/hashed
// form) in the Source field for some provider types.
func looksLikeSecretKey(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	// Starts with common key prefixes.
	if strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "AIza") ||
		strings.HasPrefix(s, "hf_") || strings.HasPrefix(s, "pk_") ||
		strings.HasPrefix(s, "rk_") {
		return true
	}
	// Long hex strings without spaces are likely fingerprints / hashes.
	if len(s) >= 40 && !strings.ContainsAny(s, " /.-_") {
		return true
	}
	// Very long tokens (>80 chars) without spaces.
	if len(s) >= 80 && !strings.Contains(s, " ") {
		return true
	}
	return false
}

func maskAPIKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return s[:1] + "******"
	}
	prefix := 2
	suffix := 2
	if len(s) < prefix+suffix {
		return s[:1] + "******" + s[len(s)-1:]
	}
	return s[:prefix] + "******" + s[len(s)-suffix:]
}

func stripCredentialSuffix(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	// Split on common separators: " · ", " - ", " | ", "/"
	parts := splitBySeparators(value)
	for i, part := range parts {
		normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(part, "-", ""), "_", "")))
		if normalized == "apikey" || normalized == "key" || normalized == "credential" || normalized == "auth" {
			if i > 0 {
				return strings.Join(parts[:i], " · ")
			}
		}
	}
	if len(parts) > 1 && looksLikeCredentialID(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " · ")
	}
	if len(parts) > 1 && looksLikeSecretKey(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " · ")
	}
	colonParts := strings.Split(value, ":")
	if len(colonParts) >= 3 && looksLikeCredentialID(colonParts[len(colonParts)-1]) {
		return strings.Join(colonParts[:len(colonParts)-1], ":")
	}
	return value
}

// splitBySeparators splits s on " · ", " - ", " | ", or "/" in priority order.
func splitBySeparators(s string) []string {
	if strings.Contains(s, " · ") {
		return strings.Split(s, " · ")
	}
	if strings.Contains(s, " - ") {
		return strings.Split(s, " - ")
	}
	if strings.Contains(s, " | ") {
		return strings.Split(s, " | ")
	}
	if strings.Contains(s, "/") {
		return strings.Split(s, "/")
	}
	return []string{s}
}

func looksLikeCredentialID(raw string) bool {
	s := strings.TrimSpace(raw)
	if len(s) >= 8 {
		allHex := true
		for _, ch := range s {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				allHex = false
				break
			}
		}
		if allHex {
			return true
		}
	}
	return len(s) >= 32 && !strings.ContainsAny(s, " /.-_")
}

// friendlySourceName turns a raw source value into a human-readable label.
// It never leaks API keys.
func friendlySourceName(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := stripCredentialSuffix(record.Source)

	// If source is a clean name (not a key), use it directly.
	if source != "" && !looksLikeSecretKey(source) {
		return source
	}
	name := provider
	if name == "" {
		name = executor
	}
	if name == "" {
		name = "unknown"
	}
	return stripCredentialSuffix(name)
}

func usageGroupKey(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := stripCredentialSuffix(record.Source)

	parts := make([]string, 0, 3)
	if provider != "" {
		parts = append(parts, provider)
	} else if executor != "" {
		parts = append(parts, executor)
	}
	// Use friendly name for the source part — never leak keys.
	// Remove source != provider/exclusive check so e.g. "openai" + "openai-prod"
	// source is not merged with bare "openai".
	if source != "" && !looksLikeSecretKey(source) {
		// Skip if source duplicates provider or executor exactly.
		dup := false
		for _, p := range parts {
			if p == source {
				dup = true
				break
			}
		}
		if !dup {
			parts = append(parts, source)
		}
	}
	if len(parts) == 0 {
		return "未知接口"
	}
	return strings.Join(parts, " · ")
}

func usageSource(record UsageRecord) string {
	return friendlySourceName(record)
}

func usageThinking(record UsageRecord) UsageThinking {
	effort := strings.TrimSpace(record.ReasoningEffort)
	if effort == "" {
		return UsageThinking{}
	}
	return UsageThinking{Intensity: effort, Level: effort}
}

func trimLong(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// parseHeaderWhitelist converts a comma-separated list of header names
// into a lookup set keyed by lowercase name. Supports "*" as wildcard to
// record ALL headers.
func parseHeaderWhitelist(raw string) map[string]bool {
	set := make(map[string]bool)
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		set[strings.ToLower(name)] = true
	}
	return set
}

// filterHeaders returns a copy of headers containing only keys whose
// lowercase name is in the whitelist. If whitelist contains "*", all
// headers are copied. If whitelist is nil or empty, nil is returned
// (log no headers).
func filterHeaders(headers map[string][]string, whitelist map[string]bool) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	if len(whitelist) == 0 {
		return nil
	}
	if whitelist["*"] {
		out := make(map[string][]string, len(headers))
		for k, v := range headers {
			vv := make([]string, len(v))
			copy(vv, v)
			out[k] = vv
		}
		return out
	}
	out := make(map[string][]string)
	for k, v := range headers {
		if whitelist[strings.ToLower(k)] {
			vv := make([]string, len(v))
			copy(vv, v)
			out[k] = vv
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// redactSensitiveText removes common API key patterns, auth headers,
// and long token-like strings from error bodies before storage.
func redactSensitiveText(value string) string {
	if value == "" {
		return ""
	}
	// Redact known key prefix patterns: sk-xxx -> sk******xx
	value = redactKeyPrefix(value, "sk-")
	value = redactKeyPrefix(value, "AIza")
	value = redactKeyPrefix(value, "hf_")
	value = redactKeyPrefix(value, "pk_")
	value = redactKeyPrefix(value, "rk_")

	// Redact Authorization: / Bearer headers
	value = redactAuthHeader(value, "Authorization:")
	value = redactAuthHeader(value, "authorization:")
	value = redactAuthHeader(value, "Bearer ")
	value = redactAuthHeader(value, "bearer ")

	// Redact x-api-key: header
	value = redactAuthHeader(value, "X-API-Key:")
	value = redactAuthHeader(value, "x-api-key:")
	value = redactAuthHeader(value, "Api-Key:")
	value = redactAuthHeader(value, "api-key:")

	// Redact URL query params carrying secrets
	value = redactQueryParam(value, "key")
	value = redactQueryParam(value, "token")
	value = redactQueryParam(value, "api_key")
	value = redactQueryParam(value, "apikey")

	return value
}

const redactedMarker = "******"

func redactKeyPrefix(s, prefix string) string {
	result := s
	for {
		idx := strings.Index(result, prefix)
		if idx < 0 {
			break
		}
		end := strings.IndexFunc(result[idx+len(prefix):], func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_'
		})
		var token string
		if end < 0 {
			token = result[idx:]
		} else {
			token = result[idx : idx+len(prefix)+end]
		}
		masked := maskToken(token)
		result = strings.Replace(result, token, masked, 1)
	}
	return result
}

func redactAuthHeader(s, marker string) string {
	idx := strings.Index(s, marker)
	if idx < 0 {
		return s
	}
	rest := s[idx+len(marker):]
	// Skip optional whitespace after marker
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return s
	}
	// Find end of token (space, comma, newline, or end of string)
	end := strings.IndexAny(rest, " ,\n\r;")
	var token string
	if end < 0 {
		token = rest
	} else {
		token = rest[:end]
	}
	if len(token) == 0 {
		return s
	}
	// Only redact if it looks like a secret
	if !looksLikeSecretToken(token) {
		return s
	}
	masked := maskToken(token)
	return strings.Replace(s, marker+" "+token, marker+" "+masked, 1)
}

func redactQueryParam(s, param string) string {
	// Match param=value in URL query strings
	prefixes := []string{param + "=", param + " %3D ", param + "%3D"}
	for _, prefix := range prefixes {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			continue
		}
		afterIdx := idx + len(prefix)
		rest := s[afterIdx:]
		end := strings.IndexAny(rest, " &;\n\r")
		var value string
		if end < 0 {
			value = rest
		} else {
			value = rest[:end]
		}
		if len(value) > 0 && looksLikeSecretToken(value) {
			s = s[:afterIdx] + redactedMarker + s[afterIdx+len(value):]
		}
	}
	return s
}

func looksLikeSecretToken(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return false
	}
	// Known prefixes
	for _, p := range []string{"sk-", "AIza", "hf_", "pk_", "rk_"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	// Long alphanumeric sequences (>=32 chars)
	if len(s) >= 32 && !strings.Contains(s, " ") {
		return true
	}
	return false
}

func maskToken(token string) string {
	if len(token) <= 4 {
		return redactedMarker
	}
	show := 2
	return token[:show] + redactedMarker + token[len(token)-show:]
}
