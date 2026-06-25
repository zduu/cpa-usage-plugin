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
	"crypto/sha256"
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
				"description": "请求、token 和模型用量统计。",
			},
			{
				"path":        "/dashboard-data",
				"description": "用量统计看板数据。",
			},
			{
				"path":        "/usage/export",
				"description": "用量统计导出数据。",
			},
			{
				"path":        "/usage/import",
				"description": "用量统计导入数据。",
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
.header{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:16px;flex-wrap:wrap}
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
.priceGrid{display:grid;grid-template-columns:2fr repeat(3,1fr) auto;gap:8px;align-items:end}
.priceList{display:grid;gap:6px;margin-top:12px}
.priceItem{display:flex;justify-content:space-between;gap:10px;align-items:center;border:1px solid #e5e5e5;border-radius:8px;padding:8px 10px}
.priceMeta{display:flex;gap:10px;color:#6f6963;font-size:12px;flex-wrap:wrap}
.healthSummary{display:flex;gap:14px;align-items:baseline;flex-wrap:wrap}
.healthRate{font-size:18px;font-weight:800}
.healthScroller{overflow:auto;padding-bottom:4px}
.healthGrid{display:grid;grid-template-columns:repeat(96,12px);grid-auto-rows:12px;gap:4px;min-width:max-content}
.healthCell{width:12px;height:12px;border-radius:2px;background:#ececec;border:1px solid rgba(0,0,0,.04)}
.healthCell.active{cursor:default}
.legend{display:flex;gap:6px;align-items:center;color:#8b8680;font-size:11px;margin-top:8px;flex-wrap:wrap}
.legendDot{width:12px;height:12px;border-radius:2px;display:inline-block}
.filters{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:10px}
.eventsMeta{display:flex;justify-content:space-between;color:#8b8680;font-size:12px;margin-bottom:6px;gap:8px;flex-wrap:wrap}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono",monospace}
.nowrap{white-space:nowrap}
.tooltip{position:fixed;z-index:20;background:#262626;color:#fff;border-radius:8px;padding:8px 10px;font-size:11px;pointer-events:none;box-shadow:0 12px 28px rgba(0,0,0,.22);max-width:260px;line-height:1.6;white-space:nowrap}
.tooltip .ok{color:#10b981}.tooltip .bad{color:#c65746}
.hidden{display:none!important}
@media(max-width:1120px){.cards{grid-template-columns:repeat(2,minmax(0,1fr))}.layout{grid-template-columns:1fr}}
@media(max-width:640px){.shell{padding:14px}h1{font-size:20px}.cards{grid-template-columns:1fr}.value{font-size:24px}.priceGrid{grid-template-columns:1fr}.btn,.select,.input{width:100%}.toolbar{width:100%}.toolbar>*{flex:1 1 150px}}
@media(prefers-color-scheme:dark){:root{background:#111315;color:#f2f2f2}body{background:#111315;color:#f2f2f2}.panel,.stat,th,.btn,.select,.input{background:#181b1f;border-color:#30343a;color:#f2f2f2}.spark,.empty{background:#121417;border-color:#30343a}.label,th,.subtle,.updated,.neutral,.meta,.legend{color:#a8a29b}td{border-color:#30343a}.nameCell{color:#f2f2f2}.pill{background:#20242a;border-color:#3a3f46;color:#c9c3bc}.healthCell{background:#2a2f35}.tooltip{background:#f2f2f2;color:#1b1b1b}}
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
      <div class="panelHead"><h2>凭证统计</h2><span class="subtle">按来源/凭证聚合成功率</span></div>
      <div class="tableWrap" id="credentialStats"></div>
    </div>
    <div class="panel">
      <div class="panelHead"><h2>接口详细统计</h2><span class="subtle">按插件可识别的提供商、来源和凭证聚合</span></div>
      <div class="tableWrap" id="apiStats"></div>
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
const compact=(value)=>new Intl.NumberFormat('zh-CN',{notation:'compact',maximumFractionDigits:1}).format(num(value));
const pct=(value)=>Number.isFinite(value)?value.toFixed(1)+'%':'-';
const formatMs=(value)=>Number.isFinite(value)&&value>0?(value>=1000?(value/1000).toFixed(2)+'秒':Math.round(value)+'毫秒'):'-';
function loadPrices(){try{return JSON.parse(localStorage.getItem(storeKey)||'{}')||{}}catch{return {}}}
function savePrices(){localStorage.setItem(storeKey,JSON.stringify(modelPrices))}
function timestampMs(value){const ms=Date.parse(value);return Number.isFinite(ms)?ms:0}
function totalTokens(detail){const t=detail.tokens||{};return num(t.total_tokens)||num(t.input_tokens)+num(t.output_tokens)+num(t.reasoning_tokens)+Math.max(num(t.cached_tokens),num(t.cache_tokens))}
function detailCost(detail){const p=modelPrices[detail.model];if(!p)return 0;const t=detail.tokens||{};const cached=Math.max(num(t.cached_tokens),num(t.cache_tokens));const input=Math.max(num(t.input_tokens)-cached,0);const output=Math.max(num(t.output_tokens),0);return input/1e6*num(p.prompt)+output/1e6*num(p.completion)+cached/1e6*num(p.cache)}
function looksLikeKey(v){return typeof v==='string'&&(v.startsWith('sk-')||v.startsWith('AIza')||v.startsWith('hf_')||v.length>=80)}
function sourceLabel(detail){const s=detail.source||'';if(s&&!looksLikeKey(s))return s;const p=detail.provider||'';if(p&&!looksLikeKey(p))return p;const a=detail.auth_id||'';if(a&&!looksLikeKey(a))return a;return detail.auth_index||'未知来源'}
function sourceKey(detail){return sourceLabel(detail)+'|'+(detail.auth_index||'')+'|'+(detail.auth_type||'')}
function friendlyApiName(apiName){if(!apiName)return'未知接口';const parts=apiName.split(' · ').filter(function(p){return !looksLikeKey(p)});return parts.length?parts.join(' · '):apiName}
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
function renderHealth(){
  const count=672, step=15*60e3, now=Date.now(), start=now-count*step; const stats=Array.from({length:count},()=>({s:0,f:0}));
  details.forEach((d)=>{if(d.timestamp_ms<start||d.timestamp_ms>now)return; const idx=count-1-Math.floor((now-d.timestamp_ms)/step); if(idx>=0&&idx<count){d.failed?stats[idx].f++:stats[idx].s++}});
  let totalS=0,totalF=0; const cells=[]; const tooltips=[];
  stats.forEach((x,i)=>{
    totalS+=x.s;totalF+=x.f;const total=x.s+x.f;const rate=total?x.s/total:-1;
    const t0=new Date(start+i*step),t1=new Date(start+(i+1)*step);
    const timeRange=t0.toLocaleString()+' - '+t1.toLocaleString();
    const tip='<span>'+timeRange+'</span><br><span class="ok">成功 '+x.s+'</span> <span class="bad">失败 '+x.f+'</span>'+(total?' <span>成功率 '+pct(rate*100)+'</span>':'');
    tooltips.push(tip);
    cells.push('<div class="healthCell '+(total?'active':'')+'" data-health-idx="'+i+'" style="'+(total?'background:'+healthColor(rate):'')+'"></div>');
  });
  $('healthGrid').innerHTML=cells.join('');
  const tip=$('tooltip');
  $('healthGrid').onmouseover=function(e){
    const cell=e.target.closest('.healthCell');
    if(!cell||!cell.classList.contains('active')){tip.classList.add('hidden');return}
    const idx=parseInt(cell.dataset.healthIdx);if(isNaN(idx)||idx<0||idx>=count){tip.classList.add('hidden');return}
    tip.innerHTML=tooltips[idx];tip.classList.remove('hidden');
    const r=cell.getBoundingClientRect();let left=r.right+6,top=r.top-4;
    if(left+260>window.innerWidth)left=r.left-266;if(top+60>window.innerHeight)top=window.innerHeight-70;
    tip.style.left=left+'px';tip.style.top=top+'px';
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
  const map=new Map(); details.forEach((d)=>{const key=sourceKey(d); const row=map.get(key)||{name:sourceLabel(d),type:d.auth_type||d.provider||'',success:0,failure:0,total:0}; d.failed?row.failure++:row.success++; row.total=row.success+row.failure; map.set(key,row)});
  const rows=[...map.values()].sort((a,b)=>b.total-a.total);
  $('credentialStats').innerHTML=rows.length?'<table><thead><tr><th>凭证</th><th>请求次数</th><th>成功率</th></tr></thead><tbody>'+rows.map((r)=>{const rate=r.total?r.success/r.total*100:100;return '<tr><td class="nameCell">'+esc(r.name)+(r.type?'<span class="pill">'+esc(r.type)+'</span>':'')+'</td><td>'+fmt.format(r.total)+' <span class="ok">('+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+')</span></td><td class="'+(rate>=95?'ok':rate>=80?'neutral':'bad')+'">'+pct(rate)+'</td></tr>'}).join('')+'</tbody></table>':'<div class="empty">暂无凭证数据</div>';
}
function renderApiStats(){
  const rows=Object.entries(usage?.apis||{}).map(([api,a])=>({api,requests:num(a.total_requests),success:num(a.success_count),failure:num(a.failure_count),tokens:num(a.total_tokens),models:a.models||{},cost:collectDetails({apis:{[api]:a}}).reduce((s,d)=>s+d.cost,0)})).sort((a,b)=>b.requests-a.requests);
  $('apiStats').innerHTML=rows.length?'<table><thead><tr><th>接口</th><th>请求</th><th>token</th><th>花费</th><th>模型</th></tr></thead><tbody>'+rows.map((r)=>'<tr><td class="nameCell">'+esc(friendlyApiName(r.api))+'</td><td>'+fmt.format(r.requests)+' <span class="ok">('+fmt.format(r.success)+'</span> <span class="bad">'+fmt.format(r.failure)+')</span></td><td>'+compact(r.tokens)+'</td><td>'+money.format(r.cost)+'</td><td>'+Object.keys(r.models).slice(0,4).map(esc).join('、')+'</td></tr>').join('')+'</tbody></table>':'<div class="empty">暂无接口数据</div>';
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
function exportRows(kind){const rows=[...details]; const stamp=new Date().toISOString().replace(/[:.]/g,'-'); if(kind==='json'){download('usage-events-'+stamp+'.json',JSON.stringify(rows,null,2),'application/json;charset=utf-8');return} const head=['时间','模型','来源','凭证','结果','延迟毫秒','输入 token','输出 token','思考 token','缓存 token','总 token']; const csv=[head,...rows.map((d)=>[d.timestamp,d.model,sourceLabel(d),d.auth_index||'',d.failed?'失败':'成功',num(d.latency_ms),num(d.tokens?.input_tokens),num(d.tokens?.output_tokens),num(d.tokens?.reasoning_tokens),d.cached_tokens,d.total_tokens])].map((row)=>row.map((v)=>'"'+String(v??'').replace(/"/g,'""')+'"').join(',')).join('\\n'); download('usage-events-'+stamp+'.csv',csv,'text/csv;charset=utf-8')}
function rerender(){details=collectDetails(usage);renderPrices();renderStats();renderHealth();renderCredentials();renderApiStats();renderModelStats();renderFilters();renderEvents()}
async function load() {
  try {
    const response = await fetch('./dashboard-data', { cache: 'no-store' });
    if (!response.ok) throw new Error('请求失败：' + response.status);
    const data = await response.json();
    rawUsage=data.usage||{}; usage=filteredUsage(rawUsage,$('range').value); setText('updated','更新于 '+new Date(data.generated_at||Date.now()).toLocaleTimeString()); rerender();
  } catch (error) {
    setText('updated', error.message || '加载用量统计失败');
  }
}
$('range').value=localStorage.getItem(rangeKey)||'24h'; $('range').onchange=()=>{localStorage.setItem(rangeKey,$('range').value); usage=filteredUsage(rawUsage,$('range').value); rerender()};
$('refreshBtn').onclick=load;
$('savePrice').onclick=()=>{const m=$('priceModel').value;if(!m)return;const prompt=num($('pricePrompt').value), completion=num($('priceCompletion').value), cache=$('priceCache').value===''?prompt:num($('priceCache').value);modelPrices[m]={prompt,completion,cache};savePrices();$('pricePrompt').value='';$('priceCompletion').value='';$('priceCache').value='';rerender()};
$('priceModel').onchange=()=>{const p=modelPrices[$('priceModel').value]||{};$('pricePrompt').value=p.prompt??'';$('priceCompletion').value=p.completion??'';$('priceCache').value=p.cache??''};
['filterModel','filterSource','filterAuth'].forEach((id)=>$(id).onchange=renderEvents); $('clearFilters').onclick=()=>{['filterModel','filterSource','filterAuth'].forEach((id)=>$(id).value='');renderEvents()};
$('exportRowsCsv').onclick=()=>exportRows('csv'); $('exportRowsJson').onclick=()=>exportRows('json');
$('exportBtn').onclick=async()=>{const r=await fetch('./usage/export',{cache:'no-store'});download('usage-export-'+new Date().toISOString().replace(/[:.]/g,'-')+'.json',JSON.stringify(await r.json(),null,2),'application/json;charset=utf-8')};
$('importBtn').onclick=()=>$('importFile').click(); $('importFile').onchange=async(e)=>{const file=e.target.files?.[0]; if(!file)return; const text=await file.text(); const r=await fetch('./usage/import',{method:'POST',headers:{'Content-Type':'application/json'},body:text}); if(!r.ok)alert('导入失败'); await load(); e.target.value=''};
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

	statsKey := usageGroupKey(record)

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
		TTFTMs:    record.TTFT.Milliseconds(),
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
		Failure:    trimLong(record.Failure.Body, 500),
		Headers:    record.ResponseHeaders,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
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

// friendlySourceName turns a raw source value into a human-readable label.
// It never leaks API keys.
func friendlySourceName(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := strings.TrimSpace(record.Source)
	authType := strings.TrimSpace(record.AuthType)
	authIndex := strings.TrimSpace(record.AuthIndex)
	authID := strings.TrimSpace(record.AuthID)

	// If source is a clean name (not a key), use it directly.
	if source != "" && !looksLikeSecretKey(source) {
		return source
	}
	// AuthID from OAuth / auth files is usually a clean identifier.
	if authID != "" && !looksLikeSecretKey(authID) {
		return authID
	}
	// Build from provider / executor + auth info.
	name := provider
	if name == "" {
		name = executor
	}
	if name == "" {
		name = "unknown"
	}
	// Append authType for disambiguation (e.g. "opencode · openai").
	if authType != "" && authType != name {
		name = name + " · " + authType
	}
	// Append authIndex if present (e.g. "opencode · openai · 3").
	if authIndex != "" {
		name = name + " · " + authIndex
	}
	return name
}

func usageGroupKey(record UsageRecord) string {
	provider := strings.TrimSpace(record.Provider)
	executor := strings.TrimSpace(record.ExecutorType)
	source := strings.TrimSpace(record.Source)
	authType := strings.TrimSpace(record.AuthType)
	authIndex := strings.TrimSpace(record.AuthIndex)

	parts := make([]string, 0, 3)
	if provider != "" {
		parts = append(parts, provider)
	} else if executor != "" {
		parts = append(parts, executor)
	}
	if authType != "" && authType != provider && authType != executor {
		parts = append(parts, authType)
	}
	// Use friendly name for the source part — never leak keys.
	if source != "" && !looksLikeSecretKey(source) {
		parts = append(parts, source)
	} else if authIndex != "" {
		parts = append(parts, authIndex)
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

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:4])
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
