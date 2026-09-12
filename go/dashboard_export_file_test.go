package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExportFileFlushesEveryFormatAndGzipTrailer(t *testing.T) {
	old := stats
	stats = buildBenchmarkStats(dashboardExportJobPageSize + 1)
	defer func() { stats = old }()
	at := time.Now()
	for _, format := range []dashboardExportFormat{dashboardExportJSON, dashboardExportJSONL, dashboardExportCSV} {
		for _, compressed := range []bool{false, true} {
			for _, rows := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/gzip=%t/rows=%t", format, compressed, rows), func(t *testing.T) {
					opts := dashboardEventsExportOptions{Format: format, Gzip: compressed, JSONRows: rows}
					var want bytes.Buffer
					if _, err := encodeDashboardEventsExportPaged(&want, EventsQuery{}, opts, at); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "export")
					result, err := encodeDashboardEventsExportFile(EventsQuery{}, opts, path, at)
					if err != nil {
						t.Fatal(err)
					}
					body, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if result.BodyBytes != len(body) || result.RawBytes != want.Len() || result.Exported != dashboardExportJobPageSize+1 {
						t.Fatalf("file counters or pages differ: %+v", result)
					}
					if compressed {
						reader, err := gzip.NewReader(bytes.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						body, err = io.ReadAll(reader)
						if err != nil {
							t.Fatal(err)
						}
						if err := reader.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if !bytes.Equal(body, want.Bytes()) {
						t.Fatal("file flush changed bytes or omitted final records")
					}
				})
			}
		}
	}
}

func TestDashboardJSONArrayFilePreservesPagesValuesAndFormatting(t *testing.T) {
	for _, count := range []int{0, 1, dashboardExportJobPageSize + 1} {
		events := make([]RequestDetail, count)
		for i := range events {
			cost := float64(i) / 1e6
			events[i] = RequestDetail{Model: "中文🙂", Timestamp: time.Unix(1700000000, int64(i)),
				Tokens: TokenStats{InputTokens: 9007199254740993}, CostUSD: &cost,
				Headers: map[string][]string{"x-test": {"quotes\" and\nnewlines"}}}
		}
		frozen := &eventExportSnapshot{result: EventsResult{Events: events, Total: count + 10, Limit: count, Truncated: true}}
		opts := dashboardEventsExportOptions{Format: dashboardExportJSON, JSONRows: true, frozen: frozen}
		var file bytes.Buffer
		exported, err := encodeDashboardEventsJSONPaged(&file, opts, frozen.page(0, dashboardExportJobPageSize))
		if err != nil || exported != count {
			t.Fatalf("count=%d exported=%d error=%v", count, exported, err)
		}
		want, err := json.MarshalIndent(events, "", "  ")
		if err != nil || !bytes.Equal(file.Bytes(), want) {
			t.Fatalf("count=%d: array file changed values, order or indentation: %v", count, err)
		}
		if count > 0 && !bytes.Contains(file.Bytes(), []byte("9007199254740993")) {
			t.Fatal("download rounded int64 through JavaScript number precision")
		}
		// The opt-in file representation must not alter the existing envelope.
		opts.JSONRows = false
		file.Reset()
		_, err = encodeDashboardEventsJSONPaged(&file, opts, frozen.page(0, dashboardExportJobPageSize))
		var legacy EventsResult
		if err != nil || json.Unmarshal(file.Bytes(), &legacy) != nil || legacy.Total != count+10 || !legacy.Truncated || len(legacy.Events) != count {
			t.Fatalf("legacy export envelope changed for count=%d", count)
		}
	}
}

func TestDashboardJSONArrayCancellationAndEmptyJobSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	frozen := &eventExportSnapshot{result: EventsResult{Events: []RequestDetail{{Model: "cancel"}}, Limit: 1}}
	var file bytes.Buffer
	_, err := encodeDashboardEventsJSONArrayPaged(&file, dashboardEventsExportOptions{ctx: ctx, frozen: frozen}, frozen.page(0, 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled export: %v", err)
	}
	response := dashboardExportJobSnapshot(dashboardExportJob{Status: dashboardExportJobSucceeded, Options: dashboardEventsExportOptions{JSONRows: true}})
	raw, err := json.Marshal(response)
	if err != nil || !bytes.Contains(raw, []byte(`"body_bytes":0`)) || !bytes.Contains(raw, []byte(`"json_rows":true`)) {
		t.Fatalf("missing empty-file size or representation negotiation: %s (%v)", raw, err)
	}
}
