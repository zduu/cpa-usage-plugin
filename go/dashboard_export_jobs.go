package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	dashboardExportJobTTL       = 15 * time.Minute
	dashboardExportJobMaxActive = 2
	dashboardExportJobMaxStored = 16
)

const (
	dashboardExportJobQueued    = "queued"
	dashboardExportJobRunning   = "running"
	dashboardExportJobSucceeded = "succeeded"
	dashboardExportJobFailed    = "failed"
)

var dashboardExportJobs = newDashboardExportJobManager()

type dashboardExportJobManager struct {
	mu     sync.Mutex
	jobs   map[string]*dashboardExportJob
	closed bool
}

type dashboardExportJob struct {
	ID          string
	Status      string
	Params      EventsQuery
	Options     dashboardEventsExportOptions
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	ExpiresAt   time.Time
	Error       string
	Total       int
	Exported    int
	Truncated   bool
	RawBytes    int
	BodyBytes   int
	ContentType string
	ETag        string
	FilePath    string
}

type dashboardExportJobResponse struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Format       string `json:"format"`
	Gzip         bool   `json:"gzip"`
	Limit        int    `json:"limit,omitempty"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Error        string `json:"error,omitempty"`
	Total        int    `json:"total,omitempty"`
	Exported     int    `json:"exported,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	RawBytes     int    `json:"raw_bytes,omitempty"`
	BodyBytes    int    `json:"body_bytes,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	DownloadPath string `json:"download_path,omitempty"`
}

type dashboardExportJobListResponse struct {
	Jobs []dashboardExportJobResponse `json:"jobs"`
}

type dashboardExportJobErrorResponse struct {
	Error string `json:"error"`
}

func newDashboardExportJobManager() *dashboardExportJobManager {
	return &dashboardExportJobManager{jobs: make(map[string]*dashboardExportJob)}
}

func handleDashboardEventsExportJobCreate(query map[string][]string) ([]byte, error) {
	params := normalizeEventsQuery(dashboardEventsQuery(query), false)
	opts := dashboardEventsExportOptionsFromQuery(query)
	opts.Limit = effectiveDashboardExportLimit(opts.Limit, stats.ExportMaxRecords())
	job, statusCode, message := dashboardExportJobs.create(params, opts)
	if message != "" {
		return dashboardExportJobJSON(statusCode, dashboardExportJobErrorResponse{Error: message})
	}
	return dashboardExportJobJSON(statusCode, dashboardExportJobSnapshot(job))
}

func handleDashboardEventsExportJobStatus(query map[string][]string) ([]byte, error) {
	id := dashboardExportJobID(query)
	if id == "" {
		return dashboardExportJobJSON(http.StatusOK, dashboardExportJobListResponse{Jobs: dashboardExportJobs.list()})
	}
	job, ok := dashboardExportJobs.get(id)
	if !ok {
		return dashboardExportJobJSON(http.StatusNotFound, dashboardExportJobErrorResponse{Error: "export job not found"})
	}
	return dashboardExportJobJSON(http.StatusOK, dashboardExportJobSnapshot(job))
}

func handleDashboardEventsExportJobDelete(query map[string][]string) ([]byte, error) {
	id := dashboardExportJobID(query)
	if id == "" {
		return dashboardExportJobJSON(http.StatusBadRequest, dashboardExportJobErrorResponse{Error: "missing export job id"})
	}
	if !dashboardExportJobs.delete(id) {
		return dashboardExportJobJSON(http.StatusNotFound, dashboardExportJobErrorResponse{Error: "export job not found"})
	}
	return dashboardExportJobJSON(http.StatusOK, map[string]string{"status": "deleted"})
}

func handleDashboardEventsExportDownload(query map[string][]string) ([]byte, error) {
	id := dashboardExportJobID(query)
	if id == "" {
		return dashboardExportJobJSON(http.StatusBadRequest, dashboardExportJobErrorResponse{Error: "missing export job id"})
	}
	job, ok := dashboardExportJobs.get(id)
	if !ok {
		return dashboardExportJobJSON(http.StatusNotFound, dashboardExportJobErrorResponse{Error: "export job not found"})
	}
	switch job.Status {
	case dashboardExportJobSucceeded:
	case dashboardExportJobFailed:
		return dashboardExportJobJSON(http.StatusInternalServerError, dashboardExportJobSnapshot(job))
	default:
		return dashboardExportJobJSON(http.StatusAccepted, dashboardExportJobSnapshot(job))
	}

	body, err := os.ReadFile(job.FilePath)
	if err != nil {
		return dashboardExportJobJSON(http.StatusGone, dashboardExportJobErrorResponse{Error: "export job file is no longer available"})
	}
	resp := ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    dashboardExportJobDownloadHeaders(job),
		Body:       body,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func (m *dashboardExportJobManager) create(params EventsQuery, opts dashboardEventsExportOptions) (dashboardExportJob, int, string) {
	now := time.Now()
	m.mu.Lock()
	m.cleanupLocked(now)
	if m.closed {
		m.mu.Unlock()
		return dashboardExportJob{}, http.StatusServiceUnavailable, "export jobs are shutting down"
	}
	if m.activeLocked() >= dashboardExportJobMaxActive {
		m.mu.Unlock()
		return dashboardExportJob{}, http.StatusTooManyRequests, "too many active export jobs"
	}
	if len(m.jobs) >= dashboardExportJobMaxStored {
		m.mu.Unlock()
		return dashboardExportJob{}, http.StatusTooManyRequests, "too many retained export jobs"
	}

	id := newDashboardExportJobID()
	filePath := filepath.Join(os.TempDir(), "cpa-usage-events-export-"+id)
	job := &dashboardExportJob{
		ID:        id,
		Status:    dashboardExportJobQueued,
		Params:    params,
		Options:   opts,
		CreatedAt: now,
		ExpiresAt: now.Add(dashboardExportJobTTL),
		FilePath:  filePath,
	}
	m.jobs[id] = job
	snapshot := *job
	m.mu.Unlock()

	go m.run(id, params, opts, filePath)
	return snapshot, http.StatusAccepted, ""
}

func (m *dashboardExportJobManager) run(id string, params EventsQuery, opts dashboardEventsExportOptions, filePath string) {
	startedAt := time.Now()
	m.update(id, func(job *dashboardExportJob) {
		job.Status = dashboardExportJobRunning
		job.StartedAt = startedAt
	})

	result := stats.QueryExportEvents(params, opts.Limit)
	body, contentType, err := encodeDashboardEventsExport(result, opts)
	if err != nil {
		m.fail(id, err)
		return
	}
	if m.isClosed() {
		_ = os.Remove(filePath + ".tmp")
		_ = os.Remove(filePath)
		return
	}
	rawBytes := len(body)
	if opts.Gzip {
		body, err = gzipBytes(body)
		if err != nil {
			m.fail(id, err)
			return
		}
	}
	if m.isClosed() {
		_ = os.Remove(filePath + ".tmp")
		_ = os.Remove(filePath)
		return
	}
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, body, 0600); err != nil {
		m.fail(id, err)
		return
	}
	if m.isClosed() {
		_ = os.Remove(tmpPath)
		_ = os.Remove(filePath)
		return
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		m.fail(id, err)
		return
	}

	sum := sha256.Sum256(body)
	finishedAt := time.Now()
	stats.RecordEventsExport(string(opts.Format), opts.Gzip, result, rawBytes, len(body), finishedAt.Sub(startedAt))
	m.update(id, func(job *dashboardExportJob) {
		job.Status = dashboardExportJobSucceeded
		job.FinishedAt = finishedAt
		job.ExpiresAt = finishedAt.Add(dashboardExportJobTTL)
		job.Total = result.Total
		job.Exported = len(result.Events)
		job.Truncated = result.Truncated
		job.RawBytes = rawBytes
		job.BodyBytes = len(body)
		job.ContentType = contentType
		job.ETag = `W/"events-export-job-` + hex.EncodeToString(sum[:]) + `"`
	})
}

func (m *dashboardExportJobManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for id, job := range m.jobs {
		delete(m.jobs, id)
		_ = os.Remove(job.FilePath + ".tmp")
		_ = os.Remove(job.FilePath)
	}
}

func (m *dashboardExportJobManager) fail(id string, err error) {
	now := time.Now()
	m.update(id, func(job *dashboardExportJob) {
		job.Status = dashboardExportJobFailed
		job.FinishedAt = now
		job.ExpiresAt = now.Add(dashboardExportJobTTL)
		job.Error = err.Error()
		_ = os.Remove(job.FilePath + ".tmp")
		_ = os.Remove(job.FilePath)
	})
}

func (m *dashboardExportJobManager) get(id string) (dashboardExportJob, bool) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	job, ok := m.jobs[id]
	if !ok {
		return dashboardExportJob{}, false
	}
	return *job, true
}

func (m *dashboardExportJobManager) list() []dashboardExportJobResponse {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	jobs := make([]dashboardExportJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, *job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	responses := make([]dashboardExportJobResponse, 0, len(jobs))
	for _, job := range jobs {
		responses = append(responses, dashboardExportJobSnapshot(job))
	}
	return responses
}

func (m *dashboardExportJobManager) delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return false
	}
	delete(m.jobs, id)
	_ = os.Remove(job.FilePath + ".tmp")
	_ = os.Remove(job.FilePath)
	return true
}

func (m *dashboardExportJobManager) update(id string, update func(*dashboardExportJob)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if job, ok := m.jobs[id]; ok {
		update(job)
	}
}

func (m *dashboardExportJobManager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *dashboardExportJobManager) activeLocked() int {
	active := 0
	for _, job := range m.jobs {
		if job.Status == dashboardExportJobQueued || job.Status == dashboardExportJobRunning {
			active++
		}
	}
	return active
}

func (m *dashboardExportJobManager) cleanupLocked(now time.Time) {
	for id, job := range m.jobs {
		if job.Status == dashboardExportJobQueued || job.Status == dashboardExportJobRunning {
			continue
		}
		expiresAt := job.ExpiresAt
		if expiresAt.IsZero() {
			expiresAt = job.CreatedAt.Add(dashboardExportJobTTL)
		}
		if now.Before(expiresAt) {
			continue
		}
		delete(m.jobs, id)
		_ = os.Remove(job.FilePath + ".tmp")
		_ = os.Remove(job.FilePath)
	}
}

func dashboardExportJobSnapshot(job dashboardExportJob) dashboardExportJobResponse {
	response := dashboardExportJobResponse{
		ID:          job.ID,
		Status:      job.Status,
		Format:      string(job.Options.Format),
		Gzip:        job.Options.Gzip,
		Limit:       job.Options.Limit,
		CreatedAt:   formatExportJobTime(job.CreatedAt),
		StartedAt:   formatExportJobTime(job.StartedAt),
		FinishedAt:  formatExportJobTime(job.FinishedAt),
		ExpiresAt:   formatExportJobTime(job.ExpiresAt),
		Error:       job.Error,
		Total:       job.Total,
		Exported:    job.Exported,
		Truncated:   job.Truncated,
		RawBytes:    job.RawBytes,
		BodyBytes:   job.BodyBytes,
		ContentType: job.ContentType,
	}
	if job.Status == dashboardExportJobSucceeded {
		response.DownloadPath = "/dashboard-events-export-download?id=" + job.ID
	}
	return response
}

func dashboardExportJobDownloadHeaders(job dashboardExportJob) map[string][]string {
	headers := dashboardExportHeaders(job.ETag, job.ContentType, job.Options.Gzip)
	headers["X-Total-Count"] = []string{strconv.Itoa(job.Total)}
	headers["X-Exported-Count"] = []string{strconv.Itoa(job.Exported)}
	headers["X-Export-Truncated"] = []string{strconv.FormatBool(job.Truncated)}
	headers["Content-Disposition"] = []string{fmt.Sprintf(`attachment; filename="usage-events-%s.%s"`, job.ID, dashboardExportJobFileExtension(job.Options))}
	return headers
}

func dashboardExportJobFileExtension(opts dashboardEventsExportOptions) string {
	ext := "json"
	switch opts.Format {
	case dashboardExportCSV:
		ext = "csv"
	case dashboardExportJSONL:
		ext = "jsonl"
	}
	if opts.Gzip {
		return ext + ".gz"
	}
	return ext
}

func dashboardExportJobJSON(statusCode int, body interface{}) ([]byte, error) {
	responseJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp := ManagementResponse{
		StatusCode: statusCode,
		Headers: map[string][]string{
			"Content-Type":  {"application/json; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: responseJSON,
	}
	return okEnvelopeJSON(string(mustMarshal(resp)))
}

func dashboardExportJobID(query map[string][]string) string {
	for _, key := range []string{"id", "job_id", "job"} {
		if values, ok := query[key]; ok && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func formatExportJobTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func newDashboardExportJobID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
