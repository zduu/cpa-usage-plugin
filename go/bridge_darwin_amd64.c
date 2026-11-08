#include "plugin_abi.h"

// Darwin amd64 Go runtimes all use GS:0x30 for the current goroutine.
// A cgo callback would otherwise adopt the other runtime's g/m and heap.
// Keep each runtime's g0 per OS thread, and restore the caller before its
// generated cgo wrapper resumes Go. Go's own pthread destructor releases
// borrowed machines when the thread exits; these caches own no Go objects.
static _Thread_local void* plugin_g;
static _Thread_local void* host_g;

static void* current_g(void) {
    void* g;
    __asm__ volatile("movq %%gs:0x30, %0" : "=r"(g) : : "memory");
    return g;
}

static void set_g(void* g) {
    __asm__ volatile("movq %0, %%gs:0x30" : : "r"(g) : "memory");
}

static void* enter_plugin(void) {
    void* caller = current_g();
    host_g = caller;
    set_g(plugin_g);
    return caller;
}

static void leave_plugin(void* caller) {
    plugin_g = current_g();
    set_g(caller);
}

static void* enter_host(void) {
    void* caller = current_g();
    plugin_g = caller;
    set_g(host_g);
    return caller;
}

static void leave_host(void* caller) {
    host_g = current_g();
    set_g(caller);
}

int cliproxy_plugin_init(cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
    void* caller = enter_plugin();
    int status = cliproxyPluginInit(host, plugin);
    leave_plugin(caller);
    return status;
}

int cliproxy_bridge_call(char* method, uint8_t* request, size_t len, cliproxy_buffer* response) {
    void* caller = enter_plugin();
    int status = cliproxyPluginCall(method, request, len, response);
    leave_plugin(caller);
    return status;
}

void cliproxy_bridge_free(void* ptr, size_t len) {
    void* caller = enter_plugin();
    cliproxyPluginFree(ptr, len);
    leave_plugin(caller);
}

void cliproxy_bridge_shutdown(void) {
    void* caller = enter_plugin();
    cliproxyPluginShutdown();
    leave_plugin(caller);
}

int cliproxy_bridge_host_call(cliproxy_host_call_fn call, void* ctx, const char* method,
                              const uint8_t* request, size_t len, cliproxy_buffer* response) {
    void* caller = enter_host();
    int status = call(ctx, method, request, len, response);
    leave_host(caller);
    return status;
}

void cliproxy_bridge_host_free(cliproxy_host_free_fn release, void* ptr, size_t len) {
    void* caller = enter_host();
    release(ptr, len);
    leave_host(caller);
}
