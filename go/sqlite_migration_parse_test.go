package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Rebuild only in tests, to compare the event contract directly with the old
// typed decoder. Production consumers must stage events / reconcile on disk.
type sqliteMigrationTestJSON struct {
	bytes.Buffer
	kinds  []string
	counts []int
}

func (b *sqliteMigrationTestJSON) consume(item sqliteMigrationItem) error {
	if item.Kind == "end" {
		i := len(b.kinds) - 1
		if b.kinds[i] == "object" {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
		b.kinds, b.counts = b.kinds[:i], b.counts[:i]
		return nil
	}
	if i := len(b.kinds) - 1; i >= 0 {
		if b.counts[i] > 0 {
			b.WriteByte(',')
		}
		b.counts[i]++
		if b.kinds[i] == "object" {
			key, _ := json.Marshal(item.Path[len(item.Path)-1])
			b.Write(key)
			b.WriteByte(':')
		}
	}
	switch item.Kind {
	case "object", "array":
		if item.Kind == "object" {
			b.WriteByte('{')
		} else {
			b.WriteByte('[')
		}
		b.kinds = append(b.kinds, item.Kind)
		b.counts = append(b.counts, 0)
	case "null":
		b.WriteString("null")
	case "value":
		b.Write(item.Value)
	default:
		return errors.New("unexpected reconstruction event")
	}
	return nil
}

func TestSQLiteMigrationSnapshotStreamingMatchesTypedDecode(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	detail := sqliteTestRecord(1).Detail
	// Source JSON only preserves minute-resolution time offsets, just as the
	// old writer did. Compare to decoding the same bytes, not the fixture.
	usage := StatisticsSnapshot{
		TotalRequests: math.MaxInt64, SuccessCount: 7, FailureCount: 2, TotalTokens: math.MaxInt64,
		InputTokens: 23, OutputTokens: 24, CachedTokens: 15, CacheWriteTokens: 5, ReasoningTokens: 8, AvgLatencyMs: 17.25,
		APIs: map[string]APISnapshot{" API / 中文 ": {
			TotalRequests: 50, TotalTokens: math.MaxInt64, CachedTokens: 15, CacheWriteTokens: 5,
			Models: map[string]ModelSnapshot{" MODEL ": {
				TotalRequests: 30, TotalTokens: math.MaxInt64, CachedTokens: 15, CacheWriteTokens: 5,
				Details: []RequestDetail{detail, detail}, Accounting: []RequestDetail{detail},
				Providers: []ModelProviderStat{{Provider: "claude", TotalRequests: 30, CachedTokens: 15, CacheWriteTokens: 5}},
			}, "empty": {}},
		}},
		RequestsByDay: map[string]int64{"2026-09-10": math.MaxInt64}, RequestsByHour: map[string]int64{"9": 2},
		TokensByDay: map[string]int64{"2026-09-10": math.MaxInt64}, TokensByHour: map[string]int64{"9": 4},
		CostByDay: map[string]float64{"2026-09-10": 1.23456789}, CostByHour: map[string]float64{"9": 2.25},
		CostTokensByDay:  map[string][]TimeSeriesTokenStat{"2026-09-10": {{Model: "MODEL", Provider: "claude", CachedTokens: 15, CacheWriteTokens: 5, TotalTokens: math.MaxInt64}}},
		CostTokensByHour: map[string][]TimeSeriesTokenStat{"9": nil},
	}
	usageJSON, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			// Header AFTER usage catches premature v1/v2 normalization. Unknown
			// future fields must be skipped structurally, without float conversion.
			payload := []byte(`{"usage":` + string(usageJSON) + `,"unknown":{"deep":[{"ignored":1e99999}]},"version":` + strconv.Itoa(version) + `,"generated_at":"2026-09-10T00:00:00Z"}`)
			path := writeSQLiteMigrationSource(t, payload)
			if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
				t.Fatal(err)
			}
			var builder sqliteMigrationTestJSON
			var details, accounting int
			result, err := s.ScanMigrationSource(ctx, path, func(item sqliteMigrationItem) error {
				if item.Start < 0 || item.End < item.Start || item.End > int64(len(payload)) {
					t.Fatalf("invalid source span: %+v", item)
				}
				if item.Kind == "value" && !bytes.Equal(payload[item.Start:item.End], item.Value) {
					t.Fatalf("value span includes punctuation or whitespace: path=%v", item.Path)
				}
				if item.Kind == "value" && len(item.Path) >= 2 {
					switch item.Path[len(item.Path)-2] {
					case "details":
						details++
					case "accounting":
						accounting++
					}
				}
				return builder.consume(item)
			})
			if err != nil || result.Version != version || !result.GeneratedAt.Equal(time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)) || details != 2 || accounting != 1 {
				t.Fatalf("snapshot parse: %+v detail=%d accounting=%d err=%v", result, details, accounting, err)
			}
			var want, got persistedStorageSnapshot
			if err := json.Unmarshal(payload, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(builder.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stream differs from legacy decode: got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestSQLiteMigrationSnapshotDuplicateNullAndCaseSemantics(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	payload := []byte(`{"VERSION":1,"version":null,"generated_at":"2026-09-10T00:00:00Z", "USAGE": {
 "TOTAL_REQUESTS":9223372036854775807,"total_requests":null,"reaſoning_tokens":5,
 "apis":{"API":{"total_requests":4,"models":{"M":{"details":[],"accounting":null}}}},
 "apis":{"API":{"total_requests":3,"models":null}},
 "requests_by_day":{"a":1,"a":2},"cost_tokens_by_day":{"a":[],"b":null}},
 "usage":{"success_count":4,"apis":{"OTHER":{"total_requests":7}}}}`)
	path := writeSQLiteMigrationSource(t, payload)
	if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
		t.Fatal(err)
	}
	var builder sqliteMigrationTestJSON
	result, err := s.ScanMigrationSource(ctx, path, builder.consume)
	if err != nil || result.Version != 1 {
		t.Fatalf("duplicate header: %+v err=%v", result, err)
	}
	var want, got persistedStorageSnapshot
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(builder.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("duplicate/null/map semantics lost: got=%+v want=%+v", got, want)
	}
	if got.Usage.ReasoningTokens != 5 {
		t.Fatal("Unicode case-folding fixture did not preserve the known field")
	}
}

func TestSQLiteMigrationJSONLSeparatesMetadataInvalidAndRealDuplicates(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	record := persistedDetail{API: " API ", Model: "Model", Detail: sqliteTestRecord(1).Detail}
	var payload bytes.Buffer
	payload.WriteString(" \r\n")
	encoder := json.NewEncoder(&payload)
	for _, metadata := range []bool{true, false, false, true} {
		record.MetadataOnly = metadata
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	payload.WriteString("{broken}\nnull\n{\"api\":\"   \"}\n42\n\r\n")
	payload.WriteString(`{"api":"last","detail":{"tokens":{"total_tokens":9223372036854775807}}}`)
	path := writeSQLiteMigrationSource(t, payload.Bytes())
	if _, err := s.StageSource(ctx, path, "jsonl"); err != nil {
		t.Fatal(err)
	}
	want, invalid, err := readPersistedStorageFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []persistedDetail
	var invalidItems int
	result, err := s.ScanMigrationSource(ctx, path, func(item sqliteMigrationItem) error {
		if !bytes.Equal(payload.Bytes()[item.Start:item.End], item.Value) {
			t.Fatal("JSONL raw spans changed")
		}
		if item.Kind == "invalid" {
			invalidItems++
			return nil
		}
		var record persistedDetail
		if err := json.Unmarshal(item.Value, &record); err != nil {
			return err
		}
		got = append(got, record)
		return nil
	})
	if err != nil || result.Requests != 3 || result.Metadata != 2 || result.Invalid != int64(invalid) || invalidItems != invalid {
		t.Fatalf("JSONL classes: %+v old_invalid=%d emitted=%d err=%v", result, invalid, invalidItems, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSONL metadata/duplicate rows changed: got=%+v want=%+v", got, want)
	}
}

func TestSQLiteMigrationSnapshotRejectsMalformedFutureAndTrailingData(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	for _, payload := range []string{
		`{"version":3,"generated_at":"2026-09-10T00:00:00Z","usage":{}}`,
		`{"version":-1,"generated_at":"2026-09-10T00:00:00Z"}`,
		`{"version":2,"generated_at":"bad","usage":{}}`,
		`{"generated_at":"2026-09-10T00:00:00Z","usage":{"total_tokens":9223372036854775808}}`,
		`{"generated_at":"2026-09-10T00:00:00Z","usage":{"apis":[]}}`,
		`{"generated_at":"2026-09-10T00:00:00Z","usage":{}} {}`,
		`{"generated_at":"2026-09-10T00:00:00Z","usage":{}} garbage`,
		`{"generated_at":"2026-09-10T00:00:00Z","usage":{`,
		`null`,
	} {
		path := writeSQLiteMigrationSource(t, []byte(payload))
		if _, err := s.StageSource(ctx, path, "snapshot"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ScanMigrationSource(ctx, path, func(sqliteMigrationItem) error { return nil }); err == nil {
			t.Fatalf("invalid snapshot accepted: %s", payload)
		}
	}
}

type sqliteMigrationCountingReader struct {
	io.Reader
	bytes int
}

func (r *sqliteMigrationCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestSQLiteMigrationSnapshotStreamsBeforeWholeModelAndStopsOnConsumerError(t *testing.T) {
	// Input is deliberately large within ONE model. A Decode(ModelSnapshot)
	// implementation would read all of it before delivering the first item.
	payload := `{"usage":{"apis":{"API":{"models":{"M":{"details":[` + strings.Repeat(`{"failure":"`+strings.Repeat("x", 1024)+`"},`, 20000) + `{}]}}}}},"generated_at":"2026-09-10T00:00:00Z"}`
	reader := &sqliteMigrationCountingReader{Reader: strings.NewReader(payload)}
	sentinel := errors.New("consumer stopped")
	parser := sqliteSnapshotParser{ctx: context.Background(), decoder: json.NewDecoder(reader), consume: func(item sqliteMigrationItem) error {
		if item.Kind == "value" && len(item.Path) > 1 && item.Path[len(item.Path)-2] == "details" {
			return sentinel
		}
		return nil
	}}
	if err := parser.object(nil, "snapshot"); !errors.Is(err, sentinel) {
		t.Fatalf("consumer error lost: %v", err)
	}
	if reader.bytes > 16<<10 {
		t.Fatalf("parser read %d bytes before first record; input=%d", reader.bytes, len(payload))
	}
}

func TestSQLiteMigrationScanCancellationAndChecksumAtEOF(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	for _, format := range []string{"snapshot", "jsonl"} {
		payload := []byte(`{"version":2,"generated_at":"2026-09-10T00:00:00Z","usage":{}}`)
		if format == "jsonl" {
			payload = []byte(`{"api":"test"}`)
		}
		path := writeSQLiteMigrationSource(t, payload)
		if _, err := s.StageSource(ctx, path, format); err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.ScanMigrationSource(canceled, path, func(sqliteMigrationItem) error { t.Fatal("canceled scan delivered data"); return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled scan: %v", err)
		}
		// Still valid JSON but no longer the bytes which were verified.
		payload = bytes.ReplaceAll(payload, []byte("test"), []byte("best"))
		payload = bytes.ReplaceAll(payload, []byte("2026"), []byte("2025"))
		if _, err := s.writer.Exec("UPDATE migration_chunks SET payload=? WHERE source=?", payload, path); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ScanMigrationSource(ctx, path, func(sqliteMigrationItem) error { return nil }); err == nil {
			t.Fatalf("%s decoder stopped without checking EOF checksum", format)
		}
	}
}

func TestSQLiteMigrationRelativePathWorksAcrossStageReadAndScan(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	ctx := context.Background()
	payload := []byte(`{"api":"relative"}`)
	path := writeSQLiteMigrationSource(t, payload)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageSource(ctx, relative, "jsonl"); err != nil {
		t.Fatal(err)
	}
	assertSQLiteStagedBytes(t, s, relative, payload)
	result, err := s.ScanMigrationSource(ctx, relative, func(sqliteMigrationItem) error { return nil })
	if err != nil || result.Requests != 1 {
		t.Fatalf("relative source lookup failed: %+v err=%v", result, err)
	}
}
