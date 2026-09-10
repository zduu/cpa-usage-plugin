package main

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"
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
	dashboardExportJobPageSize  = 5000
	dashboardExportChunkBytes   = 256 << 10
)

const (
	dashboardExportJobQueued    = "queued"
	dashboardExportJobRunning   = "running"
	dashboardExportJobSucceeded = "succeeded"
	dashboardExportJobFailed    = "failed"
)

var dashboardExportJobs = newDashboardExportJobManager()

type dashboardExportJobManager struct {
	mu      sync.Mutex
	jobs    map[string]*dashboardExportJob
	closed  bool
	workers int
	wg      sync.WaitGroup
}

type dashboardExportJob struct {
	cancel      context.CancelFunc
	ctx         context.Context
	ID          string
	Status      string
	Params      EventsQuery
	Options     dashboardEventsExportOptions
	CreatedAt   time.Time
	SnapshotAt  time.Time
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

type dashboardExportFileResult struct {
	Total       int
	Exported    int
	Truncated   bool
	RawBytes    int
	BodyBytes   int
	ContentType string
	GeneratedAt string
}

type countingWriter struct {
	w io.Writer
	n int
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
	ETag         string `json:"etag,omitempty"`
	ChunkSize    int    `json:"chunk_size,omitempty"`
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

	if queryBool(query, "chunk") {
		return dashboardExportJobChunk(job, query)
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

type dashboardExportChunk struct {
	Offset   int64  `json:"offset"`
	Total    int64  `json:"total"`
	ETag     string `json:"etag"`
	Checksum string `json:"checksum_crc32"`
	Data     []byte `json:"data"`
}

// The management ABI encodes a whole response. A bounded chunk keeps file
// download allocations independent of the final export size, including gzip.
func dashboardExportJobChunk(job dashboardExportJob, query map[string][]string) ([]byte, error) {
	if version := queryRawValue(query, "version"); version == "" || version != job.ETag {
		return dashboardExportJobJSON(http.StatusPreconditionFailed, dashboardExportJobErrorResponse{Error: "export file version does not match"})
	}
	offset, err := strconv.ParseInt(queryRawValue(query, "offset"), 10, 64)
	if err != nil || offset < 0 {
		return dashboardExportJobJSON(http.StatusBadRequest, dashboardExportJobErrorResponse{Error: "invalid chunk offset"})
	}
	length := int64(dashboardExportChunkBytes)
	if raw := queryRawValue(query, "length"); raw != "" {
		length, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || length <= 0 || length > dashboardExportChunkBytes {
			return dashboardExportJobJSON(http.StatusBadRequest, dashboardExportJobErrorResponse{Error: "invalid chunk length"})
		}
	}
	file, err := os.Open(job.FilePath)
	if err != nil {
		return dashboardExportJobJSON(http.StatusGone, dashboardExportJobErrorResponse{Error: "export job file is no longer available"})
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(job.BodyBytes) {
		return dashboardExportJobJSON(http.StatusGone, dashboardExportJobErrorResponse{Error: "export job file changed"})
	}
	if offset > info.Size() || (offset == info.Size() && offset != 0) {
		return dashboardExportJobJSON(http.StatusRequestedRangeNotSatisfiable, dashboardExportJobErrorResponse{Error: "chunk offset is outside the file"})
	}
	body := make([]byte, min(length, info.Size()-offset))
	if _, err := file.ReadAt(body, offset); err != nil {
		return dashboardExportJobJSON(http.StatusGone, dashboardExportJobErrorResponse{Error: "export job file is incomplete"})
	}
	return dashboardExportJobJSON(http.StatusOK, dashboardExportChunk{
		Offset: offset, Total: info.Size(), ETag: job.ETag,
		Checksum: fmt.Sprintf("%08x", crc32.ChecksumIEEE(body)), Data: body,
	})
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
	ctx, cancel := context.WithCancel(context.Background())
	filePath := filepath.Join(os.TempDir(), "cpa-usage-events-export-"+id)
	job := &dashboardExportJob{
		ctx: ctx, cancel: cancel,
		ID:         id,
		Status:     dashboardExportJobQueued,
		Params:     params,
		Options:    opts,
		CreatedAt:  now,
		SnapshotAt: now,
		ExpiresAt:  now.Add(dashboardExportJobTTL),
		FilePath:   filePath,
	}
	m.jobs[id] = job
	m.workers++
	m.wg.Add(1)
	snapshot := *job
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); m.workers--; m.mu.Unlock() }()
		m.run(id, params, opts, filePath)
	}()
	return snapshot, http.StatusAccepted, ""
}

func (m *dashboardExportJobManager) run(id string, params EventsQuery, opts dashboardEventsExportOptions, filePath string) {
	startedAt := time.Now()
	m.mu.Lock()
	job, exists := m.jobs[id]
	if !exists || m.closed {
		m.mu.Unlock()
		return
	}
	job.Status, job.StartedAt = dashboardExportJobRunning, startedAt
	opts.ctx = job.ctx
	snapshotAt := job.SnapshotAt
	m.mu.Unlock()
	succeeded := false
	tmpPath := filePath + ".tmp"
	defer func() {
		if !succeeded {
			_ = os.Remove(tmpPath)
			_ = os.Remove(filePath)
		}
	}()
	encoded, err := encodeDashboardEventsExportFile(params, opts, tmpPath, snapshotAt)
	if err != nil {
		_ = os.Remove(tmpPath)
		m.fail(id, err)
		return
	}
	sum, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		m.fail(id, err)
		return
	}
	// Publish the file and completed state together with respect to deletion.
	m.mu.Lock()
	defer m.mu.Unlock()
	job, exists = m.jobs[id]
	if !exists || m.closed {
		return
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		job.Status, job.Error = dashboardExportJobFailed, err.Error()
		job.FinishedAt = time.Now()
		job.ExpiresAt = job.FinishedAt.Add(dashboardExportJobTTL)
		return
	}
	succeeded = true
	finishedAt := time.Now()
	stats.RecordEventsExportSummary(string(opts.Format), opts.Gzip, encoded.Total, encoded.Exported, encoded.Truncated, encoded.RawBytes, encoded.BodyBytes, finishedAt.Sub(startedAt))
	job.Status = dashboardExportJobSucceeded
	job.FinishedAt = finishedAt
	job.ExpiresAt = finishedAt.Add(dashboardExportJobTTL)
	job.Total = encoded.Total
	job.Exported = encoded.Exported
	job.Truncated = encoded.Truncated
	job.RawBytes = encoded.RawBytes
	job.BodyBytes = encoded.BodyBytes
	job.ContentType = encoded.ContentType
	job.ETag = `W/"events-export-job-` + hex.EncodeToString(sum[:]) + `"`
}

func encodeDashboardEventsExportFile(params EventsQuery, opts dashboardEventsExportOptions, filePath string, snapshotAt time.Time) (dashboardExportFileResult, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return dashboardExportFileResult{}, err
	}
	bodyCounter := &countingWriter{w: file}
	rawWriter := io.Writer(bodyCounter)
	var gzipWriter *gzip.Writer
	if opts.Gzip {
		gzipWriter = gzip.NewWriter(bodyCounter)
		rawWriter = gzipWriter
	}
	rawCounter := &countingWriter{w: rawWriter}

	result, encodeErr := encodeDashboardEventsExportPaged(rawCounter, params, opts, snapshotAt)
	if gzipWriter != nil {
		if closeErr := gzipWriter.Close(); encodeErr == nil {
			encodeErr = closeErr
		}
	}
	if closeErr := file.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return dashboardExportFileResult{}, encodeErr
	}
	result.RawBytes = rawCounter.n
	result.BodyBytes = bodyCounter.n
	return result, nil
}

func encodeDashboardEventsExportPaged(writer io.Writer, params EventsQuery, opts dashboardEventsExportOptions, snapshotAt time.Time) (dashboardExportFileResult, error) {
	if opts.ctx != nil {
		if err := opts.ctx.Err(); err != nil {
			return dashboardExportFileResult{}, err
		}
		writer = &exportContextWriter{ctx: opts.ctx, writer: writer}
	}
	contentType := dashboardExportContentType(opts.Format)
	// Freeze event values and computed prices before writing any page.
	opts.frozen = stats.captureEventExport(params, opts.Limit, snapshotAt)
	firstPage := opts.frozen.page(0, dashboardExportJobPageSize)
	result := dashboardExportFileResult{
		Total:       firstPage.Total,
		Truncated:   firstPage.Truncated,
		ContentType: contentType,
		GeneratedAt: firstPage.GeneratedAt,
	}
	switch opts.Format {
	case dashboardExportJSONL:
		exported, err := encodeDashboardEventsJSONLPaged(writer, opts, firstPage)
		result.Exported = exported
		return result, err
	case dashboardExportCSV:
		exported, err := encodeDashboardEventsCSVPaged(writer, opts, firstPage)
		result.Exported = exported
		return result, err
	default:
		exported, err := encodeDashboardEventsJSONPaged(writer, opts, firstPage)
		result.Exported = exported
		return result, err
	}
}

func encodeDashboardEventsJSONPaged(writer io.Writer, opts dashboardEventsExportOptions, firstPage EventsResult) (int, error) {
	if _, err := io.WriteString(writer, `{"events":[`); err != nil {
		return 0, err
	}
	first := true
	exported, err := encodeDashboardEventsPaged(opts, firstPage, func(event RequestDetail) error {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		first = false
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = writer.Write(raw)
		return err
	})
	if err != nil {
		return exported, err
	}
	generatedAt, err := json.Marshal(firstPage.GeneratedAt)
	if err != nil {
		return exported, err
	}
	if _, err := fmt.Fprintf(writer, `],"total":%d,"limit":%d,"offset":0`, firstPage.Total, firstPage.Limit); err != nil {
		return exported, err
	}
	if firstPage.Truncated {
		if _, err := io.WriteString(writer, `,"truncated":true`); err != nil {
			return exported, err
		}
	}
	if _, err := fmt.Fprintf(writer, `,"generated_at":%s}`, generatedAt); err != nil {
		return exported, err
	}
	return exported, nil
}

func encodeDashboardEventsJSONLPaged(writer io.Writer, opts dashboardEventsExportOptions, firstPage EventsResult) (int, error) {
	encoder := json.NewEncoder(writer)
	return encodeDashboardEventsPaged(opts, firstPage, func(event RequestDetail) error {
		return encoder.Encode(event)
	})
}

func encodeDashboardEventsCSVPaged(writer io.Writer, opts dashboardEventsExportOptions, firstPage EventsResult) (int, error) {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(dashboardEventsCSVHeader()); err != nil {
		return 0, err
	}
	exported, err := encodeDashboardEventsPaged(opts, firstPage, func(event RequestDetail) error {
		return csvWriter.Write(dashboardEventCSVRecord(event))
	})
	csvWriter.Flush()
	if err == nil {
		err = csvWriter.Error()
	}
	return exported, err
}

func encodeDashboardEventsPaged(opts dashboardEventsExportOptions, firstPage EventsResult, consume func(RequestDetail) error) (int, error) {
	page := firstPage
	exported := 0
	for {
		for _, event := range page.Events {
			if opts.ctx != nil && opts.ctx.Err() != nil {
				return exported, opts.ctx.Err()
			}
			if err := consume(event); err != nil {
				return exported, err
			}
			exported++
		}
		if exported >= page.Limit || len(page.Events) == 0 {
			return exported, nil
		}
		page = opts.frozen.page(exported, dashboardExportJobPageSize)
	}
}

// Capture values once under the statistics lock. Mutable indexes, late usage,
// retention and repricing can no longer shift an export between pages.
type eventExportSnapshot struct{ result EventsResult }

func (s *RequestStatistics) captureEventExport(params EventsQuery, limit int, at time.Time) *eventExportSnapshot {
	result := s.QueryExportEventsPage(params, 0, math.MaxInt, limit, at, nil)
	// QueryExportEventsPage already deep-clones records under the statistics
	// lock and freezes their costs. A second clone doubles snapshot allocations.
	return &eventExportSnapshot{result: result}
}

func (s *eventExportSnapshot) page(offset, size int) EventsResult {
	result := s.result
	if offset < 0 {
		offset = 0
	}
	if offset > len(result.Events) {
		offset = len(result.Events)
	}
	if size < 0 {
		size = 0
	}
	end := offset + min(size, len(result.Events)-offset)
	result.Events = result.Events[offset:end]
	result.Offset = offset
	return result
}

type exportContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w *exportContextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}

func (m *dashboardExportJobManager) close() {
	m.mu.Lock()
	m.closed = true
	for id, job := range m.jobs {
		if job.cancel != nil {
			job.cancel()
		}
		delete(m.jobs, id)
		_ = os.Remove(job.FilePath + ".tmp")
		_ = os.Remove(job.FilePath)
	}
	m.mu.Unlock()
	m.wg.Wait()
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
	if job.cancel != nil {
		job.cancel()
	}
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

func (m *dashboardExportJobManager) activeLocked() int {
	active := 0
	for _, job := range m.jobs {
		if job.Status == dashboardExportJobQueued || job.Status == dashboardExportJobRunning {
			active++
		}
	}
	if m.workers > active {
		return m.workers
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
		response.ETag = job.ETag
		response.ChunkSize = dashboardExportChunkBytes
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

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += n
	return n, err
}

func fileSHA256(path string) ([32]byte, error) {
	var sum [32]byte
	file, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return sum, err
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}
