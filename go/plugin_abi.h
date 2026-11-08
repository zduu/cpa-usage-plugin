#ifndef CPA_USAGE_PLUGIN_ABI_H
#define CPA_USAGE_PLUGIN_ABI_H

#include <stdint.h>
#include <stdlib.h>

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

int cliproxy_plugin_init(cliproxy_host_api*, cliproxy_plugin_api*);

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

#if defined(__APPLE__) && defined(__x86_64__)
int cliproxyPluginInit(cliproxy_host_api*, cliproxy_plugin_api*);
int cliproxy_bridge_call(char*, uint8_t*, size_t, cliproxy_buffer*);
void cliproxy_bridge_free(void*, size_t);
void cliproxy_bridge_shutdown(void);
int cliproxy_bridge_host_call(cliproxy_host_call_fn, void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
void cliproxy_bridge_host_free(cliproxy_host_free_fn, void*, size_t);
#endif

#endif
