"""Start a local test container and verify the image before publishing it."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def docker(*arguments, check=True):
    return subprocess.run(["docker", *arguments], text=True, encoding="utf-8", errors="replace", capture_output=True, check=check, timeout=30)


def verify(image, platform):
    name = "quota-warmer-smoke-" + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix="quota-warmer-image-") as directory:
        config = Path(directory, "config.yaml")
        config.write_text('''timezone: Asia/Shanghai
state_file: data/state.json
schedules: []
accounts:
  - provider: image-smoke
    api_url: http://127.0.0.1:9/v1
    api_format: openai
    api_key: image-smoke-api-key
    model: image-smoke-model
    prompt: hello
''', encoding="utf-8")
        config.chmod(0o644)
        try:
            docker("run", "--detach", "--name", name, "--platform", platform,
                   "--mount", f"type=bind,source={config.resolve()},target=/app/config.yaml,readonly",
                   "--env", "QUOTA_WARMER_MANAGEMENT_KEY=image-smoke-management-key", image)
            deadline = time.monotonic() + 30
            while True:
                result = docker("exec", name, "wget", "-qO-", "http://127.0.0.1:1688/healthz", check=False)
                if result.returncode == 0:
                    assert json.loads(result.stdout)["status"] == "ok"
                    break
                if time.monotonic() >= deadline or docker("inspect", "--format", "{{.State.Running}}", name).stdout.strip() != "true":
                    raise RuntimeError("Container did not become healthy")
                time.sleep(0.25)
            meta = json.loads(docker("exec", name, "wget", "-qO-", "http://127.0.0.1:1688/api/meta").stdout)
            assert meta["management_enabled"] and not meta["cpa_enabled"]
            status = docker("exec", name, "wget", "-qO-", "--header=Authorization: Bearer image-smoke-management-key",
                            "http://127.0.0.1:1688/api/status").stdout
            assert json.loads(status)["account_count"] == 1
            assert "image-smoke-api-key" not in status
            assert docker("exec", name, "id", "-u").stdout.strip() == "10001"
            docker("exec", name, "test", "-s", "/app/data/state.json")
            print(f"Image startup, health, management auth, non-root user and state write passed: {platform}")
        except Exception:
            logs = docker("logs", name, check=False)
            print(logs.stdout + logs.stderr)
            raise
        finally:
            docker("rm", "--force", name, check=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--platform", default="linux/amd64")
    arguments = parser.parse_args()
    verify(arguments.image, arguments.platform)
