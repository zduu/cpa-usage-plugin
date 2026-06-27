package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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

	// Match on the trailing path segment(s) instead of a bare suffix so that
	// "/dashboard" never accidentally matches "/dashboard-summary" etc., and
	// route ordering no longer affects dispatch.
	tail := pathTail(req.Path)
	switch {
	case req.Method == "GET" && tail == "dashboard":
		return handleDashboardPage()
	case req.Method == "GET" && tail == "dashboard-summary":
		return handleDashboardSummary()
	case req.Method == "GET" && tail == "dashboard-events":
		return handleDashboardEvents(req.Query)
	case req.Method == "GET" && tail == "dashboard-events-export":
		return handleDashboardEventsExport(req.Query)
	case req.Method == "GET" && tail == "dashboard-api-detail":
		return handleDashboardAPIDetail(req.Query)
	case req.Method == "GET" && tail == "dashboard-data":
		return handleDashboardData()
	case req.Method == "GET" && tail == "model-prices":
		return handleGetModelPrices()
	case req.Method == "PUT" && tail == "model-prices":
		return handlePutModelPrice(req.Body)
	case req.Method == "DELETE" && tail == "model-prices":
		return handleDeleteModelPrice(req.Query)
	case req.Method == "GET" && tail == "export" && strings.Contains(req.Path, "/usage/"):
		return handleExportUsage()
	case req.Method == "POST" && tail == "import" && strings.Contains(req.Path, "/usage/"):
		return handleImportUsage(req.Body)
	case req.Method == "GET" && tail == "health":
		return handleHealthCheck()
	case req.Method == "GET" && tail == "usage":
		return handleGetUsage()
	}

	return errorEnvelope("not_found", "endpoint not found"), nil
}

// pathTail returns the segment after the final "/" in p (or p itself when there
// is no slash). It lets us dispatch on the resource name without suffix pitfalls.
func pathTail(p string) string {
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
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
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/dashboard-summary",
				Description: "获取用量统计看板摘要（不含请求明细）。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/dashboard-events",
				Description: "分页获取请求事件明细。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/dashboard-events-export",
				Description: "一次性导出筛选后的请求事件明细。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/dashboard-api-detail",
				Description: "获取单个上游接口的聚合详情。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/model-prices",
				Description: "获取全局模型价格表。",
			},
			{
				Method:      "PUT",
				Path:        "/plugins/usage-statistics/model-prices",
				Description: "新增或更新全局模型价格。",
			},
			{
				Method:      "DELETE",
				Path:        "/plugins/usage-statistics/model-prices",
				Description: "删除全局模型价格。",
			},
			{
				Method:      "GET",
				Path:        "/plugins/usage-statistics/health",
				Description: "获取插件运行健康状态。",
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
				Description: "用量统计看板数据（兼容旧版，含全部细节）。",
			},
			{
				Path:        "/dashboard-summary",
				Description: "用量统计看板摘要数据（不含请求明细）。",
			},
			{
				Path:        "/dashboard-events",
				Description: "用量统计请求事件明细（分页）。",
			},
			{
				Path:        "/dashboard-events-export",
				Description: "筛选后的请求事件明细导出数据。",
			},
			{
				Path:        "/dashboard-api-detail",
				Description: "单个上游接口聚合详情。",
			},
			{
				Path:        "/model-prices",
				Description: "全局模型价格表。",
			},
			{
				Path:        "/usage/export",
				Description: "用量统计导出数据。",
			},
			{
				Path:        "/usage/import",
				Description: "用量统计导入数据。",
			},
			{
				Path:        "/health",
				Description: "插件运行健康状态。",
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
		Body: []byte(completeDashboardHTML),
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

func handleGetModelPrices() ([]byte, error) {
	return modelPricesManagementResponse(stats.ModelPrices())
}

func handlePutModelPrice(body []byte) ([]byte, error) {
	var payload struct {
		Model string     `json:"model"`
		Price ModelPrice `json:"price"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return errorEnvelope("invalid_json", "failed to parse model price payload"), nil
	}
	response, err := stats.UpsertModelPrice(payload.Model, payload.Price)
	if err != nil {
		return errorEnvelope("invalid_price", err.Error()), nil
	}
	return modelPricesManagementResponse(response)
}

func handleDeleteModelPrice(query map[string][]string) ([]byte, error) {
	model := ""
	if values, ok := query["model"]; ok && len(values) > 0 {
		model = values[0]
	}
	response, err := stats.DeleteModelPrice(model)
	if err != nil {
		return errorEnvelope("invalid_price", err.Error()), nil
	}
	return modelPricesManagementResponse(response)
}

func modelPricesManagementResponse(data ModelPricesResponse) ([]byte, error) {
	responseJSON, err := json.Marshal(data)
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

func handleExportUsage() ([]byte, error) {
	snapshot := stats.Snapshot()

	exportPayload := ExportPayload{
		Version:     1,
		ExportedAt:  time.Now().UTC().Format(time.RFC3339),
		Plugin:      pluginVersion,
		DetailCount: stats.DetailCount(),
		Config:      stats.ConfigSnapshot(),
		Usage:       snapshot,
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
	const maxBodySize = 50 * 1024 * 1024
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

	var recordCount int64
	for _, apiSnapshot := range importPayload.Usage.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			recordCount += int64(len(modelSnapshot.Details))
		}
	}
	if recordCount > maxRecordCount {
		return errorEnvelope("too_many_records",
			fmt.Sprintf("import payload contains %d records, max allowed is %d", recordCount, maxRecordCount)), nil
	}

	for apiName, apiSnapshot := range importPayload.Usage.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for detailIndex, detail := range modelSnapshot.Details {
				t := detail.Tokens
				if t.TotalTokens < 0 || t.InputTokens < 0 || t.OutputTokens < 0 ||
					t.ReasoningTokens < 0 || t.CachedTokens < 0 || t.CacheTokens < 0 {
					return errorEnvelope("invalid_record",
						fmt.Sprintf("negative token count found at api=%q model=%q detail_index=%d",
							apiName, modelName, detailIndex)), nil
				}
			}
		}
	}

	result := stats.MergeSnapshot(importPayload.Usage)
	snapshot := stats.Snapshot()

	responseData := ImportResponse{
		InputRecords:       recordCount,
		AcceptedRecords:    result.Added + result.Skipped + result.IgnoredByRetention,
		RejectedRecords:    recordCount - result.Added - result.Skipped - result.IgnoredByRetention,
		Added:              result.Added,
		Skipped:            result.Skipped,
		IgnoredByRetention: result.IgnoredByRetention,
		TotalRequests:      snapshot.TotalRequests,
		FailedRequests:     snapshot.FailureCount,
	}

	// Track last import result
	stats.mu.Lock()
	stats.lastImportResult = &responseData
	stats.invalidateSummaryLocked()
	stats.mu.Unlock()

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
