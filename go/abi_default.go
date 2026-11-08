//go:build !darwin || !amd64

package main

/*
#include "plugin_abi.h"
*/
import "C"

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	return initializePlugin(host, plugin)
}
