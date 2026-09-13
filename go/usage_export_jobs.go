package main

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Full usage backups have a separate route and explicit kind. Older event
// exporters must never silently satisfy a backup request with visible rows.
func handleUsageExportJobCreate(query map[string][]string) ([]byte, error) {
	if format := queryRawValue(query, "format"); format != "" && format != string(dashboardExportJSON) {
		return dashboardExportJobJSON(http.StatusBadRequest, dashboardExportJobErrorResponse{Error: "usage backups require JSON format"})
	}
	opts := dashboardEventsExportOptions{Format: dashboardExportJSON, Gzip: queryBool(query, "gzip")}
	// No EventsQuery or event export limit applies to a full usage backup.
	job, statusCode, message := dashboardExportJobs.createKind(dashboardExportKindUsage, EventsQuery{}, opts)
	if message != "" {
		return dashboardExportJobJSON(statusCode, dashboardExportJobErrorResponse{Error: message})
	}
	return dashboardExportJobJSON(statusCode, dashboardExportJobSnapshot(job))
}

type usageExportView struct {
	header ExportPayload
	usage  storageSnapshotView
}

type usageExportHeader struct {
	*ExportPayload
	Usage *int `json:"usage,omitempty"`
}

func (s *RequestStatistics) captureUsageExport(ctx context.Context) (usageExportView, error) {
	if err := s.lockUsageExportSnapshot(ctx); err != nil {
		return usageExportView{}, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return usageExportView{}, err
	}
	now := time.Now()
	if s.protocolFallbackReconcileDirty {
		s.reconcileRecordedProtocolFallbacksLocked(now)
	}
	view := usageExportView{
		header: ExportPayload{
			Version:     1,
			ExportedAt:  now.UTC().Format(time.RFC3339),
			Plugin:      pluginVersion,
			DetailCount: s.countAccountingLocked(),
			Config:      s.configSnapshotLocked(),
		},
		usage: s.captureStorageSnapshotLocked(),
	}
	return view, ctx.Err()
}

// A large import or snapshot can hold the statistics lock for a while. A
// canceled backup must release its worker slot without waiting for that work.
// The normal uncontended path acquires the lock once, without a timer.
func (s *RequestStatistics) lockUsageExportSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.mu.TryLock() {
		return nil
	}
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry.C:
			if s.mu.TryLock() {
				return nil
			}
		}
	}
}

func (view usageExportView) write(writer io.Writer) error {
	if err := writeJSONObjectPrefix(writer, usageExportHeader{ExportPayload: &view.header}); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"usage":`); err != nil {
		return err
	}
	if err := view.usage.writeUsage(writer); err != nil {
		return err
	}
	_, err := io.WriteString(writer, "}")
	return err
}

func encodeUsageExportFile(opts dashboardEventsExportOptions, filePath string) (dashboardExportFileResult, error) {
	ctx := opts.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return encodeDashboardExportFile(opts, filePath, func(writer io.Writer) (dashboardExportFileResult, error) {
		view, err := stats.captureUsageExport(ctx)
		if err != nil {
			return dashboardExportFileResult{}, err
		}
		err = view.write(&exportContextWriter{ctx: ctx, writer: writer})
		return dashboardExportFileResult{
			Total:       int(view.header.DetailCount),
			Exported:    int(view.header.DetailCount),
			ContentType: dashboardExportContentType(dashboardExportJSON),
			GeneratedAt: view.header.ExportedAt,
		}, err
	})
}
