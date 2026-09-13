package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Generate valid but non-canonical legacy JSON. Duplicate members exercise
// Go's typed merge/replacement rules; the independent oracle is json.Unmarshal
// on the ORIGINAL document, not the streaming parser's events or a map[string]any.
func sqliteProjectionFuzzDocument(data []byte) string {
	if len(data) == 0 {
		data = []byte{0}
	}
	if len(data) > 96 {
		data = data[:96]
	}
	modelFields := []string{
		`"details":[{"model":"old","tokens":{"input_tokens":7,"total_tokens":9007199254740993},"headers":{"keep":[]}}, {"source":"tail"}]`,
		`"details":[{"tokens":{"output_tokens":2},"headers":{"x":[null]}}]`,
		`"DETAILS":[null,{}, {"timestamp":"2500-01-01T00:00:00+05:45","stream":true}]`,
		`"details":[]`, `"details":null`,
		`"accounting":[{"latency_ms":9},{"correlation":{"schema_version":1,"input_mode":"native"}}]`,
		`"accounting":[null,{"correlation":{"output_mode":"native"}}]`,
		`"accounting":[]`, `"accounting":null`,
		`"providers":[{"provider":"claude","total_requests":5,"cached_tokens":15},{"provider":"tail"}]`,
		`"providers":[{"cache_write_tokens":5}]`, `"providers":[{},null,{}]`,
		`"providers":[]`, `"providers":null`,
		`"total_requests":9223372036854775807`, `"total_requests":null`,
		`"reaſoning_tokens":-2`, `"avg_latency_ms":-0`,
	}
	var fields strings.Builder
	for i, choice := range data {
		if i > 0 {
			fields.WriteByte(',')
		}
		fields.WriteString(modelFields[int(choice)%len(modelFields)])
	}
	usageFields := []string{
		`"requests_by_day":{"a":7,"b":8},"requests_by_day":{"a":null,"c":-3}`,
		`"cost_by_day":{"x":1.125},"cost_by_day":null,"cost_by_day":{}`,
		`"cost_tokens_by_hour":{"x":[{"model":"old","total_tokens":7}]},"cost_tokens_by_hour":{"x":[{}],"nil":null,"empty":[]}`,
		`"success_count":7,"success_count":null,"ſuccess_count":-1`,
		`"apis":{"old":{"models":{"M":{}}}},"apis":null,"apis":{}`,
		`"apis":{"API":{"total_requests":7},"API":{"failure_count":2},"api":{"models":null}}`,
		`"tokens_by_hour":{"01":9223372036854775807,"1":1,"\u0000":2,"":0}`,
	}
	return projectionSnapshotJSON(fmt.Sprintf(`"usage":{%s},"usage":null,"usage":{"apis":{"API":{"models":{"M":{%s},"m":{"details":[]},"":{"accounting":null}}}}},"USAGE":{"cached_tokens":15,"cache_write_tokens":5},"version":%d,"version":null`, usageFields[int(data[0])%len(usageFields)], fields.String(), int(data[len(data)-1])%3))
}

func TestSQLiteProjectionGeneratedDocumentsMatchLegacyDecode(t *testing.T) {
	s, _ := testSQLiteLedger(t)
	rng := rand.New(rand.NewSource(20260913))
	for i := 0; i < 128; i++ {
		data := make([]byte, 1+rng.Intn(96))
		if _, err := rng.Read(data); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			compareProjectedSnapshotForTest(t, s, sqliteProjectionFuzzDocument(data))
		})
	}
}

func FuzzSQLiteProjectionMatchesLegacyDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17})
	f.Add([]byte{0, 1, 2, 1, 2, 1, 2})
	f.Add([]byte{0, 1, 3, 2, 4, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		s, _ := testSQLiteLedger(t)
		compareProjectedSnapshotForTest(t, s, sqliteProjectionFuzzDocument(data))
	})
}
