"""Exercise the real C ABI using an in-process fake CPA host and no real credentials."""
import base64
import ctypes as c
import json
from pathlib import Path
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


class Buffer(c.Structure):
    _fields_ = [("ptr", c.c_void_p), ("len", c.c_size_t)]


HostCall = c.CFUNCTYPE(c.c_int, c.c_void_p, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Free = c.CFUNCTYPE(None, c.c_void_p, c.c_size_t)
PluginCall = c.CFUNCTYPE(c.c_int, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Shutdown = c.CFUNCTYPE(None)


class Host(c.Structure):
    _fields_ = [("abi", c.c_uint32), ("ctx", c.c_void_p), ("call", HostCall), ("free", Free)]


class Plugin(c.Structure):
    _fields_ = [("abi", c.c_uint32), ("call", PluginCall), ("free", Free), ("shutdown", Shutdown)]


def main(library):
    # Import without running the builder, so version assertions use the same source.
    from build_plugin import PLUGIN_ID, source_version
    loaded = c.CDLL(str(Path(library).resolve()))
    loaded.cliproxy_plugin_init.argtypes = [c.POINTER(Host), c.POINTER(Plugin)]
    loaded.cliproxy_plugin_init.restype = c.c_int
    buffers, executions, failures = {}, [], []

    @HostCall
    def callback(ctx, method, body, size, output):
        try:
            request = json.loads(c.string_at(body, size))
            if method == b"host.auth.list":
                result = {"files": [{"id": "native-account", "provider": "codex", "name": "Native smoke"}]}
            elif method == b"host.model.execute":
                assert request["auth_id"] == "native-account" and request["forced_provider"] == "codex"
                assert request["entry_protocol"] == request["exit_protocol"] == "openai"
                assert json.loads(base64.b64decode(request["body"]))["model"] == "smoke-model"
                executions.append(request)
                result = {"status_code": 200, "body": base64.b64encode(b'{"choices":[{"message":{"content":"OK"}}]}').decode()}
            else:
                raise AssertionError("Unexpected host call: " + method.decode())
            payload = json.dumps({"ok": True, "result": result}).encode()
            buffer = c.create_string_buffer(payload)
            address = c.addressof(buffer)
            buffers[address] = buffer
            output.contents.ptr, output.contents.len = address, len(payload)
            return 0
        except BaseException as error:
            failures.append(str(error))
            return 1

    @Free
    def release(pointer, size):
        buffers.pop(pointer, None)

    plugin = Plugin()
    host = Host(1, None, callback, release)
    assert loaded.cliproxy_plugin_init(None, c.byref(plugin)) != 0
    host.abi = 0
    assert loaded.cliproxy_plugin_init(c.byref(host), c.byref(plugin)) != 0
    host.abi = 1
    assert loaded.cliproxy_plugin_init(c.byref(host), c.byref(plugin)) == 0
    assert plugin.abi == 1
    for method, pointer, size in [(None, None, 0), (b"plugin.register", None, 1), (b"plugin.register", None, 2**21 + 1)]:
        reply = Buffer()
        assert plugin.call(method, pointer, size, c.byref(reply)) != 0
        assert not reply.ptr

    def rpc(method, request):
        payload = json.dumps(request).encode()
        body, reply = c.create_string_buffer(payload), Buffer()
        assert plugin.call(method.encode(), body, len(payload), c.byref(reply)) == 0
        try:
            response = json.loads(c.string_at(reply.ptr, reply.len))
        finally:
            plugin.free(reply.ptr, reply.len)
        assert response["ok"], response
        return response["result"]

    prefix = "/v0/management/plugins/" + PLUGIN_ID + "/api/"

    def endpoint(method, path, value=None, expected=200):
        response = rpc("management.handle", {"Method": method, "Path": prefix + path,
                       "Body": base64.b64encode(json.dumps(value).encode()).decode() if value is not None else ""})
        assert response["StatusCode"] == expected, response
        return json.loads(base64.b64decode(response["Body"]))

    with tempfile.TemporaryDirectory() as directory:
        try:
            registration = rpc("plugin.register", {"schema_version": 6,
                "config_yaml": base64.b64encode(("state_dir: " + directory.replace("\\", "/")).encode()).decode()})
            assert registration["metadata"]["Version"] == source_version()
            assert registration["capabilities"] == {"management_api": True}
            assert len(rpc("management.register", {})["routes"]) == 7
            resource = rpc("management.handle", {"Method": "GET", "Path": "/v0/resource/plugins/" + PLUGIN_ID + "/status"})
            assert base64.b64decode(resource["Body"]) == (ROOT / "src/web/index.html").read_bytes()
            assert endpoint("GET", "accounts")["accounts"][0]["id"] == "native-account"
            config = endpoint("GET", "config")
            assert not config["accounts"]
            config["accounts"] = [{"provider": "native-test", "source": "cpa", "auth_id": "native-account",
                                   "cpa_provider": "codex", "model": "smoke-model", "prompt": "hello"}]
            assert endpoint("PUT", "config", config)["config"]["accounts"][0]["auth_id"] == "native-account"
            assert endpoint("POST", "run")["results"][0]["success"]
            assert len(executions) == 1 and not failures, failures
            assert Path(directory, "config.yaml").is_file() and Path(directory, "state.json").is_file()
            rpc("plugin.quiesce", {})
            endpoint("POST", "run", expected=503)
            assert not buffers, "Host reply buffers were not released"
        finally:
            plugin.shutdown()
    print("Native C ABI smoke passed")


if __name__ == "__main__":
    main(sys.argv[1])
