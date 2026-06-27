package main

import (
	_ "embed"
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

func init() {
	h := strings.Replace(dashboardPageHTML, "</head>",
		"<style>\n"+dashboardPageCSS+"\n</style></head>", 1)
	h = strings.Replace(h, "</body>",
		"<script>\n"+dashboardHelpersJS+"\n"+dashboardPageJS+"\n</script></body>", 1)
	completeDashboardHTML = h
}

// handleDashboardSummary returns lightweight dashboard data without detail arrays.
func handleDashboardSummary() ([]byte, error) {
	summary := stats.SummaryWithoutDetails()
	responseJSON, err := json.Marshal(summary)
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
func handleDashboardEvents(query map[string][]string) ([]byte, error) {
	params := dashboardEventsQuery(query)
	result := stats.QueryEvents(params)
	responseJSON, err := json.Marshal(result)
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

// handleDashboardEventsExport returns all filtered event details for one export.
func handleDashboardEventsExport(query map[string][]string) ([]byte, error) {
	params := dashboardEventsQuery(query)
	result := stats.QueryAllEvents(params)
	responseJSON, err := json.Marshal(result)
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

// handleDashboardAPIDetail returns compact per-upstream detail widgets.
func handleDashboardAPIDetail(query map[string][]string) ([]byte, error) {
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

	result := stats.QueryAPIDetail(api, rangeKey, recentLimit, errorLimit)
	responseJSON, err := json.Marshal(result)
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

// handleHealthCheck returns a lightweight health/status endpoint.
func handleHealthCheck() ([]byte, error) {
	type HealthResponse struct {
		Status        string        `json:"status"`
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

	health := HealthResponse{
		Status:        "ok",
		DetailCount:   detailCount,
		TotalRequests: summary.Usage.TotalRequests,
		EvictedTotal:  evicted,
		Config:        stats.ConfigSnapshot(),
		Storage:       stats.StorageStatus(),
		Runtime:       stats.RuntimeStatus(),
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
