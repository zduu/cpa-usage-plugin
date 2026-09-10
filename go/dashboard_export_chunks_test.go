package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestExportChunksRoundTripAndVersionChecks(t *testing.T) {
	for _, payload := range [][]byte{nil, bytes.Repeat([]byte("中文\x00🙂\xff"), 50000)} {
		path := filepath.Join(t.TempDir(), "export")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		job := dashboardExportJob{ID: "chunk-test", FilePath: path, BodyBytes: len(payload), ETag: fmt.Sprintf(`W/"%x"`, sha256.Sum256(payload))}
		query := map[string][]string{"offset": {"0"}, "version": {job.ETag}}
		var restored []byte
		for offset := 0; offset == 0 || offset < len(payload); {
			query["offset"] = []string{strconv.Itoa(offset)}
			raw, err := dashboardExportJobChunk(job, query)
			if err != nil {
				t.Fatal(err)
			}
			var chunk dashboardExportChunk
			response := decodeManagementResponse(t, raw, &chunk)
			if response.StatusCode != http.StatusOK || chunk.Offset != int64(offset) || chunk.Total != int64(len(payload)) || chunk.ETag != job.ETag {
				t.Fatalf("invalid range response: status=%d chunk=%+v", response.StatusCode, chunk)
			}
			if len(chunk.Data) > dashboardExportChunkBytes || chunk.Checksum != fmt.Sprintf("%08x", crc32.ChecksumIEEE(chunk.Data)) {
				t.Fatal("chunk exceeds budget or checksum does not match")
			}
			// Repeating an offset must produce the same immutable bytes.
			repeated, err := dashboardExportJobChunk(job, query)
			if err != nil || !bytes.Equal(raw, repeated) {
				t.Fatal("retry changed a chunk")
			}
			restored = append(restored, chunk.Data...)
			offset += len(chunk.Data)
			if len(chunk.Data) == 0 {
				break
			}
		}
		if !bytes.Equal(restored, payload) {
			t.Fatal("chunk round trip changed binary export")
		}
		for _, invalid := range []struct {
			offset, length, version string
			status                  int
		}{
			{"0", "1", "wrong-job-version", 412},
			{"0", "1", "", 412},
			{"-1", "1", job.ETag, 400},
			{"9223372036854775808", "1", job.ETag, 400},
			{"0", "0", job.ETag, 400},
			{"0", "262145", job.ETag, 400},
			{strconv.Itoa(len(payload) + 1), "1", job.ETag, 416},
		} {
			raw, err := dashboardExportJobChunk(job, map[string][]string{"offset": {invalid.offset}, "length": {invalid.length}, "version": {invalid.version}})
			if err != nil {
				t.Fatal(err)
			}
			if response := decodeManagementResponse(t, raw, nil); response.StatusCode != invalid.status {
				t.Fatalf("invalid request %+v: status %d", invalid, response.StatusCode)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		query["offset"] = []string{"0"}
		raw, _ := dashboardExportJobChunk(job, query)
		if response := decodeManagementResponse(t, raw, nil); response.StatusCode != http.StatusGone {
			t.Fatalf("deleted file download status=%d", response.StatusCode)
		}
	}
}
