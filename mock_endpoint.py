# 本地验证用的 mock 端点：模拟 OpenAI 兼容接口，POST 一律返回 200。
# 用法：python mock_endpoint.py [端口]（默认 8000）
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length)
        payload = {
            "id": "mock-1",
            "model": "mock-model",
            "choices": [{"message": {"role": "assistant", "content": "ok"}}],
            "echoed_path": self.path,
        }
        if body:
            try:
                decoded = json.loads(body)
                if isinstance(decoded, dict):
                    payload["echoed_model"] = decoded.get("model")
            except ValueError:
                pass
        raw = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        raw = b'{"status":"mock"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, format, *args):
        sys.stderr.write("[mock] %s\n" % (format % args))


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8000
    print(f"mock endpoint listening on http://127.0.0.1:{port}")
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
