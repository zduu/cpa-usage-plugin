package main

import (
	"bufio"
	"container/heap"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Statistics Engine
// ============================================================================

type RequestStatistics struct {
	mu sync.RWMutex

	maxDetailsPerModel int
	retention          time.Duration
	dedupWindow        time.Duration
	seen               map[string]time.Time

	totalRequests   int64
	successCount    int64
	failureCount    int64
	totalTokens     int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	latencySum      int64
	latencyN        int64
	startedAt       time.Time
	lastRecordedAt  time.Time

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
	healthBuckets  map[int64]healthBucket

	modelSummaryStats map[string]*ModelStat
	sourceStats       map[string]*sourceStatAccumulator
	credentialStats   map[string]*CredentialStat
	clientAPIStats    map[string]*clientAPIStatAccumulator

	logResponseHeaders headerWhitelist
	storageEnabled     bool
	storagePath        string
	storageFlush       time.Duration
	storageFile        *os.File
	storageWriter      *bufio.Writer
	storageDir         string
	storageLegacyPath  string
	storageLoadedPath  string
	storageActiveDate  string
	storageLastFlush   time.Time
	storageLastError   string
	storageBuffered    int64

	priceStoragePath       string
	priceStorageLoadedPath string
	priceStorageLastError  string
	modelPrices            map[string]ModelPrice
	modelPricesUpdatedAt   time.Time

	lastImportResult *ImportResponse
	evictedTotal     int64

	summaryVersion      uint64
	summaryCacheValid   bool
	summaryCache        DashboardSummary
	summaryCacheVersion uint64
	summaryCacheWindow  time.Time

	eventQueryCache      map[dashboardEventCacheKey]EventsResult
	eventQueryCacheOrder []dashboardEventCacheKey
	eventIndexVersion    uint64
	eventIndex           []dashboardEventDetail
	eventAPIIndex        map[string][]dashboardEventDetail
	eventModelIndex      map[string][]dashboardEventDetail
	eventSourceIndex     map[string][]dashboardEventDetail
	eventAuthIndex       map[string][]dashboardEventDetail
}

type apiStats struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	Models          map[string]*modelStats
}

type modelStats struct {
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	latencySum      int64
	latencyN        int64
	Details         []RequestDetail
}

type detailTotals struct {
	totalTokens     int64
	inputTokens     int64
	outputTokens    int64
	cachedTokens    int64
	reasoningTokens int64
	latencySum      int64
	latencyN        int64
}

type healthBucket struct {
	success int64
	failure int64
}

type sourceStatAccumulator struct {
	stat      SourceStat
	providers map[string]int64
}

type clientAPIStatAccumulator struct {
	stat   ClientAPIStat
	models map[string]*ClientAPIModelStat
}

type dashboardEventCacheKey struct {
	limit      int
	offset     int
	timeBucket int64
	rangeKey   string
	model      string
	source     string
	authIndex  string
	api        string
}

// apiKeySalt is a per-process random salt used to produce stable grouping IDs.
var apiKeySalt string

// hourKeys pre-computes "00" through "23" so Snapshot never allocates strings.
var hourKeys = [24]string{
	"00", "01", "02", "03", "04", "05", "06", "07",
	"08", "09", "10", "11", "12", "13", "14", "15",
	"16", "17", "18", "19", "20", "21", "22", "23",
}

const (
	dashboardHealthSlotCount = 672
	dashboardHealthStep      = 15 * time.Minute
	dashboardEventCacheMax   = 16
)

func init() {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
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
		healthBuckets:      make(map[int64]healthBucket),
		modelSummaryStats:  make(map[string]*ModelStat),
		sourceStats:        make(map[string]*sourceStatAccumulator),
		credentialStats:    make(map[string]*CredentialStat),
		clientAPIStats:     make(map[string]*clientAPIStatAccumulator),
		storagePath:        defaultRuntimeConfig().StoragePath,
		storageFlush:       time.Duration(defaultStorageFlushSeconds) * time.Second,
		priceStoragePath:   defaultRuntimeConfig().PriceStoragePath,
		modelPrices:        make(map[string]ModelPrice),
		startedAt:          time.Now(),
	}
}

func (s *RequestStatistics) Configure(cfg runtimeConfig) {
	s.ConfigurePatch(runtimeConfigPatch{
		MaxDetailsPerModel:  positiveIntPtr(cfg.MaxDetailsPerModel),
		RetentionDays:       intPtr(cfg.RetentionDays),
		DedupWindowMinutes:  intPtr(cfg.DedupWindowMinutes),
		LogResponseHeaders:  stringPtr(cfg.LogResponseHeaders),
		APIKeyHashSalt:      stringPtr(cfg.APIKeyHashSalt),
		StorageEnabled:      boolPtr(cfg.StorageEnabled),
		StoragePath:         stringPtr(cfg.StoragePath),
		StorageFlushSeconds: positiveIntPtr(cfg.StorageFlushSeconds),
		PriceStoragePath:    stringPtr(cfg.PriceStoragePath),
		UpdateEnabled:       boolPtr(cfg.UpdateEnabled),
		UpdateVersion:       stringPtr(cfg.UpdateVersion),
	})
}

func (s *RequestStatistics) ConfigurePatch(cfg runtimeConfigPatch) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.MaxDetailsPerModel != nil && *cfg.MaxDetailsPerModel >= 0 {
		s.maxDetailsPerModel = *cfg.MaxDetailsPerModel
	}
	if cfg.RetentionDays != nil && *cfg.RetentionDays >= 0 {
		s.retention = time.Duration(*cfg.RetentionDays) * 24 * time.Hour
	}
	if cfg.DedupWindowMinutes != nil && *cfg.DedupWindowMinutes >= 0 {
		s.dedupWindow = time.Duration(*cfg.DedupWindowMinutes) * time.Minute
	}
	if cfg.LogResponseHeaders != nil {
		s.logResponseHeaders = parseHeaderWhitelist(*cfg.LogResponseHeaders)
	}
	if cfg.APIKeyHashSalt != nil && strings.TrimSpace(*cfg.APIKeyHashSalt) != "" {
		apiKeySalt = strings.TrimSpace(*cfg.APIKeyHashSalt)
	}
	if cfg.StoragePath != nil && strings.TrimSpace(*cfg.StoragePath) != "" {
		s.storagePath = strings.TrimSpace(*cfg.StoragePath)
	}
	if cfg.StorageFlushSeconds != nil && *cfg.StorageFlushSeconds > 0 {
		s.storageFlush = time.Duration(*cfg.StorageFlushSeconds) * time.Second
	}
	if cfg.PriceStoragePath != nil && strings.TrimSpace(*cfg.PriceStoragePath) != "" {
		s.priceStoragePath = strings.TrimSpace(*cfg.PriceStoragePath)
	}
	if cfg.StorageEnabled != nil {
		s.storageEnabled = *cfg.StorageEnabled
	}
	s.configureStorageLocked()
	s.loadModelPricesLocked()
	s.pruneLocked(time.Now(), true)
	s.rebuildAggregatesLocked()
	s.rebuildSeenLocked(time.Now())
	s.invalidateSummaryLocked()
}

func intPtr(value int) *int {
	return &value
}

func positiveIntPtr(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func (s *RequestStatistics) invalidateSummaryLocked() {
	if s == nil {
		return
	}
	s.summaryVersion++
	s.summaryCacheValid = false
	s.eventQueryCache = nil
	s.eventQueryCacheOrder = nil
	s.eventIndexVersion = 0
	s.eventIndex = nil
	s.eventAPIIndex = nil
	s.eventModelIndex = nil
	s.eventSourceIndex = nil
	s.eventAuthIndex = nil
}

func (s *RequestStatistics) Record(record UsageRecord) {
	if s == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	totalTokens := usageDetailTotalTokens(record.Detail)

	statsKey := usageGroupKey(record)

	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	detail := RequestDetail{
		Model:      modelName,
		Timestamp:  timestamp,
		LatencyMs:  record.Latency.Milliseconds(),
		TTFTMs:     record.TTFT.Milliseconds(),
		APIKey:     maskAPIKey(record.APIKey),
		APIKeyHash: hashAPIKey(record.APIKey),
		Source:     usageSource(record),
		Provider:   strings.TrimSpace(record.Provider),
		AuthID:     strings.TrimSpace(record.AuthID),
		AuthIndex:  strings.TrimSpace(record.AuthIndex),
		AuthType:   strings.TrimSpace(record.AuthType),
		BaseURL:    strings.TrimSpace(record.BaseURL),
		Thinking:   usageThinking(record),
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
	if s.recordDetailLocked(statsKey, modelName, detail, dedup, now, true) {
		s.appendDetailLocked(persistedDetail{API: statsKey, Model: modelName, Detail: detail})
		s.pruneLocked(now, false)
		s.pruneSeenLocked(now)
	}
}

type persistedDetail struct {
	API    string        `json:"api"`
	Model  string        `json:"model"`
	Detail RequestDetail `json:"detail"`
}

type persistedStorageSnapshot struct {
	Version     int                `json:"version"`
	GeneratedAt string             `json:"generated_at"`
	Usage       StatisticsSnapshot `json:"usage"`
}

func (s *RequestStatistics) recordDetailLocked(apiName, modelName string, detail RequestDetail, dedup string, now time.Time, useDedupWindow bool) bool {
	if s == nil {
		return false
	}
	apiName = usageGroupKeyFromDetail(apiName, detail)
	if strings.TrimSpace(apiName) == "" {
		apiName = "未知接口"
	}
	dedup = dedupKey(apiName, modelName, detail)
	s.pruneSeenLocked(now)
	if useDedupWindow && s.dedupWindow > 0 {
		if _, exists := s.seen[dedup]; exists {
			return false
		}
		s.seen[dedup] = now
	}

	apiSt, ok := s.apis[apiName]
	if !ok {
		apiSt = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[apiName] = apiSt
	}

	totals := s.updateAPIStats(apiSt, modelName, detail)

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totals.totalTokens
	s.inputTokens += totals.inputTokens
	s.outputTokens += totals.outputTokens
	s.cachedTokens += totals.cachedTokens
	s.reasoningTokens += totals.reasoningTokens
	s.latencySum += totals.latencySum
	s.latencyN += totals.latencyN
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()
	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totals.totalTokens
	s.tokensByHour[hourKey] += totals.totalTokens
	s.incrementModelSummaryStatsLocked(modelName, detail, totals)
	s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
	s.incrementHealthBucketLocked(detail)
	if detail.Timestamp.After(s.lastRecordedAt) {
		s.lastRecordedAt = detail.Timestamp
	}
	s.invalidateSummaryLocked()
	return true
}

func (s *RequestStatistics) configureStorageLocked() {
	if s == nil {
		return
	}
	if !s.storageEnabled {
		s.closeStorageLocked()
		return
	}
	path := strings.TrimSpace(s.storagePath)
	if path == "" {
		path = defaultRuntimeConfig().StoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.storageLastError = err.Error()
		return
	}
	dir, legacyPath := storageLayout(abs)
	if s.storageFile != nil && s.storageDir == dir && s.storageLegacyPath == legacyPath {
		if err := s.cleanupStorageFilesLocked(time.Now()); err != nil {
			s.storageLastError = err.Error()
		}
		return
	}
	s.closeStorageLocked()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.storageLastError = err.Error()
		return
	}
	now := time.Now()
	var warnings []string
	snapshotAt, err := s.loadStorageSnapshotLocked(dir, now)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	if err := s.replayStorageFilesLocked(dir, legacyPath, now, snapshotAt); err != nil {
		warnings = append(warnings, err.Error())
	}
	s.storagePath = path
	s.storageDir = dir
	s.storageLegacyPath = legacyPath
	if err := s.openStorageFileLocked(now); err != nil {
		s.storageLastError = err.Error()
		return
	}
	if err := s.cleanupStorageFilesLocked(now); err != nil {
		warnings = append(warnings, err.Error())
	}
	if err := combineStorageWarnings(warnings); err != nil {
		s.storageLastError = err.Error()
	} else {
		s.storageLastError = ""
	}
}

func storageLayout(absPath string) (string, string) {
	if strings.EqualFold(filepath.Ext(absPath), ".jsonl") {
		return strings.TrimSuffix(absPath, filepath.Ext(absPath)), absPath
	}
	return absPath, ""
}

func storageDate(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01-02")
}

func storageFileName(date string) string {
	return "usage-" + date + ".jsonl"
}

func storageSnapshotPath(dir string) string {
	return filepath.Join(dir, "snapshot.json")
}

func parseStorageFileDate(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "usage-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, false
	}
	dateText := strings.TrimSuffix(strings.TrimPrefix(name, "usage-"), ".jsonl")
	t, err := time.Parse("2006-01-02", dateText)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (s *RequestStatistics) openStorageFileLocked(now time.Time) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(s.storageDir) == "" {
		return errors.New("storage directory is not configured")
	}
	date := storageDate(now)
	path := filepath.Join(s.storageDir, storageFileName(date))
	if s.storageFile != nil && s.storageLoadedPath == path {
		return nil
	}
	s.closeStorageLocked()
	if err := os.MkdirAll(s.storageDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.storageFile = file
	s.storageWriter = bufio.NewWriter(file)
	s.storageLoadedPath = path
	s.storageActiveDate = date
	return nil
}

func (s *RequestStatistics) loadStorageSnapshotLocked(dir string, now time.Time) (time.Time, error) {
	raw, err := os.ReadFile(storageSnapshotPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	var persisted persistedStorageSnapshot
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return time.Time{}, fmt.Errorf("load storage snapshot: %w", err)
	}
	s.mergeSnapshotLocked(persisted.Usage, false, now)
	generatedAt, err := time.Parse(time.RFC3339, persisted.GeneratedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse storage snapshot time: %w", err)
	}
	return generatedAt, nil
}

func (s *RequestStatistics) writeStorageSnapshotLocked(now time.Time) error {
	if s == nil || strings.TrimSpace(s.storageDir) == "" {
		return nil
	}
	if err := os.MkdirAll(s.storageDir, 0o755); err != nil {
		return err
	}
	payload := persistedStorageSnapshot{
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Usage:       s.snapshotLocked(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target := storageSnapshotPath(s.storageDir)
	tmp := target + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = syncDir(s.storageDir)
	return nil
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (s *RequestStatistics) replayStorageFilesLocked(dir string, legacyPath string, now time.Time, snapshotAt time.Time) error {
	var warnings []string
	seenFiles := make(map[string]struct{})
	if strings.TrimSpace(legacyPath) != "" && snapshotAt.IsZero() {
		if err := s.replayStorageLocked(legacyPath); err != nil {
			warnings = append(warnings, err.Error())
		}
		seenFiles[legacyPath] = struct{}{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return combineStorageWarnings(warnings)
		}
		return err
	}
	var files []string
	cutoff := time.Time{}
	if s.retention > 0 {
		cutoff = now.Add(-s.retention).UTC().Truncate(24 * time.Hour)
	}
	snapshotDay := time.Time{}
	if !snapshotAt.IsZero() {
		snapshotDay = snapshotAt.UTC().Truncate(24 * time.Hour)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileDate, ok := parseStorageFileDate(entry.Name())
		if !ok {
			continue
		}
		if !cutoff.IsZero() && fileDate.Before(cutoff) {
			continue
		}
		if !snapshotDay.IsZero() && fileDate.Before(snapshotDay) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, ok := seenFiles[path]; ok {
			continue
		}
		files = append(files, path)
	}
	sort.Strings(files)
	for _, path := range files {
		if err := s.replayStorageLocked(path); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return combineStorageWarnings(warnings)
}

func combineStorageWarnings(warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	return errors.New(strings.Join(warnings, "; "))
}

func (s *RequestStatistics) cleanupStorageFilesLocked(now time.Time) error {
	if s == nil || s.retention <= 0 || strings.TrimSpace(s.storageDir) == "" {
		return nil
	}
	cutoff := now.Add(-s.retention).UTC().Truncate(24 * time.Hour)
	entries, err := os.ReadDir(s.storageDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileDate, ok := parseStorageFileDate(entry.Name())
		if !ok || !fileDate.Before(cutoff) {
			continue
		}
		path := filepath.Join(s.storageDir, entry.Name())
		if path == s.storageLoadedPath {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *RequestStatistics) replayStorageLocked(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	now := time.Now()
	existing := s.detailKeysLocked()
	var invalidLines int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var persisted persistedDetail
		if err := json.Unmarshal([]byte(line), &persisted); err != nil {
			invalidLines++
			continue
		}
		apiName := strings.TrimSpace(persisted.API)
		if apiName == "" {
			invalidLines++
			continue
		}
		modelName := normalizeModelName(persisted.Model)
		detail := persisted.Detail
		if detail.Model == "" {
			detail.Model = modelName
		}
		if detail.Timestamp.IsZero() {
			detail.Timestamp = now
		}
		detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
		apiName = usageGroupKeyFromDetail(apiName, detail)
		key := dedupKey(apiName, modelName, detail)
		if _, ok := existing[key]; ok {
			continue
		}
		if s.recordDetailLocked(apiName, modelName, detail, key, now, true) {
			existing[key] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan storage: %w", err)
	}
	if invalidLines > 0 {
		return fmt.Errorf("replay storage skipped %d invalid line(s)", invalidLines)
	}
	return nil
}

func (s *RequestStatistics) detailKeysLocked() map[string]struct{} {
	keys := make(map[string]struct{})
	for apiName, apiSt := range s.apis {
		if apiSt == nil {
			continue
		}
		for modelName, modelSt := range apiSt.Models {
			if modelSt == nil {
				continue
			}
			for _, detail := range modelSt.Details {
				keys[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}
	return keys
}

func (s *RequestStatistics) appendDetailLocked(detail persistedDetail) {
	if s == nil || !s.storageEnabled {
		return
	}
	if err := s.openStorageFileLocked(time.Now()); err != nil {
		s.storageLastError = err.Error()
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		s.storageLastError = err.Error()
		return
	}
	if _, err := s.storageWriter.Write(raw); err != nil {
		s.storageLastError = err.Error()
		return
	}
	if err := s.storageWriter.WriteByte('\n'); err != nil {
		s.storageLastError = err.Error()
		return
	}
	s.storageBuffered++
	if s.storageFlush <= 0 || time.Since(s.storageLastFlush) >= s.storageFlush {
		if err := s.storageWriter.Flush(); err != nil {
			s.storageLastError = err.Error()
			return
		}
		s.storageBuffered = 0
		s.storageLastFlush = time.Now()
	}
}

func (s *RequestStatistics) closeStorageLocked() {
	if s == nil {
		return
	}
	flushed := false
	synced := true
	if s.storageWriter != nil {
		if err := s.storageWriter.Flush(); err != nil {
			s.storageLastError = err.Error()
		} else {
			flushed = true
			s.storageBuffered = 0
		}
		s.storageWriter = nil
	}
	if s.storageFile != nil {
		if err := s.storageFile.Sync(); err != nil {
			s.storageLastError = err.Error()
			synced = false
		}
		if err := s.storageFile.Close(); err != nil {
			s.storageLastError = err.Error()
		}
		s.storageFile = nil
	}
	s.storageLoadedPath = ""
	s.storageActiveDate = ""
	if flushed && synced {
		s.storageLastFlush = time.Now()
	}
	if s.storageEnabled && strings.TrimSpace(s.storageDir) != "" {
		if err := s.writeStorageSnapshotLocked(time.Now()); err != nil {
			s.storageLastError = err.Error()
		}
	}
}

func (s *RequestStatistics) loadModelPricesLocked() {
	if s == nil {
		return
	}
	path := strings.TrimSpace(s.priceStoragePath)
	if path == "" {
		path = defaultRuntimeConfig().PriceStoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return
	}
	if s.priceStorageLoadedPath == abs {
		return
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.priceStoragePath = path
			s.priceStorageLoadedPath = abs
			s.modelPrices = make(map[string]ModelPrice)
			s.modelPricesUpdatedAt = time.Time{}
			s.priceStorageLastError = ""
			return
		}
		s.priceStorageLastError = err.Error()
		return
	}
	var persisted struct {
		UpdatedAt string                `json:"updated_at"`
		Prices    map[string]ModelPrice `json:"prices"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		s.priceStorageLastError = err.Error()
		return
	}
	prices := make(map[string]ModelPrice, len(persisted.Prices))
	for model, price := range persisted.Prices {
		name := strings.TrimSpace(model)
		if name == "" || !validModelPrice(price) {
			continue
		}
		prices[name] = price
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPrices = prices
	s.modelPricesUpdatedAt = parseRFC3339OrZero(persisted.UpdatedAt)
	s.priceStorageLastError = ""
}

func (s *RequestStatistics) saveModelPricesLocked() error {
	if s == nil {
		return nil
	}
	path := strings.TrimSpace(s.priceStoragePath)
	if path == "" {
		path = defaultRuntimeConfig().PriceStoragePath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	updatedAt := time.Now().UTC()
	payload := struct {
		UpdatedAt string                `json:"updated_at"`
		Prices    map[string]ModelPrice `json:"prices"`
	}{
		UpdatedAt: updatedAt.Format(time.RFC3339),
		Prices:    copyModelPrices(s.modelPrices),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		s.priceStorageLastError = err.Error()
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		s.priceStorageLastError = err.Error()
		return err
	}
	s.priceStoragePath = path
	s.priceStorageLoadedPath = abs
	s.modelPricesUpdatedAt = updatedAt
	s.priceStorageLastError = ""
	return nil
}

func parseRFC3339OrZero(value string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return t
}

func validModelPrice(price ModelPrice) bool {
	return validPriceNumber(price.Prompt) && validPriceNumber(price.Completion) && validPriceNumber(price.Cache)
}

func validPriceNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func copyModelPrices(source map[string]ModelPrice) map[string]ModelPrice {
	copy := make(map[string]ModelPrice, len(source))
	for model, price := range source {
		copy[model] = price
	}
	return copy
}

func (s *RequestStatistics) ModelPrices() ModelPricesResponse {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	return s.modelPricesResponseLocked()
}

func (s *RequestStatistics) UpsertModelPrice(model string, price ModelPrice) (ModelPricesResponse, error) {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}, nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPricesResponse{}, errors.New("model is required")
	}
	if !validModelPrice(price) {
		return ModelPricesResponse{}, errors.New("price values must be non-negative finite numbers")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	if s.modelPrices == nil {
		s.modelPrices = make(map[string]ModelPrice)
	}
	s.modelPrices[model] = price
	if err := s.saveModelPricesLocked(); err != nil {
		return ModelPricesResponse{}, err
	}
	return s.modelPricesResponseLocked(), nil
}

func (s *RequestStatistics) DeleteModelPrice(model string) (ModelPricesResponse, error) {
	if s == nil {
		return ModelPricesResponse{Prices: map[string]ModelPrice{}}, nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelPricesResponse{}, errors.New("model is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadModelPricesLocked()
	if s.modelPrices == nil {
		s.modelPrices = make(map[string]ModelPrice)
	}
	delete(s.modelPrices, model)
	if err := s.saveModelPricesLocked(); err != nil {
		return ModelPricesResponse{}, err
	}
	return s.modelPricesResponseLocked(), nil
}

func (s *RequestStatistics) modelPricesResponseLocked() ModelPricesResponse {
	response := ModelPricesResponse{
		Prices: copyModelPrices(s.modelPrices),
		Storage: ModelPriceStorageStatus{
			Path:       s.priceStoragePath,
			LoadedPath: s.priceStorageLoadedPath,
			LastError:  s.priceStorageLastError,
		},
	}
	if !s.modelPricesUpdatedAt.IsZero() {
		response.UpdatedAt = s.modelPricesUpdatedAt.UTC().Format(time.RFC3339)
	}
	return response
}

func (s *RequestStatistics) updateAPIStats(apiSt *apiStats, model string, detail RequestDetail) detailTotals {
	totals := detailTotalsFromRequest(detail)
	apiSt.TotalRequests++
	if detail.Failed {
		apiSt.FailureCount++
	} else {
		apiSt.SuccessCount++
	}
	apiSt.TotalTokens += totals.totalTokens
	apiSt.InputTokens += totals.inputTokens
	apiSt.OutputTokens += totals.outputTokens
	apiSt.CachedTokens += totals.cachedTokens
	apiSt.ReasoningTokens += totals.reasoningTokens
	apiSt.latencySum += totals.latencySum
	apiSt.latencyN += totals.latencyN

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
	modelSt.TotalTokens += totals.totalTokens
	modelSt.InputTokens += totals.inputTokens
	modelSt.OutputTokens += totals.outputTokens
	modelSt.CachedTokens += totals.cachedTokens
	modelSt.ReasoningTokens += totals.reasoningTokens
	modelSt.latencySum += totals.latencySum
	modelSt.latencyN += totals.latencyN
	modelSt.Details = append(modelSt.Details, detail)
	return totals
}

func (s *RequestStatistics) incrementModelSummaryStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if s.modelSummaryStats == nil {
		s.modelSummaryStats = make(map[string]*ModelStat)
	}
	modelStat, ok := s.modelSummaryStats[modelName]
	if !ok {
		modelStat = &ModelStat{Model: modelName}
		s.modelSummaryStats[modelName] = modelStat
	}
	modelStat.TotalRequests++
	if detail.Failed {
		modelStat.FailureCount++
	} else {
		modelStat.SuccessCount++
	}
	modelStat.TotalTokens += totals.totalTokens
	modelStat.InputTokens += totals.inputTokens
	modelStat.OutputTokens += totals.outputTokens
	modelStat.CachedTokens += totals.cachedTokens
	modelStat.ReasoningTokens += totals.reasoningTokens
	modelStat.latencySum += totals.latencySum
	modelStat.latencyN += totals.latencyN
}

func (s *RequestStatistics) decrementModelSummaryStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	modelStat, ok := s.modelSummaryStats[modelName]
	if !ok {
		return
	}
	modelStat.TotalRequests--
	if detail.Failed {
		modelStat.FailureCount--
	} else {
		modelStat.SuccessCount--
	}
	modelStat.TotalTokens -= totals.totalTokens
	modelStat.InputTokens -= totals.inputTokens
	modelStat.OutputTokens -= totals.outputTokens
	modelStat.CachedTokens -= totals.cachedTokens
	modelStat.ReasoningTokens -= totals.reasoningTokens
	modelStat.latencySum -= totals.latencySum
	modelStat.latencyN -= totals.latencyN
	if modelStat.TotalRequests <= 0 {
		delete(s.modelSummaryStats, modelName)
	}
}

func (s *RequestStatistics) incrementSummaryDimensionStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if s.sourceStats == nil {
		s.sourceStats = make(map[string]*sourceStatAccumulator)
	}
	if s.credentialStats == nil {
		s.credentialStats = make(map[string]*CredentialStat)
	}
	if s.clientAPIStats == nil {
		s.clientAPIStats = make(map[string]*clientAPIStatAccumulator)
	}

	source := summarySourceKey(detail)
	sourceAgg, ok := s.sourceStats[source]
	if !ok {
		sourceAgg = &sourceStatAccumulator{
			stat:      SourceStat{Source: source, Provider: detail.Provider},
			providers: make(map[string]int64),
		}
		s.sourceStats[source] = sourceAgg
	}
	if sourceAgg.stat.Provider == "" {
		sourceAgg.stat.Provider = detail.Provider
	}
	sourceAgg.providers[detail.Provider]++
	sourceAgg.stat.TotalRequests++
	if detail.Failed {
		sourceAgg.stat.FailureCount++
	} else {
		sourceAgg.stat.SuccessCount++
	}
	sourceAgg.stat.TotalTokens += totals.totalTokens

	credential := summaryCredentialKey(detail)
	credentialStat, ok := s.credentialStats[credential]
	if !ok {
		credentialStat = &CredentialStat{AuthIndex: credential}
		s.credentialStats[credential] = credentialStat
	}
	credentialStat.TotalRequests++
	if detail.Failed {
		credentialStat.FailureCount++
	} else {
		credentialStat.SuccessCount++
	}
	credentialStat.TotalTokens += totals.totalTokens

	clientKey := clientAPIGroupKey(detail)
	clientAgg, ok := s.clientAPIStats[clientKey]
	if !ok {
		clientAgg = &clientAPIStatAccumulator{
			stat: ClientAPIStat{
				APIKey:     clientAPIGroupLabel(detail),
				APIKeyHash: detail.APIKeyHash,
			},
			models: make(map[string]*ClientAPIModelStat),
		}
		s.clientAPIStats[clientKey] = clientAgg
	}
	clientAgg.stat.TotalRequests++
	if detail.Failed {
		clientAgg.stat.FailureCount++
	} else {
		clientAgg.stat.SuccessCount++
	}
	clientAgg.stat.TotalTokens += totals.totalTokens
	clientAgg.stat.InputTokens += totals.inputTokens
	clientAgg.stat.OutputTokens += totals.outputTokens
	clientAgg.stat.CachedTokens += totals.cachedTokens
	clientAgg.stat.ReasoningTokens += totals.reasoningTokens

	clientModel, ok := clientAgg.models[modelName]
	if !ok {
		clientModel = &ClientAPIModelStat{Model: modelName}
		clientAgg.models[modelName] = clientModel
	}
	clientModel.TotalRequests++
	if detail.Failed {
		clientModel.FailureCount++
	} else {
		clientModel.SuccessCount++
	}
	clientModel.TotalTokens += totals.totalTokens
	clientModel.InputTokens += totals.inputTokens
	clientModel.OutputTokens += totals.outputTokens
	clientModel.CachedTokens += totals.cachedTokens
	clientModel.ReasoningTokens += totals.reasoningTokens
}

func (s *RequestStatistics) decrementSummaryDimensionStatsLocked(modelName string, detail RequestDetail, totals detailTotals) {
	if sourceAgg, ok := s.sourceStats[summarySourceKey(detail)]; ok {
		sourceAgg.stat.TotalRequests--
		if detail.Failed {
			sourceAgg.stat.FailureCount--
		} else {
			sourceAgg.stat.SuccessCount--
		}
		sourceAgg.stat.TotalTokens -= totals.totalTokens
		if sourceAgg.providers != nil {
			sourceAgg.providers[detail.Provider]--
			if sourceAgg.providers[detail.Provider] <= 0 {
				delete(sourceAgg.providers, detail.Provider)
			}
			if sourceAgg.stat.Provider == detail.Provider {
				sourceAgg.stat.Provider = ""
				for provider := range sourceAgg.providers {
					sourceAgg.stat.Provider = provider
					break
				}
			}
		}
		if sourceAgg.stat.TotalRequests <= 0 {
			delete(s.sourceStats, summarySourceKey(detail))
		}
	}

	if credentialStat, ok := s.credentialStats[summaryCredentialKey(detail)]; ok {
		credentialStat.TotalRequests--
		if detail.Failed {
			credentialStat.FailureCount--
		} else {
			credentialStat.SuccessCount--
		}
		credentialStat.TotalTokens -= totals.totalTokens
		if credentialStat.TotalRequests <= 0 {
			delete(s.credentialStats, summaryCredentialKey(detail))
		}
	}

	clientKey := clientAPIGroupKey(detail)
	if clientAgg, ok := s.clientAPIStats[clientKey]; ok {
		clientAgg.stat.TotalRequests--
		if detail.Failed {
			clientAgg.stat.FailureCount--
		} else {
			clientAgg.stat.SuccessCount--
		}
		clientAgg.stat.TotalTokens -= totals.totalTokens
		clientAgg.stat.InputTokens -= totals.inputTokens
		clientAgg.stat.OutputTokens -= totals.outputTokens
		clientAgg.stat.CachedTokens -= totals.cachedTokens
		clientAgg.stat.ReasoningTokens -= totals.reasoningTokens

		if clientModel, ok := clientAgg.models[modelName]; ok {
			clientModel.TotalRequests--
			if detail.Failed {
				clientModel.FailureCount--
			} else {
				clientModel.SuccessCount--
			}
			clientModel.TotalTokens -= totals.totalTokens
			clientModel.InputTokens -= totals.inputTokens
			clientModel.OutputTokens -= totals.outputTokens
			clientModel.CachedTokens -= totals.cachedTokens
			clientModel.ReasoningTokens -= totals.reasoningTokens
			if clientModel.TotalRequests <= 0 {
				delete(clientAgg.models, modelName)
			}
		}
		if clientAgg.stat.TotalRequests <= 0 {
			delete(s.clientAPIStats, clientKey)
		}
	}
}

func (s *RequestStatistics) incrementHealthBucketLocked(detail RequestDetail) {
	key, ok := healthBucketKey(detail.Timestamp)
	if !ok {
		return
	}
	if s.healthBuckets == nil {
		s.healthBuckets = make(map[int64]healthBucket)
	}
	bucket := s.healthBuckets[key]
	if detail.Failed {
		bucket.failure++
	} else {
		bucket.success++
	}
	s.healthBuckets[key] = bucket
}

func (s *RequestStatistics) decrementHealthBucketLocked(detail RequestDetail) {
	key, ok := healthBucketKey(detail.Timestamp)
	if !ok || s.healthBuckets == nil {
		return
	}
	bucket, ok := s.healthBuckets[key]
	if !ok {
		return
	}
	if detail.Failed {
		bucket.failure--
	} else {
		bucket.success--
	}
	if bucket.success <= 0 && bucket.failure <= 0 {
		delete(s.healthBuckets, key)
		return
	}
	s.healthBuckets[key] = bucket
}

func (s *RequestStatistics) pruneLocked(now time.Time, sortNeeded bool) {
	if s == nil {
		return
	}
	changed := false
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
			if !cutoff.IsZero() {
				kept := details[:0]
				for _, d := range details {
					if d.Timestamp.IsZero() || !d.Timestamp.Before(cutoff) {
						kept = append(kept, d)
					} else {
						s.decrementCounters(d, apiSt, modelSt, modelName)
						s.evictedTotal++
						changed = true
					}
				}
				details = kept
			}
			if sortNeeded {
				sort.SliceStable(details, func(i, j int) bool {
					return details[i].Timestamp.Before(details[j].Timestamp)
				})
			}
			if s.maxDetailsPerModel >= 0 && len(details) > s.maxDetailsPerModel {
				keep := s.maxDetailsPerModel
				removed := details[:len(details)-keep]
				for _, d := range removed {
					s.decrementCounters(d, apiSt, modelSt, modelName)
					s.evictedTotal++
					changed = true
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
	if changed {
		s.invalidateSummaryLocked()
	}
}

func (s *RequestStatistics) decrementCounters(d RequestDetail, apiSt *apiStats, modelSt *modelStats, modelName string) {
	totals := detailTotalsFromRequest(d)
	s.totalRequests--
	if d.Failed {
		s.failureCount--
	} else {
		s.successCount--
	}
	s.totalTokens -= totals.totalTokens
	s.inputTokens -= totals.inputTokens
	s.outputTokens -= totals.outputTokens
	s.cachedTokens -= totals.cachedTokens
	s.reasoningTokens -= totals.reasoningTokens
	s.latencySum -= totals.latencySum
	s.latencyN -= totals.latencyN

	apiSt.TotalRequests--
	if d.Failed {
		apiSt.FailureCount--
	} else {
		apiSt.SuccessCount--
	}
	apiSt.TotalTokens -= totals.totalTokens
	apiSt.InputTokens -= totals.inputTokens
	apiSt.OutputTokens -= totals.outputTokens
	apiSt.CachedTokens -= totals.cachedTokens
	apiSt.ReasoningTokens -= totals.reasoningTokens
	apiSt.latencySum -= totals.latencySum
	apiSt.latencyN -= totals.latencyN

	modelSt.TotalRequests--
	if d.Failed {
		modelSt.FailureCount--
	} else {
		modelSt.SuccessCount--
	}
	modelSt.TotalTokens -= totals.totalTokens
	modelSt.InputTokens -= totals.inputTokens
	modelSt.OutputTokens -= totals.outputTokens
	modelSt.CachedTokens -= totals.cachedTokens
	modelSt.ReasoningTokens -= totals.reasoningTokens
	modelSt.latencySum -= totals.latencySum
	modelSt.latencyN -= totals.latencyN

	dayKey := d.Timestamp.Format("2006-01-02")
	hourKey := d.Timestamp.Hour()
	s.requestsByDay[dayKey]--
	s.requestsByHour[hourKey]--
	s.tokensByDay[dayKey] -= totals.totalTokens
	s.tokensByHour[hourKey] -= totals.totalTokens
	s.decrementModelSummaryStatsLocked(modelName, d, totals)
	s.decrementSummaryDimensionStatsLocked(modelName, d, totals)
	s.decrementHealthBucketLocked(d)
}

func (s *RequestStatistics) rebuildAggregatesLocked() {
	if s == nil {
		return
	}
	s.totalRequests = 0
	s.successCount = 0
	s.failureCount = 0
	s.totalTokens = 0
	s.inputTokens = 0
	s.outputTokens = 0
	s.cachedTokens = 0
	s.reasoningTokens = 0
	s.latencySum = 0
	s.latencyN = 0
	s.requestsByDay = make(map[string]int64)
	s.requestsByHour = make(map[int]int64)
	s.tokensByDay = make(map[string]int64)
	s.tokensByHour = make(map[int]int64)
	s.healthBuckets = make(map[int64]healthBucket)
	s.modelSummaryStats = make(map[string]*ModelStat)
	s.sourceStats = make(map[string]*sourceStatAccumulator)
	s.credentialStats = make(map[string]*CredentialStat)
	s.clientAPIStats = make(map[string]*clientAPIStatAccumulator)
	for _, apiSt := range s.apis {
		apiSt.TotalRequests = 0
		apiSt.SuccessCount = 0
		apiSt.FailureCount = 0
		apiSt.TotalTokens = 0
		apiSt.InputTokens = 0
		apiSt.OutputTokens = 0
		apiSt.CachedTokens = 0
		apiSt.ReasoningTokens = 0
		apiSt.latencySum = 0
		apiSt.latencyN = 0
		for modelName, modelSt := range apiSt.Models {
			modelSt.TotalRequests = 0
			modelSt.SuccessCount = 0
			modelSt.FailureCount = 0
			modelSt.TotalTokens = 0
			modelSt.InputTokens = 0
			modelSt.OutputTokens = 0
			modelSt.CachedTokens = 0
			modelSt.ReasoningTokens = 0
			modelSt.latencySum = 0
			modelSt.latencyN = 0
			for _, detail := range modelSt.Details {
				totals := detailTotalsFromRequest(detail)
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
				s.totalTokens += totals.totalTokens
				s.inputTokens += totals.inputTokens
				s.outputTokens += totals.outputTokens
				s.cachedTokens += totals.cachedTokens
				s.reasoningTokens += totals.reasoningTokens
				s.latencySum += totals.latencySum
				s.latencyN += totals.latencyN
				apiSt.TotalTokens += totals.totalTokens
				apiSt.InputTokens += totals.inputTokens
				apiSt.OutputTokens += totals.outputTokens
				apiSt.CachedTokens += totals.cachedTokens
				apiSt.ReasoningTokens += totals.reasoningTokens
				apiSt.latencySum += totals.latencySum
				apiSt.latencyN += totals.latencyN
				modelSt.TotalTokens += totals.totalTokens
				modelSt.InputTokens += totals.inputTokens
				modelSt.OutputTokens += totals.outputTokens
				modelSt.CachedTokens += totals.cachedTokens
				modelSt.ReasoningTokens += totals.reasoningTokens
				modelSt.latencySum += totals.latencySum
				modelSt.latencyN += totals.latencyN
				dayKey := detail.Timestamp.Format("2006-01-02")
				hourKey := detail.Timestamp.Hour()
				s.requestsByDay[dayKey]++
				s.requestsByHour[hourKey]++
				s.tokensByDay[dayKey] += totals.totalTokens
				s.tokensByHour[hourKey] += totals.totalTokens
				s.incrementModelSummaryStatsLocked(modelName, detail, totals)
				s.incrementSummaryDimensionStatsLocked(modelName, detail, totals)
				s.incrementHealthBucketLocked(detail)
			}
		}
	}
}

// Snapshot returns a full deep-copy of all statistics including details.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *RequestStatistics) snapshotLocked() StatisticsSnapshot {
	result := StatisticsSnapshot{}
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

// MergeSnapshot imports a snapshot into the current statistics.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mergeSnapshotLocked(snapshot, true, time.Now())
}

func (s *RequestStatistics) mergeSnapshotLocked(snapshot StatisticsSnapshot, persist bool, now time.Time) MergeResult {
	result := MergeResult{}
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

		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = normalizeModelName(modelName)

			for _, detail := range modelSnapshot.Details {
				importModelName := normalizeModelName(detail.Model)
				if importModelName == "unknown" {
					importModelName = modelName
				}
				detail.Model = importModelName
				detail.Tokens.TotalTokens = detailTotalTokens(detail.Tokens)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = now
				}
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.TTFTMs < 0 {
					detail.TTFTMs = 0
				}

				if !cutoff.IsZero() && !detail.Timestamp.IsZero() && detail.Timestamp.Before(cutoff) {
					result.IgnoredByRetention++
					continue
				}

				importAPIName := usageGroupKeyFromDetail(apiName, detail)
				key := dedupKey(importAPIName, importModelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}

				s.recordImported(importAPIName, importModelName, detail, persist, now)
				result.Added++
			}
		}
	}

	s.pruneLocked(now, true)
	s.rebuildAggregatesLocked()
	s.rebuildSeenLocked(now)
	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, detail RequestDetail, persist bool, now time.Time) {
	if s.recordDetailLocked(apiName, modelName, detail, dedupKey(apiName, modelName, detail), now, false) {
		if persist {
			s.appendDetailLocked(persistedDetail{API: apiName, Model: modelName, Detail: detail})
		}
	}
}

func usageDetailTotalTokens(detail UsageDetail) int64 {
	totalTokens := detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return nonNegativeInt64(totalTokens)
}

func detailTotalTokens(tokens TokenStats) int64 {
	totalTokens := tokens.TotalTokens
	if totalTokens == 0 {
		totalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	return nonNegativeInt64(totalTokens)
}

func detailTotalsFromRequest(detail RequestDetail) detailTotals {
	totals := detailTotals{
		totalTokens:     detailTotalTokens(detail.Tokens),
		inputTokens:     nonNegativeInt64(detail.Tokens.InputTokens),
		outputTokens:    nonNegativeInt64(detail.Tokens.OutputTokens),
		cachedTokens:    normalizedCacheTokens(detail.Tokens),
		reasoningTokens: nonNegativeInt64(detail.Tokens.ReasoningTokens),
	}
	if detail.LatencyMs > 0 {
		totals.latencySum = detail.LatencyMs
		totals.latencyN = 1
	}
	return totals
}

func summarySourceKey(detail RequestDetail) string {
	source := detail.Source
	if source == "" {
		return "未知来源"
	}
	return source
}

func summaryCredentialKey(detail RequestDetail) string {
	if detail.AuthIndex == "" {
		return "(空)"
	}
	return detail.AuthIndex
}

func healthBucketKey(t time.Time) (int64, bool) {
	if t.IsZero() {
		return 0, false
	}
	return t.UTC().Truncate(dashboardHealthStep).Unix(), true
}

func normalizedCacheTokens(tokens TokenStats) int64 {
	return maxInt64(nonNegativeInt64(tokens.CachedTokens), nonNegativeInt64(tokens.CacheTokens))
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
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

// ============================================================================
// New P0 Methods: Lightweight Summary + Paginated Events
// ============================================================================

// SummaryWithoutDetails computes a lightweight dashboard summary without detail arrays.
func (s *RequestStatistics) SummaryWithoutDetails() DashboardSummary {
	if s == nil {
		return DashboardSummary{}
	}

	now := time.Now()
	healthWindow := summaryHealthWindow(now)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.summaryCacheValid && s.summaryCacheVersion == s.summaryVersion && s.summaryCacheWindow.Equal(healthWindow) {
		return cloneDashboardSummaryWithGeneratedAt(s.summaryCache, now)
	}

	summary := s.buildSummaryWithoutDetailsLocked(now, healthWindow)
	s.summaryCache = cloneDashboardSummary(summary)
	s.summaryCacheValid = true
	s.summaryCacheVersion = s.summaryVersion
	s.summaryCacheWindow = healthWindow
	return summary
}

func summaryHealthWindow(now time.Time) time.Time {
	return now.UTC().Truncate(dashboardHealthStep).Add(dashboardHealthStep)
}

func cloneDashboardSummaryWithGeneratedAt(summary DashboardSummary, now time.Time) DashboardSummary {
	cloned := cloneDashboardSummary(summary)
	cloned.GeneratedAt = now.UTC().Format(time.RFC3339)
	return cloned
}

func cloneDashboardSummary(summary DashboardSummary) DashboardSummary {
	cloned := summary
	cloned.Usage = cloneStatisticsSnapshotWithoutDetails(summary.Usage)
	cloned.HealthGrid = append([]HealthGridSlot(nil), summary.HealthGrid...)
	cloned.SourceStats = append([]SourceStat(nil), summary.SourceStats...)
	cloned.CredentialStats = append([]CredentialStat(nil), summary.CredentialStats...)
	cloned.ClientAPIStats = make([]ClientAPIStat, len(summary.ClientAPIStats))
	for i, stat := range summary.ClientAPIStats {
		cloned.ClientAPIStats[i] = stat
		cloned.ClientAPIStats[i].Models = append([]ClientAPIModelStat(nil), stat.Models...)
	}
	cloned.ModelStats = append([]ModelStat(nil), summary.ModelStats...)
	if summary.Meta.LastImport != nil {
		lastImport := *summary.Meta.LastImport
		cloned.Meta.LastImport = &lastImport
	}
	return cloned
}

func cloneStatisticsSnapshotWithoutDetails(snapshot StatisticsSnapshotWithoutDetails) StatisticsSnapshotWithoutDetails {
	cloned := snapshot
	cloned.APIs = make(map[string]APISnapshotWithoutDetails, len(snapshot.APIs))
	for apiName, apiSnapshot := range snapshot.APIs {
		apiClone := apiSnapshot
		apiClone.Models = make(map[string]ModelSnapshotWithoutDetails, len(apiSnapshot.Models))
		for modelName, modelSnapshot := range apiSnapshot.Models {
			apiClone.Models[modelName] = modelSnapshot
		}
		cloned.APIs[apiName] = apiClone
	}
	cloned.RequestsByDay = cloneInt64Map(snapshot.RequestsByDay)
	cloned.RequestsByHour = cloneInt64Map(snapshot.RequestsByHour)
	cloned.TokensByDay = cloneInt64Map(snapshot.TokensByDay)
	cloned.TokensByHour = cloneInt64Map(snapshot.TokensByHour)
	return cloned
}

func cloneInt64Map(values map[string]int64) map[string]int64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int64, len(values))
	for k, v := range values {
		cloned[k] = v
	}
	return cloned
}

func (s *RequestStatistics) buildSummaryWithoutDetailsLocked(now time.Time, healthWindow time.Time) DashboardSummary {
	summary := DashboardSummary{}
	summary.Usage.TotalRequests = s.totalRequests
	summary.Usage.SuccessCount = s.successCount
	summary.Usage.FailureCount = s.failureCount
	summary.Usage.TotalTokens = s.totalTokens
	summary.Usage.InputTokens = s.inputTokens
	summary.Usage.OutputTokens = s.outputTokens
	summary.Usage.CachedTokens = s.cachedTokens
	summary.Usage.ReasoningTokens = s.reasoningTokens
	if s.latencyN > 0 {
		summary.Usage.AvgLatencyMs = float64(s.latencySum) / float64(s.latencyN)
	}

	summary.Usage.APIs = make(map[string]APISnapshotWithoutDetails, len(s.apis))

	healthStart := healthWindow.Add(-dashboardHealthSlotCount * dashboardHealthStep)

	for apiName, apiSt := range s.apis {
		apiSnap := APISnapshotWithoutDetails{
			TotalRequests:   apiSt.TotalRequests,
			SuccessCount:    apiSt.SuccessCount,
			FailureCount:    apiSt.FailureCount,
			TotalTokens:     apiSt.TotalTokens,
			InputTokens:     apiSt.InputTokens,
			OutputTokens:    apiSt.OutputTokens,
			CachedTokens:    apiSt.CachedTokens,
			ReasoningTokens: apiSt.ReasoningTokens,
			Models:          make(map[string]ModelSnapshotWithoutDetails, len(apiSt.Models)),
		}
		if apiSt.latencyN > 0 {
			apiSnap.AvgLatencyMs = float64(apiSt.latencySum) / float64(apiSt.latencyN)
		}

		for modelName, modelSt := range apiSt.Models {
			modelSnap := ModelSnapshotWithoutDetails{
				TotalRequests:   modelSt.TotalRequests,
				SuccessCount:    modelSt.SuccessCount,
				FailureCount:    modelSt.FailureCount,
				TotalTokens:     modelSt.TotalTokens,
				InputTokens:     modelSt.InputTokens,
				OutputTokens:    modelSt.OutputTokens,
				CachedTokens:    modelSt.CachedTokens,
				ReasoningTokens: modelSt.ReasoningTokens,
			}
			if modelSt.latencyN > 0 {
				modelSnap.AvgLatencyMs = float64(modelSt.latencySum) / float64(modelSt.latencyN)
			}

			apiSnap.Models[modelName] = modelSnap
		}
		summary.Usage.APIs[apiName] = apiSnap
	}

	// Finalize model average latencies from accumulated sums.
	summary.ModelStats = make([]ModelStat, 0, len(s.modelSummaryStats))
	for _, m := range s.modelSummaryStats {
		stat := *m
		if stat.latencyN > 0 {
			stat.AvgLatencyMs = float64(stat.latencySum) / float64(stat.latencyN)
		}
		stat.latencySum = 0
		stat.latencyN = 0
		summary.ModelStats = append(summary.ModelStats, stat)
	}
	sort.SliceStable(summary.ModelStats, func(i, j int) bool {
		return summary.ModelStats[i].TotalRequests > summary.ModelStats[j].TotalRequests
	})

	// Build source stats sorted by requests
	summary.SourceStats = make([]SourceStat, 0, len(s.sourceStats))
	for _, sr := range s.sourceStats {
		summary.SourceStats = append(summary.SourceStats, sr.stat)
	}
	sort.SliceStable(summary.SourceStats, func(i, j int) bool {
		return summary.SourceStats[i].TotalRequests > summary.SourceStats[j].TotalRequests
	})

	// Build credential stats sorted by requests
	summary.CredentialStats = make([]CredentialStat, 0, len(s.credentialStats))
	for _, cr := range s.credentialStats {
		summary.CredentialStats = append(summary.CredentialStats, *cr)
	}
	sort.SliceStable(summary.CredentialStats, func(i, j int) bool {
		return summary.CredentialStats[i].TotalRequests > summary.CredentialStats[j].TotalRequests
	})

	summary.ClientAPIStats = make([]ClientAPIStat, 0, len(s.clientAPIStats))
	for _, agg := range s.clientAPIStats {
		stat := agg.stat
		stat.Models = make([]ClientAPIModelStat, 0, len(agg.models))
		for _, model := range agg.models {
			stat.Models = append(stat.Models, *model)
		}
		sort.SliceStable(stat.Models, func(i, j int) bool {
			return stat.Models[i].TotalRequests > stat.Models[j].TotalRequests
		})
		summary.ClientAPIStats = append(summary.ClientAPIStats, stat)
	}
	sort.SliceStable(summary.ClientAPIStats, func(i, j int) bool {
		return summary.ClientAPIStats[i].TotalRequests > summary.ClientAPIStats[j].TotalRequests
	})

	// Build health grid
	summary.HealthGrid = make([]HealthGridSlot, dashboardHealthSlotCount)
	for i := 0; i < dashboardHealthSlotCount; i++ {
		t := healthStart.Add(time.Duration(i) * dashboardHealthStep)
		slot := s.healthBuckets[t.Unix()]
		summary.HealthGrid[i] = HealthGridSlot{
			Slot:    i,
			Total:   slot.success + slot.failure,
			Success: slot.success,
			Failure: slot.failure,
			Start:   t.Format(time.RFC3339),
			End:     t.Add(dashboardHealthStep).Format(time.RFC3339),
		}
	}

	// Time series
	summary.Usage.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		summary.Usage.RequestsByDay[k] = v
	}
	summary.Usage.RequestsByHour = make(map[string]int64, 24)
	for hour, v := range s.requestsByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.RequestsByHour[hourKeys[hour]] = v
		}
	}
	summary.Usage.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		summary.Usage.TokensByDay[k] = v
	}
	summary.Usage.TokensByHour = make(map[string]int64, 24)
	for hour, v := range s.tokensByHour {
		if hour >= 0 && hour < 24 {
			summary.Usage.TokensByHour[hourKeys[hour]] = v
		}
	}

	// Metadata
	summary.Meta.RetentionDays = int(s.retention.Hours() / 24)
	summary.Meta.MaxDetailsPerModel = s.maxDetailsPerModel
	summary.Meta.CurrentDetailCount = s.countDetailsLocked()
	summary.Meta.EvictedTotal = s.evictedTotal
	summary.Meta.Storage = s.storageStatusLocked()
	if !s.lastRecordedAt.IsZero() {
		summary.Meta.LastRecordedAt = s.lastRecordedAt.UTC().Format(time.RFC3339)
	}
	if s.lastImportResult != nil {
		summary.Meta.LastImport = &ImportSummary{
			Added:              s.lastImportResult.Added,
			Skipped:            s.lastImportResult.Skipped,
			IgnoredByRetention: s.lastImportResult.IgnoredByRetention,
		}
	}

	summary.GeneratedAt = now.UTC().Format(time.RFC3339)
	return summary
}

func clientAPIGroupLabel(detail RequestDetail) string {
	label := strings.TrimSpace(detail.APIKey)
	if label == "" {
		return "未知 API"
	}
	return label
}

func clientAPIGroupKey(detail RequestDetail) string {
	label := strings.TrimSpace(detail.APIKey)
	if label != "" {
		return "api_key:" + label
	}
	hash := strings.TrimSpace(detail.APIKeyHash)
	if hash != "" {
		return "api_key_hash:" + hash
	}
	return "(unknown)"
}

func dashboardRangeCutoff(rangeKey string, now time.Time) time.Time {
	switch rangeKey {
	case "7h":
		return now.Add(-7 * time.Hour)
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

type dashboardEventDetail struct {
	detail    *RequestDetail
	sortKey   string
	modelName string
	sequence  int64
}

func (d dashboardEventDetail) requestDetail() RequestDetail {
	if d.detail == nil {
		return RequestDetail{}
	}
	return *d.detail
}

func (d dashboardEventDetail) timestamp() time.Time {
	if d.detail == nil {
		return time.Time{}
	}
	return d.detail.Timestamp
}

func dashboardEventBefore(a, b dashboardEventDetail) bool {
	at := a.timestamp()
	bt := b.timestamp()
	if !at.Equal(bt) {
		return at.After(bt)
	}
	if a.sortKey != b.sortKey {
		return a.sortKey < b.sortKey
	}
	return a.sequence < b.sequence
}

type dashboardEventHeap []dashboardEventDetail

func (h dashboardEventHeap) Len() int { return len(h) }

func (h dashboardEventHeap) Less(i, j int) bool {
	return dashboardEventBefore(h[j], h[i])
}

func (h dashboardEventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *dashboardEventHeap) Push(x any) {
	*h = append(*h, x.(dashboardEventDetail))
}

func (h *dashboardEventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func appendBoundedDashboardEventHeap(events *dashboardEventHeap, candidate dashboardEventDetail, limit int) {
	if events == nil || limit <= 0 {
		return
	}
	if events.Len() < limit {
		heap.Push(events, candidate)
		return
	}
	if dashboardEventBefore(candidate, (*events)[0]) {
		(*events)[0] = candidate
		heap.Fix(events, 0)
	}
}

func dashboardEventCacheKeyFor(params EventsQuery, now time.Time) dashboardEventCacheKey {
	var timeBucket int64
	if params.Range != "" && params.Range != "all" {
		timeBucket = now.UTC().Unix()
	}
	return dashboardEventCacheKey{
		limit:      params.Limit,
		offset:     params.Offset,
		timeBucket: timeBucket,
		rangeKey:   params.Range,
		model:      params.Model,
		source:     params.Source,
		authIndex:  params.AuthIndex,
		api:        params.API,
	}
}

func cloneEventsResult(result EventsResult, generatedAt time.Time) EventsResult {
	cloned := result
	cloned.Events = cloneRequestDetails(result.Events)
	if !generatedAt.IsZero() {
		cloned.GeneratedAt = generatedAt.UTC().Format(time.RFC3339)
	}
	return cloned
}

func cloneRequestDetails(details []RequestDetail) []RequestDetail {
	if details == nil {
		return nil
	}
	cloned := make([]RequestDetail, len(details))
	for i, detail := range details {
		cloned[i] = detail
		if detail.Headers != nil {
			cloned[i].Headers = cloneHeaders(detail.Headers)
		}
	}
	return cloned
}

func cloneHeaders(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string][]string, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func (s *RequestStatistics) cacheDashboardEventsLocked(key dashboardEventCacheKey, result EventsResult) {
	if s == nil {
		return
	}
	if s.eventQueryCache == nil {
		s.eventQueryCache = make(map[dashboardEventCacheKey]EventsResult)
	}
	if _, exists := s.eventQueryCache[key]; !exists {
		s.eventQueryCacheOrder = append(s.eventQueryCacheOrder, key)
	}
	s.eventQueryCache[key] = cloneEventsResult(result, time.Time{})
	for len(s.eventQueryCacheOrder) > dashboardEventCacheMax {
		evict := s.eventQueryCacheOrder[0]
		s.eventQueryCacheOrder = s.eventQueryCacheOrder[1:]
		delete(s.eventQueryCache, evict)
	}
}

func (s *RequestStatistics) dashboardEventIndexLocked(api string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	if s.eventIndexVersion != s.summaryVersion {
		s.eventIndexVersion = s.summaryVersion
		s.eventIndex = nil
		s.eventAPIIndex = nil
		s.eventModelIndex = nil
		s.eventSourceIndex = nil
		s.eventAuthIndex = nil
	}
	if api != "" {
		if s.eventAPIIndex == nil {
			s.eventAPIIndex = make(map[string][]dashboardEventDetail)
		}
		if events, ok := s.eventAPIIndex[api]; ok {
			return events
		}
		events := buildDashboardEventIndexForAPI(api, s.apis[api])
		s.eventAPIIndex[api] = events
		return events
	}
	if s.eventIndex == nil {
		var events []dashboardEventDetail
		for apiName, apiSt := range s.apis {
			events = appendDashboardEventIndexForAPI(events, apiName, apiSt)
		}
		sort.Slice(events, func(i, j int) bool {
			return dashboardEventBefore(events[i], events[j])
		})
		s.eventIndex = events
	}
	return s.eventIndex
}

func (s *RequestStatistics) dashboardEventQueryIndexLocked(params EventsQuery) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	if params.API != "" {
		return s.dashboardEventIndexLocked(params.API)
	}
	if params.Model != "" {
		return s.dashboardEventModelIndexLocked(params.Model)
	}
	if params.Source != "" {
		return s.dashboardEventSourceIndexLocked(params.Source)
	}
	if params.AuthIndex != "" {
		return s.dashboardEventAuthIndexLocked(params.AuthIndex)
	}
	return s.dashboardEventIndexLocked("")
}

func (s *RequestStatistics) dashboardEventModelIndexLocked(model string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventModelIndex == nil {
		s.eventModelIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventModelIndex[model]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventModelKey(event) == model {
			events = append(events, event)
		}
	}
	s.eventModelIndex[model] = events
	return events
}

func (s *RequestStatistics) dashboardEventSourceIndexLocked(source string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventSourceIndex == nil {
		s.eventSourceIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventSourceIndex[source]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventSourceKey(event) == source {
			events = append(events, event)
		}
	}
	s.eventSourceIndex[source] = events
	return events
}

func (s *RequestStatistics) dashboardEventAuthIndexLocked(authIndex string) []dashboardEventDetail {
	if s == nil {
		return nil
	}
	index := s.dashboardEventIndexLocked("")
	if s.eventAuthIndex == nil {
		s.eventAuthIndex = make(map[string][]dashboardEventDetail)
	}
	if events, ok := s.eventAuthIndex[authIndex]; ok {
		return events
	}
	events := make([]dashboardEventDetail, 0)
	for _, event := range index {
		if dashboardEventAuthKey(event) == authIndex {
			events = append(events, event)
		}
	}
	s.eventAuthIndex[authIndex] = events
	return events
}

func dashboardEventModelKey(event dashboardEventDetail) string {
	d := event.requestDetail()
	if d.Model != "" {
		return d.Model
	}
	return event.modelName
}

func dashboardEventSourceKey(event dashboardEventDetail) string {
	source := event.requestDetail().Source
	if source == "" {
		return "未知来源"
	}
	return source
}

func dashboardEventAuthKey(event dashboardEventDetail) string {
	return event.requestDetail().AuthIndex
}

func buildDashboardEventIndexForAPI(apiName string, apiSt *apiStats) []dashboardEventDetail {
	events := appendDashboardEventIndexForAPI(nil, apiName, apiSt)
	sort.Slice(events, func(i, j int) bool {
		return dashboardEventBefore(events[i], events[j])
	})
	return events
}

func appendDashboardEventIndexForAPI(events []dashboardEventDetail, apiName string, apiSt *apiStats) []dashboardEventDetail {
	if apiSt == nil {
		return events
	}
	sequence := int64(len(events))
	for modelName, modelSt := range apiSt.Models {
		if modelSt == nil {
			continue
		}
		for i := range modelSt.Details {
			events = append(events, dashboardEventDetail{detail: &modelSt.Details[i], sortKey: apiName, modelName: modelName, sequence: sequence})
			sequence++
		}
	}
	return events
}

func requestDetailsFromDashboardEvents(events []dashboardEventDetail) []RequestDetail {
	details := make([]RequestDetail, len(events))
	for i, event := range events {
		details[i] = event.requestDetail()
	}
	return details
}

func dashboardEventQueryHasFilters(params EventsQuery) bool {
	return params.Range != "" && params.Range != "all" ||
		params.Model != "" ||
		params.Source != "" ||
		params.AuthIndex != ""
}

func dashboardEventPastCutoff(d RequestDetail, cutoff time.Time) bool {
	return !cutoff.IsZero() && !d.Timestamp.IsZero() && d.Timestamp.Before(cutoff)
}

func dashboardEventMatches(d RequestDetail, params EventsQuery, cutoff time.Time) bool {
	if dashboardEventPastCutoff(d, cutoff) {
		return false
	}
	if params.Model != "" && d.Model != params.Model {
		return false
	}
	if params.Source != "" {
		src := d.Source
		if src == "" {
			src = "未知来源"
		}
		if src != params.Source {
			return false
		}
	}
	if params.AuthIndex != "" && d.AuthIndex != params.AuthIndex {
		return false
	}
	return true
}

// QueryEvents returns paginated, filtered event details.
func (s *RequestStatistics) QueryEvents(params EventsQuery) EventsResult {
	return s.queryEvents(params, true)
}

// QueryAllEvents returns every matching event for backend-generated exports.
func (s *RequestStatistics) QueryAllEvents(params EventsQuery) EventsResult {
	return s.queryEvents(params, false)
}

func (s *RequestStatistics) queryEvents(params EventsQuery, paginate bool) EventsResult {
	if s == nil {
		return EventsResult{}
	}

	if paginate {
		if params.Limit <= 0 || params.Limit > 500 {
			params.Limit = 50
		}
		if params.Offset < 0 {
			params.Offset = 0
		}
	} else {
		params.Limit = 0
		params.Offset = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cutoff := dashboardRangeCutoff(params.Range, now)
	var cacheKey dashboardEventCacheKey
	if paginate {
		cacheKey = dashboardEventCacheKeyFor(params, now)
		if cached, ok := s.eventQueryCache[cacheKey]; ok {
			return cloneEventsResult(cached, now)
		}
	}

	index := s.dashboardEventQueryIndexLocked(params)
	if !dashboardEventQueryHasFilters(params) {
		total := len(index)
		if !paginate {
			return EventsResult{
				Events:      requestDetailsFromDashboardEvents(index),
				Total:       total,
				Limit:       total,
				Offset:      0,
				GeneratedAt: now.UTC().Format(time.RFC3339),
			}
		}
		if params.Offset >= total {
			result := EventsResult{
				Events:      []RequestDetail{},
				Total:       total,
				Limit:       params.Limit,
				Offset:      params.Offset,
				GeneratedAt: now.UTC().Format(time.RFC3339),
			}
			s.cacheDashboardEventsLocked(cacheKey, result)
			return result
		}
		end := params.Offset + params.Limit
		if end > total {
			end = total
		}
		result := EventsResult{
			Events:      requestDetailsFromDashboardEvents(index[params.Offset:end]),
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return result
	}

	var events []RequestDetail
	total := 0
	for _, dm := range index {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(d, cutoff) {
			break
		}
		if !dashboardEventMatches(d, params, cutoff) {
			continue
		}
		if !paginate || (total >= params.Offset && len(events) < params.Limit) {
			events = append(events, d)
		}
		total++
	}

	if !paginate {
		if events == nil {
			events = []RequestDetail{}
		}
		return EventsResult{
			Events:      events,
			Total:       total,
			Limit:       total,
			Offset:      0,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
	}

	if params.Offset >= total {
		result := EventsResult{
			Events:      []RequestDetail{},
			Total:       total,
			Limit:       params.Limit,
			Offset:      params.Offset,
			GeneratedAt: now.UTC().Format(time.RFC3339),
		}
		s.cacheDashboardEventsLocked(cacheKey, result)
		return result
	}

	result := EventsResult{
		Events:      events,
		Total:       total,
		Limit:       params.Limit,
		Offset:      params.Offset,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}
	s.cacheDashboardEventsLocked(cacheKey, result)
	return result
}

// QueryAPIDetail returns range-scoped aggregates and recent events for one API
// without making the browser page through every matching event.
func (s *RequestStatistics) QueryAPIDetail(api string, rangeKey string, recentLimit int, errorLimit int) APIDetailResponse {
	result := APIDetailResponse{
		API:         api,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if s == nil {
		return result
	}
	if recentLimit <= 0 || recentLimit > 500 {
		recentLimit = 120
	}
	if errorLimit <= 0 || errorLimit > 100 {
		errorLimit = 20
	}

	now := time.Now()
	cutoff := dashboardRangeCutoff(rangeKey, now)

	s.mu.Lock()
	defer s.mu.Unlock()

	index := s.dashboardEventIndexLocked(api)
	if len(index) == 0 {
		result.GeneratedAt = now.UTC().Format(time.RFC3339)
		return result
	}

	modelAgg := make(map[string]*ModelStat)
	sourceAgg := make(map[string]*SourceStat)
	errorAgg := make(map[string]*APIDetailErrorStat)
	recentEvents := dashboardEventHeap{}
	heap.Init(&recentEvents)
	var latencySum int64
	var latencyN int64
	sequence := int64(0)

	for _, dm := range index {
		d := dm.requestDetail()
		if dashboardEventPastCutoff(d, cutoff) {
			break
		}
		totalTokens := detailTotalTokens(d.Tokens)
		inputTokens := nonNegativeInt64(d.Tokens.InputTokens)
		outputTokens := nonNegativeInt64(d.Tokens.OutputTokens)
		reasoningTokens := nonNegativeInt64(d.Tokens.ReasoningTokens)
		cachedTokens := normalizedCacheTokens(d.Tokens)

		result.TotalEvents++
		result.Summary.TotalRequests++
		if d.Failed {
			result.Summary.FailureCount++
		} else {
			result.Summary.SuccessCount++
		}
		result.Summary.TotalTokens += totalTokens
		result.Summary.InputTokens += inputTokens
		result.Summary.OutputTokens += outputTokens
		result.Summary.CachedTokens += cachedTokens
		result.Summary.ReasoningTokens += reasoningTokens
		if d.LatencyMs > 0 {
			latencySum += d.LatencyMs
			latencyN++
		}

		modelLabel := normalizeModelName(dm.modelName)
		if d.Model != "" {
			modelLabel = d.Model
		}
		ms, ok := modelAgg[modelLabel]
		if !ok {
			ms = &ModelStat{Model: modelLabel}
			modelAgg[modelLabel] = ms
		}
		ms.TotalRequests++
		if d.Failed {
			ms.FailureCount++
		} else {
			ms.SuccessCount++
		}
		ms.TotalTokens += totalTokens
		ms.InputTokens += inputTokens
		ms.OutputTokens += outputTokens
		ms.CachedTokens += cachedTokens
		ms.ReasoningTokens += reasoningTokens
		if d.LatencyMs > 0 {
			ms.latencySum += d.LatencyMs
			ms.latencyN++
		}

		source := strings.TrimSpace(d.Source)
		if source == "" {
			source = "未知来源"
		}
		ss, ok := sourceAgg[source]
		if !ok {
			ss = &SourceStat{Source: source, Provider: d.Provider}
			sourceAgg[source] = ss
		}
		ss.TotalRequests++
		if d.Failed {
			ss.FailureCount++
		} else {
			ss.SuccessCount++
		}
		ss.TotalTokens += totalTokens

		if d.Failed {
			failure := strings.TrimSpace(d.Failure)
			if failure == "" {
				failure = "未返回错误内容"
			}
			key := fmt.Sprintf("%d|%s", d.StatusCode, failure)
			es, ok := errorAgg[key]
			if !ok {
				es = &APIDetailErrorStat{StatusCode: d.StatusCode, Failure: failure}
				errorAgg[key] = es
			}
			es.Count++
		}

		appendBoundedDashboardEventHeap(&recentEvents, dashboardEventDetail{detail: dm.detail, sortKey: d.Model, sequence: sequence}, recentLimit)
		sequence++
	}

	if latencyN > 0 {
		result.Summary.AvgLatencyMs = float64(latencySum) / float64(latencyN)
	}
	result.ModelStats = make([]ModelStat, 0, len(modelAgg))
	for _, ms := range modelAgg {
		if ms.latencyN > 0 {
			ms.AvgLatencyMs = float64(ms.latencySum) / float64(ms.latencyN)
		}
		ms.latencySum = 0
		ms.latencyN = 0
		result.ModelStats = append(result.ModelStats, *ms)
	}
	sort.SliceStable(result.ModelStats, func(i, j int) bool {
		return result.ModelStats[i].TotalRequests > result.ModelStats[j].TotalRequests
	})

	result.SourceStats = make([]SourceStat, 0, len(sourceAgg))
	for _, ss := range sourceAgg {
		result.SourceStats = append(result.SourceStats, *ss)
	}
	sort.SliceStable(result.SourceStats, func(i, j int) bool {
		return result.SourceStats[i].TotalRequests > result.SourceStats[j].TotalRequests
	})

	result.ErrorStats = make([]APIDetailErrorStat, 0, len(errorAgg))
	for _, es := range errorAgg {
		result.ErrorStats = append(result.ErrorStats, *es)
	}
	sort.SliceStable(result.ErrorStats, func(i, j int) bool {
		return result.ErrorStats[i].Count > result.ErrorStats[j].Count
	})
	if len(result.ErrorStats) > errorLimit {
		result.ErrorStats = result.ErrorStats[:errorLimit]
	}

	sort.Slice(recentEvents, func(i, j int) bool {
		return dashboardEventBefore(recentEvents[i], recentEvents[j])
	})
	result.RecentEvents = make([]RequestDetail, len(recentEvents))
	for i, dm := range recentEvents {
		result.RecentEvents[i] = dm.requestDetail()
	}
	result.GeneratedAt = now.UTC().Format(time.RFC3339)
	return result
}

func (s *RequestStatistics) countDetailsLocked() int64 {
	var count int64
	for _, apiSt := range s.apis {
		for _, modelSt := range apiSt.Models {
			count += int64(len(modelSt.Details))
		}
	}
	return count
}

func (s *RequestStatistics) DetailCount() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countDetailsLocked()
}

func (s *RequestStatistics) EvictedTotal() int64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evictedTotal
}

func (s *RequestStatistics) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeStorageLocked()
}

func (s *RequestStatistics) ConfigSnapshot() ExportConfig {
	if s == nil {
		return ExportConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ExportConfig{
		RetentionDays:      int(s.retention.Hours() / 24),
		MaxDetailsPerModel: s.maxDetailsPerModel,
		DedupWindowMinutes: int(s.dedupWindow.Minutes()),
		LogResponseHeaders: s.logResponseHeaders.String(),
		StorageEnabled:     s.storageEnabled,
		StoragePath:        s.storagePath,
		PriceStoragePath:   s.priceStoragePath,
	}
}

func (s *RequestStatistics) StorageStatus() StorageStatus {
	if s == nil {
		return StorageStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storageStatusLocked()
}

func (s *RequestStatistics) storageStatusLocked() StorageStatus {
	status := StorageStatus{
		Enabled:                s.storageEnabled,
		Path:                   s.storagePath,
		LoadedPath:             s.storageLoadedPath,
		LastError:              s.storageLastError,
		PendingBufferedRecords: s.storageBuffered,
	}
	if !s.storageLastFlush.IsZero() {
		status.LastFlushAt = s.storageLastFlush.UTC().Format(time.RFC3339)
	}
	return status
}

func (s *RequestStatistics) RuntimeStatus() RuntimeStatus {
	if s == nil {
		return RuntimeStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := RuntimeStatus{
		SeenCount: len(s.seen),
	}
	if !s.startedAt.IsZero() {
		status.StartedAt = s.startedAt.UTC().Format(time.RFC3339)
	}
	if !s.lastRecordedAt.IsZero() {
		status.LastRecordedAt = s.lastRecordedAt.UTC().Format(time.RFC3339)
	}
	if s.lastImportResult != nil {
		status.LastImport = &ImportSummary{
			Added:              s.lastImportResult.Added,
			Skipped:            s.lastImportResult.Skipped,
			IgnoredByRetention: s.lastImportResult.IgnoredByRetention,
		}
	}
	return status
}
