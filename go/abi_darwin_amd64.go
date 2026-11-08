package main

/*
#include "plugin_abi.h"
*/
import "C"

// The public entry point is a C shim: runtime state must be switched before
// the generated cgo callback enters this runtime.
//
//export cliproxyPluginInit
func cliproxyPluginInit(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	return initializePlugin(host, plugin)
}
