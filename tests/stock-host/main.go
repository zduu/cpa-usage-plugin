// This smoke test loads the actual shared library and serializes callbacks
// using the unmodified, pinned CPA SDK. No CPA checkout is built or changed.
package main

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
typedef struct { void *ptr; size_t len; } buffer;
typedef struct {
  uint32_t version;
  int (*call)(const char *, const uint8_t *, size_t, buffer *);
  void (*free_buffer)(void *, size_t);
  void (*shutdown)(void);
} plugin_api;
static plugin_api api;
static int load_plugin(const char *path) {
  void *lib = dlopen(path, RTLD_NOW | RTLD_LOCAL);
  if (!lib) return 1;
  int (*init)(void *, plugin_api *) = dlsym(lib, "cliproxy_plugin_init");
  if (!init) return 2;
  return init(NULL, &api);
}
static int invoke(const char *method, const uint8_t *body, size_t len, buffer *out) {
  return api.call(method, body, len, out);
}
static void release_buffer(buffer b) { api.free_buffer(b.ptr, b.len); }
static void shutdown_plugin(void) { api.shutdown(); }
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func call(method string, request any, result any) {
	raw, err := json.Marshal(request)
	check(err)
	m := C.CString(method)
	b := C.CBytes(raw)
	defer C.free(unsafe.Pointer(m))
	defer C.free(b)
	var out C.buffer
	status := C.invoke(m, (*C.uint8_t)(b), C.size_t(len(raw)), &out)
	defer C.release_buffer(out)
	response := C.GoBytes(out.ptr, C.int(out.len))
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	check(json.Unmarshal(response, &env))
	if status != 0 || !env.OK {
		panic(fmt.Sprintf("%s: %s", method, response))
	}
	check(json.Unmarshal(env.Result, result))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		panic("usage: go run . /absolute/path/to/plugin.so [expected-version]")
	}
	path := C.CString(os.Args[1])
	defer C.free(unsafe.Pointer(path))
	if C.load_plugin(path) != 0 {
		panic("cannot load plugin")
	}
	defer C.shutdown_plugin()
	var registration struct {
		Metadata     pluginapi.Metadata
		Capabilities map[string]bool
	}
	call("plugin.register", map[string]any{"config_yaml": []byte("exchange_rate_enabled: false\nmodels_dev_prices_enabled: false\n")}, &registration)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(registration.Metadata.Version) ||
		(len(os.Args) == 3 && registration.Metadata.Version != os.Args[2]) || !registration.Capabilities["request_interceptor"] ||
		!registration.Capabilities["request_lifecycle_plugin"] || registration.Capabilities["response_interceptor"] ||
		registration.Capabilities["response_stream_interceptor"] {
		panic("incorrect registration")
	}
	paths := []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1beta/models/gemini:streamGenerateContent"}
	for i, endpoint := range paths {
		id := fmt.Sprintf("request-%d", i)
		req := pluginapi.RequestInterceptRequest{RequestID: id, Model: "model", RequestedModel: "model", Stream: i%2 == 0,
			Headers: http.Header{"Authorization": {"Bearer test-client"}}, Body: []byte(`{"model":"model"}`),
			Metadata: map[string]any{"selected_auth_id": "auth", "selected_auth_index": "index", "request_path": endpoint}}
		for _, method := range []string{"request.intercept_before", "request.intercept_after"} {
			var response map[string]any
			call(method, req, &response)
			if len(response) != 0 {
				panic("request was modified")
			}
		}
		at := time.Now()
		usage := pluginapi.UsageRecord{Provider: "test", Model: "model", Alias: "model", AuthID: "auth", AuthIndex: "index",
			RequestedAt: at, Latency: time.Millisecond, TTFT: time.Microsecond, Failed: i == 2,
			Detail: pluginapi.UsageDetail{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}
		// Verify the test is not accidentally relying on a patched SDK.
		var fields map[string]any
		raw, err := json.Marshal(usage)
		check(err)
		check(json.Unmarshal(raw, &fields))
		for _, key := range []string{"Endpoint", "Stream", "RequestID"} {
			if _, exists := fields[key]; exists {
				panic("SDK unexpectedly contains " + key)
			}
		}
		completion := pluginapi.RequestCompletion{RequestID: id, StartedAt: at, CompletedAt: time.Now(), Outcome: pluginapi.RequestCompletionSucceeded}
		if i == 2 {
			completion.Outcome = pluginapi.RequestCompletionCanceled
		}
		var empty map[string]any
		if i%2 == 0 {
			call("request.complete", completion, &empty)
		}
		call("usage.handle", usage, &empty)
		if i%2 != 0 {
			call("request.complete", completion, &empty)
		}
	}
	for _, window := range []string{"all", "24h"} {
		var response pluginapi.ManagementResponse
		call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events", Query: url.Values{"range": {window}}}, &response)
		if response.StatusCode != 200 {
			panic(fmt.Sprintf("events status %d", response.StatusCode))
		}
		var payload struct {
			Events []struct {
				Endpoint string
				Stream   bool
				Failed   bool
			}
		}
		check(json.Unmarshal(response.Body, &payload))
		if len(payload.Events) != len(paths) {
			panic(fmt.Sprintf("wrong event count: %s", response.Body))
		}
		seen := make(map[string]bool)
		failures := 0
		for _, event := range payload.Events {
			seen[event.Endpoint] = event.Stream
			if event.Failed {
				failures++
			}
		}
		if failures != 1 {
			panic("failure accounting changed")
		}
		for i, endpoint := range paths {
			if stream, ok := seen[endpoint]; !ok || stream != (i%2 == 0) {
				panic(fmt.Sprintf("metadata missing: %s", response.Body))
			}
		}
		etag := ""
		for name, values := range response.Headers {
			if strings.EqualFold(name, "ETag") && len(values) > 0 {
				etag = values[0]
			}
		}
		if etag == "" {
			panic("missing event ETag")
		}
		call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events",
			Query: url.Values{"range": {window}}, Headers: http.Header{"If-None-Match": {etag}}}, &response)
		if response.StatusCode != http.StatusNotModified || len(response.Body) != 0 {
			panic("304 ABI response lost")
		}
	}
	var missing pluginapi.ManagementResponse
	call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events-export-jobs", Query: url.Values{"id": {"missing"}}}, &missing)
	if missing.StatusCode != http.StatusNotFound {
		panic("404 ABI response lost")
	}
	verifyExportChunks(len(paths))
	fmt.Println("PASS: stock CPA v7.2.152 SDK -> shared-library ABI -> dashboard; 4 native records, paths, stream flags, cancellation, both callback orders, all/24h")
}

// Exercise the real ABI envelope and byte/base64 conversion, not a direct
// call into the export encoder. Small chunks split JSON and UTF-8 boundaries.
func verifyExportChunks(expected int) {
	type exportJob struct {
		ID, Status, ETag string
		JSONRows         bool `json:"json_rows"`
		BodyBytes        int  `json:"body_bytes"`
		Exported         int
	}
	var response pluginapi.ManagementResponse
	call("management.handle", pluginapi.ManagementRequest{Method: "POST", Path: "/dashboard-events-export-jobs",
		Query: url.Values{"format": {"json"}, "json_rows": {"true"}}}, &response)
	if response.StatusCode != http.StatusAccepted {
		panic("export job was not accepted")
	}
	var job exportJob
	check(json.Unmarshal(response.Body, &job))
	if job.ID == "" {
		panic("export job ID missing")
	}
	defer func() {
		call("management.handle", pluginapi.ManagementRequest{Method: "DELETE", Path: "/dashboard-events-export-jobs", Query: url.Values{"id": {job.ID}}}, &response)
		if response.StatusCode != http.StatusOK {
			panic("export cleanup failed")
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for job.Status != "succeeded" {
		if time.Now().After(deadline) || job.Status == "failed" {
			panic("export job did not complete")
		}
		time.Sleep(10 * time.Millisecond)
		call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events-export-jobs", Query: url.Values{"id": {job.ID}}}, &response)
		if response.StatusCode != http.StatusOK {
			panic("export polling failed")
		}
		check(json.Unmarshal(response.Body, &job))
	}
	if !job.JSONRows || job.Exported != expected || job.BodyBytes <= 0 {
		panic("export negotiation or counters changed")
	}
	var body []byte
	for offset := 0; offset < job.BodyBytes; {
		query := url.Values{"id": {job.ID}, "chunk": {"1"}, "offset": {strconv.Itoa(offset)}, "length": {"37"}, "version": {job.ETag}}
		call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events-export-download", Query: query}, &response)
		if response.StatusCode != http.StatusOK {
			panic("export chunk failed")
		}
		var chunk struct {
			Offset, Total int
			ETag          string
			Checksum      string `json:"checksum_crc32"`
			Data          []byte
		}
		check(json.Unmarshal(response.Body, &chunk))
		if chunk.Offset != offset || chunk.Total != job.BodyBytes || chunk.ETag != job.ETag || len(chunk.Data) != min(37, job.BodyBytes-offset) || chunk.Checksum != fmt.Sprintf("%08x", crc32.ChecksumIEEE(chunk.Data)) {
			panic("export chunk integrity failed")
		}
		body = append(body, chunk.Data...)
		offset += len(chunk.Data)
	}
	var rows []json.RawMessage
	check(json.Unmarshal(body, &rows))
	if len(rows) != expected {
		panic("array export lost records")
	}
	call("management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/dashboard-events-export-download",
		Query: url.Values{"id": {job.ID}, "chunk": {"1"}, "offset": {"0"}, "version": {"wrong"}}}, &response)
	if response.StatusCode != http.StatusPreconditionFailed {
		panic("412 ABI response lost")
	}
	fmt.Println("PASS: negotiated JSON array, chunk offsets, file version, CRC32, record count and 412 through stock CPA ABI")
}
