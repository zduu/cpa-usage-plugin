package main

/*
#include <stdint.h>
#include <stdlib.h>
#if defined(__APPLE__)
#include <dlfcn.h>
#endif

// Go runtime threads and signal handlers outlive the plugin shutdown callback.
// Darwin otherwise unmaps their code when the host calls dlclose. Keep one
// reference per image for the process lifetime; plugin workers still shut down.
static int pin_go_runtime_image(void) {
#if defined(__APPLE__)
	static void* pinned_image;
	if (pinned_image != NULL) return 1;
	Dl_info info;
	if (dladdr((void*)&pin_go_runtime_image, &info) == 0) return 0;
	pinned_image = dlopen(info.dli_fname, RTLD_NOW | RTLD_LOCAL | RTLD_NODELETE);
	return pinned_image != NULL;
#else
	return 1;
#endif
}

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"
)

func main() {}

var pluginLifecycle struct {
	sync.Mutex
	stopped bool
}

// A pinned shared library can be initialized again without rerunning Go init.
// Recreate closed services so re-enabling it behaves like a fresh process.
func restartPluginState() {
	pluginLifecycle.Lock()
	defer pluginLifecycle.Unlock()
	if !pluginLifecycle.stopped {
		return
	}
	stats = NewRequestStatistics()
	dashboardExportJobs = newDashboardExportJobManager()
	requestMetadata = newRequestMetadataCache()
	authIndexes = newAuthIndexLearner()
	usageFallbacks = nil
	apiKeySalt = defaultAPIKeyHashSalt
	pluginLifecycle.stopped = false
}

// callQuotaHostAuth validates a submitted observation against the live host.
func callQuotaHostAuth(index string) (quotaHostAuth, error) {
	var auth quotaHostAuth
	raw, _ := json.Marshal(map[string]string{"auth_index": index})
	method := C.CString("host.auth.get_runtime")
	defer C.free(unsafe.Pointer(method))
	request := C.CBytes(raw)
	defer C.free(request)
	var response C.cliproxy_buffer
	status := C.call_host_api(method, (*C.uint8_t)(request), C.size_t(len(raw)), &response)
	defer C.free_host_buffer(response.ptr, response.len)
	if status != 0 || response.ptr == nil || response.len > 4*1024*1024 {
		return auth, fmt.Errorf("host credential lookup failed")
	}
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Auth quotaHostAuth `json:"auth"`
		} `json:"result"`
	}
	if err := json.Unmarshal(C.GoBytes(response.ptr, C.int(response.len)), &result); err != nil || !result.OK {
		return auth, fmt.Errorf("invalid host credential response")
	}
	return result.Result.Auth, nil
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	if C.pin_go_runtime_image() == 0 {
		return 1
	}
	restartPluginState()
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBody []byte
	if request != nil && requestLen > 0 {
		requestBody = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, errHandle := handleMethod(C.GoString(method), requestBody)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	pluginLifecycle.Lock()
	defer pluginLifecycle.Unlock()
	if pluginLifecycle.stopped {
		return
	}
	if dashboardExportJobs != nil {
		dashboardExportJobs.close()
	}
	if usageFallbacks != nil {
		usageFallbacks.Flush()
	}
	if stats != nil {
		stats.Close()
	}
	pluginLifecycle.stopped = true
}

func handleMethod(method string, requestBody []byte) ([]byte, error) {
	switch method {
	case "plugin.register":
		return handleRegister(requestBody)
	case "plugin.reconfigure":
		return handleReconfigure(requestBody)
	case "management.register":
		return handleManagementRegister()
	case "usage.handle":
		return handleUsage(requestBody)
	case "request.intercept_before", "request.intercept_after", "request.complete":
		return handleRequestMetadata(method, requestBody)
	case "response.intercept_after":
		return okEnvelopeJSON("{}")
	case "response.intercept_stream_chunk":
		return okEnvelopeJSON("{}")
	case "management.handle":
		return handleManagement(requestBody)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func mustMarshal(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}
