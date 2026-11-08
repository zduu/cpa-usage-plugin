package main

/*
#include "plugin_abi.h"
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

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
#if defined(__APPLE__) && defined(__x86_64__)
	return cliproxy_bridge_host_call(stored_host->call, stored_host->host_ctx, method, request, request_len, response);
#else
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
#endif
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
#if defined(__APPLE__) && defined(__x86_64__)
		cliproxy_bridge_host_free(stored_host->free_buffer, ptr, len);
#else
		stored_host->free_buffer(ptr, len);
#endif
	}
}

static void set_plugin_api(cliproxy_plugin_api* plugin, uint32_t version) {
	plugin->abi_version = version;
#if defined(__APPLE__) && defined(__x86_64__)
	plugin->call = cliproxy_bridge_call;
	plugin->free_buffer = cliproxy_bridge_free;
	plugin->shutdown = cliproxy_bridge_shutdown;
#else
	plugin->call = cliproxyPluginCall;
	plugin->free_buffer = cliproxyPluginFree;
	plugin->shutdown = cliproxyPluginShutdown;
#endif
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
	sync.RWMutex
	stopped bool
}

// A pinned shared library can be initialized again without rerunning Go init.
// Recreate closed services so re-enabling it behaves like a fresh process.
func restartPluginState() {
	pluginLifecycle.Lock()
	defer pluginLifecycle.Unlock()
	restartPluginStateLocked()
}

func restartPluginStateLocked() {
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

func initializePlugin(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	if C.pin_go_runtime_image() == 0 {
		return 1
	}
	restartPluginState()
	C.store_host_api(host)
	C.set_plugin_api(plugin, C.uint32_t(abiVersion))
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
	stopPluginStateLocked()
}

// Quiesce and final shutdown share the same transition. A retired image can
// remain mapped, but must never save its old statistics after a successor runs.
func stopPluginStateLocked() {
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
	case "plugin.quiesce":
		pluginLifecycle.Lock()
		defer pluginLifecycle.Unlock()
		stopPluginStateLocked()
		return okEnvelopeJSON("{}")
	case "plugin.register", "plugin.reconfigure":
		pluginLifecycle.Lock()
		defer pluginLifecycle.Unlock()
		// CPA rolls back a failed replacement by reconfiguring the quiesced
		// image without calling its ABI init entry point again.
		restartPluginStateLocked()
		if method == "plugin.register" {
			return handleRegister(requestBody)
		}
		return handleReconfigure(requestBody)
	}
	// Wait for admitted calls before closing workers, and reject late calls
	// until an explicit register/reconfigure restores this image.
	pluginLifecycle.RLock()
	defer pluginLifecycle.RUnlock()
	if pluginLifecycle.stopped {
		return errorEnvelope("plugin_stopped", "plugin is quiesced or shut down"), nil
	}
	switch method {
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
