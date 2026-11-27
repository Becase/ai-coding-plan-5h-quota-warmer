/* CPA ABI 1, based on the official host-model-callback example.
 * Source: CLIProxyAPI 0db19025571684b198a2ef508f99b41c717337e8.
 * MIT notice: web/third-party-notices.txt (also embedded in the library).
 */
#ifndef QUOTA_WARMER_ABI_H
#define QUOTA_WARMER_ABI_H
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
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

/* Copy the table: the host's initialization stack frame is temporary. */
static cliproxy_host_api warmer_host;
static int warmer_bind(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
    if (!host || !plugin || host->abi_version != 1 || !host->call || !host->free_buffer) return 1;
    warmer_host = *host;
    plugin->abi_version = 1;
    plugin->call = cliproxyPluginCall;
    plugin->free_buffer = cliproxyPluginFree;
    plugin->shutdown = cliproxyPluginShutdown;
    return 0;
}
static int warmer_host_call(const char* method, const uint8_t* body, size_t size, cliproxy_buffer* reply) {
    if (!warmer_host.call) return 1;
    return warmer_host.call(warmer_host.host_ctx, method, body, size, reply);
}
static void warmer_host_release(cliproxy_buffer reply) {
    if (reply.ptr && warmer_host.free_buffer) warmer_host.free_buffer(reply.ptr, reply.len);
}
#endif
