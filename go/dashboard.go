package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed dashboard/index.html
var dashboardPageHTML string

//go:embed dashboard/style.css
var dashboardPageCSS string

//go:embed dashboard/helpers.js
var dashboardHelpersJS string

//go:embed dashboard/script.js
var dashboardPageJS string

var completeDashboardHTML string

type HealthAlert struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func init() {
	h := strings.Replace(dashboardPageHTML, "</head>",
		"<style>\n"+dashboardPageCSS+"\n</style></head>", 1)
	h = strings.Replace(h, "</body>",
		"<script>\n"+dashboardHelpersJS+"\n"+dashboardPageJS+"\n</script></body>", 1)
	completeDashboardHTML = h
}

// handleDashboardSummary returns lightweight dashboard data without detail arrays.
func handleDashboardSummary(headers map[string][]string) ([]byte, error) {
	etag := dashboardSummaryETag(time.Now())
	if dashboardConditionalMatch("dashboard-summary", headers, etag) {
		return dashboardNotModified(etag)
	}
	summary := stats.SummaryWithoutDetails()
	responseJSON, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    dashboardJSONHeaders(etag),
		Body:       responseJSON,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func dashboardSummaryETag(now time.Time) string {
	window := summaryHealthWindow(now).UTC().Format(time.RFC3339)
	return dashboardWeakETag("summary", strconv.FormatUint(stats.DashboardVersion(), 10), window)
}

func dashboardEventsQuery(query map[string][]string) EventsQuery {
	params := EventsQuery{
		Limit:  50,
		Offset: 0,
	}
	if v, ok := query["limit"]; ok && len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			params.Limit = n
		}
	}
	if v, ok := query["offset"]; ok && len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n >= 0 {
			params.Offset = n
		}
	}
	if v, ok := query["range"]; ok && len(v) > 0 {
		params.Range = v[0]
	}
	if v, ok := query["model"]; ok && len(v) > 0 {
		params.Model = v[0]
	}
	if v, ok := query["source"]; ok && len(v) > 0 {
		params.Source = v[0]
	}
	if v, ok := query["auth"]; ok && len(v) > 0 {
		params.AuthIndex = v[0]
	}
	if v, ok := query["api"]; ok && len(v) > 0 {
		params.API = v[0]
	}
	return params
}

// handleDashboardEvents returns paginated, filtered event details.
func handleDashboardEvents(query map[string][]string, headers map[string][]string) ([]byte, error) {
	params := dashboardEventsQuery(query)
	params = normalizeEventsQuery(params, true)
	etag := dashboardEventsETag(params, time.Now())
	if dashboardConditionalMatch("dashboard-events", headers, etag) {
		return dashboardNotModified(etag)
	}
	result := stats.QueryEvents(params)
	responseJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    dashboardJSONHeaders(etag),
		Body:       responseJSON,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func dashboardEventsETag(params EventsQuery, now time.Time) string {
	key := dashboardEventCacheKeyFor(params, now)
	return dashboardWeakETag(
		"events",
		strconv.FormatUint(stats.DashboardVersion(), 10),
		strconv.Itoa(key.limit),
		strconv.Itoa(key.offset),
		strconv.FormatInt(key.timeBucket, 10),
		key.rangeKey,
		key.model,
		key.source,
		key.authIndex,
		key.api,
	)
}

type dashboardExportFormat string

const (
	dashboardExportJSON  dashboardExportFormat = "json"
	dashboardExportJSONL dashboardExportFormat = "jsonl"
	dashboardExportCSV   dashboardExportFormat = "csv"
)

type dashboardEventsExportOptions struct {
	Format dashboardExportFormat
	Gzip   bool
	Limit  int
}

func dashboardEventsExportOptionsFromQuery(query map[string][]string) dashboardEventsExportOptions {
	opts := dashboardEventsExportOptions{Format: dashboardExportJSON}
	if v, ok := query["format"]; ok && len(v) > 0 {
		switch strings.ToLower(strings.TrimSpace(v[0])) {
		case "jsonl", "ndjson":
			opts.Format = dashboardExportJSONL
		case "csv":
			opts.Format = dashboardExportCSV
		default:
			opts.Format = dashboardExportJSON
		}
	}
	if queryBool(query, "gzip") || queryBool(query, "compress") || queryValue(query, "encoding") == "gzip" {
		opts.Gzip = true
	}
	if v, ok := query["limit"]; ok && len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	return opts
}

func queryValue(query map[string][]string, key string) string {
	if v, ok := query[key]; ok && len(v) > 0 {
		return strings.ToLower(strings.TrimSpace(v[0]))
	}
	return ""
}

func queryBool(query map[string][]string, key string) bool {
	switch queryValue(query, key) {
	case "1", "true", "yes", "y", "on", "gzip":
		return true
	default:
		return false
	}
}

// handleDashboardEventsExport returns all filtered event details for one export.
func handleDashboardEventsExport(query map[string][]string, headers map[string][]string) ([]byte, error) {
	params := normalizeEventsQuery(dashboardEventsQuery(query), false)
	opts := dashboardEventsExportOptionsFromQuery(query)
	opts.Limit = effectiveDashboardExportLimit(opts.Limit, stats.ExportMaxRecords())
	etag := dashboardEventsExportETag(params, opts, time.Now())
	if dashboardConditionalMatch("dashboard-events-export", headers, etag) {
		return dashboardNotModifiedWithHeaders(dashboardExportHeaders(etag, dashboardExportContentType(opts.Format), opts.Gzip))
	}
	startedAt := time.Now()
	result := stats.QueryExportEvents(params, opts.Limit)
	body, contentType, err := encodeDashboardEventsExport(result, opts)
	if err != nil {
		return nil, err
	}
	rawBytes := len(body)
	if opts.Gzip {
		body, err = gzipBytes(body)
		if err != nil {
			return nil, err
		}
	}
	stats.RecordEventsExport(string(opts.Format), opts.Gzip, result, rawBytes, len(body), time.Since(startedAt))
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    dashboardExportResultHeaders(etag, contentType, opts.Gzip, result),
		Body:       body,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func effectiveDashboardExportLimit(requestLimit int, configuredLimit int) int {
	if configuredLimit <= 0 {
		return requestLimit
	}
	if requestLimit <= 0 || requestLimit > configuredLimit {
		return configuredLimit
	}
	return requestLimit
}

func dashboardEventsExportETag(params EventsQuery, opts dashboardEventsExportOptions, now time.Time) string {
	key := dashboardEventCacheKeyFor(params, now)
	return dashboardWeakETag(
		"events-export",
		strconv.FormatUint(stats.DashboardVersion(), 10),
		string(opts.Format),
		strconv.FormatBool(opts.Gzip),
		strconv.Itoa(opts.Limit),
		strconv.FormatInt(key.timeBucket, 10),
		key.rangeKey,
		key.model,
		key.source,
		key.authIndex,
		key.api,
	)
}

func encodeDashboardEventsExport(result EventsResult, opts dashboardEventsExportOptions) ([]byte, string, error) {
	switch opts.Format {
	case dashboardExportJSONL:
		raw, err := dashboardEventsJSONL(result.Events)
		return raw, dashboardExportContentType(opts.Format), err
	case dashboardExportCSV:
		raw, err := dashboardEventsCSV(result.Events)
		return raw, dashboardExportContentType(opts.Format), err
	default:
		raw, err := json.Marshal(result)
		return raw, dashboardExportContentType(opts.Format), err
	}
}

func dashboardExportContentType(format dashboardExportFormat) string {
	switch format {
	case dashboardExportJSONL:
		return "application/x-ndjson; charset=utf-8"
	case dashboardExportCSV:
		return "text/csv; charset=utf-8"
	default:
		return "application/json; charset=utf-8"
	}
}

func dashboardExportHeaders(etag string, contentType string, gzipped bool) map[string][]string {
	headers := map[string][]string{
		"Content-Type":  {contentType},
		"Cache-Control": {"private, no-cache"},
		"ETag":          {etag},
	}
	if gzipped {
		headers["Content-Encoding"] = []string{"gzip"}
	}
	return headers
}

func dashboardExportResultHeaders(etag string, contentType string, gzipped bool, result EventsResult) map[string][]string {
	headers := dashboardExportHeaders(etag, contentType, gzipped)
	headers["X-Total-Count"] = []string{strconv.Itoa(result.Total)}
	headers["X-Exported-Count"] = []string{strconv.Itoa(len(result.Events))}
	headers["X-Export-Truncated"] = []string{strconv.FormatBool(result.Truncated)}
	return headers
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func dashboardEventsJSONL(events []RequestDetail) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func dashboardEventsCSV(events []RequestDetail) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{"时间", "模型", "来源", "凭证", "结果", "延迟毫秒", "TTFT毫秒", "输入 token", "输出 token", "思考 token", "缓存 token", "总 token", "状态码", "错误"}); err != nil {
		return nil, err
	}
	for _, event := range events {
		tokens := event.Tokens
		status := "成功"
		if event.Failed {
			status = "失败"
		}
		if err := writer.Write([]string{
			event.Timestamp.UTC().Format(time.RFC3339),
			event.Model,
			dashboardExportSource(event),
			event.AuthIndex,
			status,
			strconv.FormatInt(event.LatencyMs, 10),
			strconv.FormatInt(event.TTFTMs, 10),
			strconv.FormatInt(tokens.InputTokens, 10),
			strconv.FormatInt(tokens.OutputTokens, 10),
			strconv.FormatInt(tokens.ReasoningTokens, 10),
			strconv.FormatInt(normalizedCacheTokens(tokens), 10),
			strconv.FormatInt(detailTotalTokens(tokens), 10),
			dashboardExportStatusCode(event),
			event.Failure,
		}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}

func dashboardExportSource(event RequestDetail) string {
	if strings.TrimSpace(event.Source) != "" {
		return event.Source
	}
	if strings.TrimSpace(event.Provider) != "" {
		return event.Provider
	}
	return "未知来源"
}

func dashboardExportStatusCode(event RequestDetail) string {
	if event.StatusCode == 0 {
		return ""
	}
	return strconv.Itoa(event.StatusCode)
}

// handleDashboardAPIDetail returns compact per-upstream detail widgets.
func handleDashboardAPIDetail(query map[string][]string, headers map[string][]string) ([]byte, error) {
	api := ""
	if v, ok := query["api"]; ok && len(v) > 0 {
		api = v[0]
	}
	rangeKey := ""
	if v, ok := query["range"]; ok && len(v) > 0 {
		rangeKey = v[0]
	}
	recentLimit := 120
	if v, ok := query["recent_limit"]; ok && len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			recentLimit = n
		}
	}
	errorLimit := 20
	if v, ok := query["error_limit"]; ok && len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			errorLimit = n
		}
	}

	etag := dashboardAPIDetailETag(api, rangeKey, recentLimit, errorLimit, time.Now())
	if dashboardConditionalMatch("dashboard-api-detail", headers, etag) {
		return dashboardNotModified(etag)
	}
	result := stats.QueryAPIDetail(api, rangeKey, recentLimit, errorLimit)
	responseJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    dashboardJSONHeaders(etag),
		Body:       responseJSON,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func dashboardAPIDetailETag(api string, rangeKey string, recentLimit int, errorLimit int, now time.Time) string {
	timeBucket := int64(0)
	if rangeKey != "" && rangeKey != "all" {
		timeBucket = now.UTC().Unix()
	}
	return dashboardWeakETag(
		"api-detail",
		strconv.FormatUint(stats.DashboardVersion(), 10),
		strconv.FormatInt(timeBucket, 10),
		api,
		rangeKey,
		strconv.Itoa(recentLimit),
		strconv.Itoa(errorLimit),
	)
}

func dashboardWeakETag(parts ...string) string {
	sum := sha256.Sum224([]byte(strings.Join(parts, "\x00")))
	return `W/"` + hex.EncodeToString(sum[:]) + `"`
}

func dashboardJSONHeaders(etag string) map[string][]string {
	return map[string][]string{
		"Content-Type":  {"application/json; charset=utf-8"},
		"Cache-Control": {"private, no-cache"},
		"ETag":          {etag},
	}
}

func dashboardNotModified(etag string) ([]byte, error) {
	return dashboardNotModifiedWithHeaders(dashboardJSONHeaders(etag))
}

func dashboardNotModifiedWithHeaders(headers map[string][]string) ([]byte, error) {
	resp := ManagementResponse{
		StatusCode: http.StatusNotModified,
		Headers:    headers,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func dashboardConditionalMatch(endpoint string, headers map[string][]string, etag string) bool {
	hasValidator := dashboardHasIfNoneMatch(headers)
	matched := dashboardIfNoneMatch(headers, etag)
	stats.RecordConditionalRequest(endpoint, hasValidator, matched)
	return matched
}

func dashboardHasIfNoneMatch(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "If-None-Match") {
			continue
		}
		if len(values) == 0 {
			return true
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

func dashboardIfNoneMatch(headers map[string][]string, etag string) bool {
	if etag == "" {
		return false
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "If-None-Match") {
			continue
		}
		for _, value := range values {
			for _, candidate := range strings.Split(value, ",") {
				candidate = strings.TrimSpace(candidate)
				if candidate == "*" || candidate == etag {
					return true
				}
			}
		}
	}
	return false
}

// handleHealthCheck returns a lightweight health/status endpoint.
func handleHealthCheck() ([]byte, error) {
	type HealthResponse struct {
		Status        string        `json:"status"`
		Alerts        []HealthAlert `json:"alerts,omitempty"`
		DetailCount   int64         `json:"detail_count"`
		TotalRequests int64         `json:"total_requests"`
		EvictedTotal  int64         `json:"evicted_total"`
		Config        ExportConfig  `json:"config"`
		Storage       StorageStatus `json:"storage"`
		Runtime       RuntimeStatus `json:"runtime"`
		GeneratedAt   string        `json:"generated_at"`
	}

	detailCount := stats.DetailCount()
	evicted := stats.EvictedTotal()
	summary := stats.SummaryWithoutDetails()
	storage := stats.StorageStatus()
	runtime := stats.RuntimeStatus()
	alerts := healthAlerts(storage, runtime)

	health := HealthResponse{
		Status:        healthStatus(alerts),
		Alerts:        alerts,
		DetailCount:   detailCount,
		TotalRequests: summary.Usage.TotalRequests,
		EvictedTotal:  evicted,
		Config:        stats.ConfigSnapshot(),
		Storage:       storage,
		Runtime:       runtime,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	responseJSON, err := json.Marshal(health)
	if err != nil {
		return nil, err
	}
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		Body: responseJSON,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func healthStatus(alerts []HealthAlert) string {
	status := "ok"
	for _, alert := range alerts {
		if alert.Severity == "error" {
			return "error"
		}
		if alert.Severity == "warn" {
			status = "warn"
		}
	}
	return status
}

func healthAlerts(storage StorageStatus, runtime RuntimeStatus) []HealthAlert {
	var alerts []HealthAlert
	if storage.LastError != "" {
		alerts = append(alerts, HealthAlert{
			Severity: "error",
			Code:     "storage_error",
			Message:  storage.LastError,
		})
	}
	switch storage.WritePressure {
	case "full":
		alerts = append(alerts, HealthAlert{
			Severity: "error",
			Code:     "storage_writer_full",
			Message:  "持久化写入队列已满",
		})
	case "backlog":
		alerts = append(alerts, HealthAlert{
			Severity: "warn",
			Code:     "storage_writer_backlog",
			Message:  "持久化写入队列积压",
		})
	case "slow":
		alerts = append(alerts, HealthAlert{
			Severity: "warn",
			Code:     "storage_writer_slow",
			Message:  "持久化写入偏慢",
		})
	}
	if runtime.LastEventsExportTruncated {
		alerts = append(alerts, HealthAlert{
			Severity: "warn",
			Code:     "events_export_truncated",
			Message:  "最近一次事件导出被上限截断",
		})
	}
	return alerts
}
