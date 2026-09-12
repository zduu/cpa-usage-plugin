package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Same fixture and public file encoder on B0, B1 and the candidate. Dataset
// preparation is excluded; snapshot capture, encoding, flush and close count.
func BenchmarkReleaseExportFile(b *testing.B) {
	old := stats
	stats = buildBenchmarkStats(10000)
	defer func() { stats = old }()
	now := time.Now()
	for _, format := range []dashboardExportFormat{dashboardExportJSON, dashboardExportJSONL, dashboardExportCSV} {
		for _, compressed := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/gzip=%t", format, compressed), func(b *testing.B) {
				path := filepath.Join(b.TempDir(), "export")
				opts := dashboardEventsExportOptions{Format: format, Gzip: compressed}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					result, err := encodeDashboardEventsExportFile(EventsQuery{}, opts, path, now)
					if err != nil || result.Exported != 10000 {
						b.Fatalf("export=%+v err=%v", result, err)
					}
				}
			})
		}
	}
}
