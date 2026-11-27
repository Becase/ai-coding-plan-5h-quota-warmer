//go:build cpa_plugin && cgo

package main

/*
#include "plugin_abi.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"sync"
	"unsafe"
)

var nativePlugin struct {
	sync.RWMutex
	app *pluginApp
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	nativePlugin.Lock()
	defer nativePlugin.Unlock()
	if nativePlugin.app != nil {
		return 1
	}
	if status := C.warmer_bind(host, plugin); status != 0 {
		return status
	}
	nativePlugin.app = &pluginApp{host: callbackHost{}}
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, reply *C.cliproxy_buffer) (status C.int) {
	if reply == nil {
		return 1
	}
	*reply = C.cliproxy_buffer{}
	defer func() {
		if recover() != nil {
			setPluginReply(reply, []byte(`{"ok":false,"error":{"code":"panic","message":"plugin panic"}}`))
			status = 1
		}
	}()
	if method == nil || size > 2*1024*1024 || (size != 0 && request == nil) {
		return 1
	}
	nativePlugin.RLock()
	app := nativePlugin.app
	nativePlugin.RUnlock()
	if app == nil {
		return 1
	}
	setPluginReply(reply, app.Call(C.GoString(method), C.GoBytes(unsafe.Pointer(request), C.int(size))))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(pointer unsafe.Pointer, _ C.size_t) { C.free(pointer) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	nativePlugin.RLock()
	app := nativePlugin.app
	nativePlugin.RUnlock()
	if app != nil {
		app.Close()
	}
}

func setPluginReply(reply *C.cliproxy_buffer, body []byte) {
	reply.ptr = C.CBytes(body)
	reply.len = C.size_t(len(body))
}

type callbackHost struct{}

func (callbackHost) Call(method string, request interface{}, result interface{}) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	name, body := C.CString(method), C.CBytes(payload)
	defer C.free(unsafe.Pointer(name))
	defer C.free(body)
	var reply C.cliproxy_buffer
	status := C.warmer_host_call(name, (*C.uint8_t)(body), C.size_t(len(payload)), &reply)
	defer C.warmer_host_release(reply)
	if reply.ptr == nil || reply.len == 0 || reply.len > 16*1024*1024 {
		return errors.New("invalid CPA response buffer")
	}
	return decodeHostReply(C.GoBytes(reply.ptr, C.int(reply.len)), status != 0, result)
}
