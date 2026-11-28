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


def wait_healthy(name):
    deadline = time.monotonic() + 30
    while True:
        result = docker("exec", name, "wget", "-qO-", "http://127.0.0.1:1688/healthz", check=False)
        if result.returncode == 0:
            assert json.loads(result.stdout)["status"] == "ok"
            return
        if time.monotonic() >= deadline or docker("inspect", "--format", "{{.State.Running}}", name).stdout.strip() != "true":
            raise RuntimeError("Container did not become healthy")
        time.sleep(0.25)


def api(name, endpoint, *arguments):
    return json.loads(docker("exec", name, "wget", "-qO-", "--header=Authorization: Bearer image-smoke-management-key",
                             *arguments, "http://127.0.0.1:1688/api/" + endpoint).stdout)


def verify_process_user(name):
    # docker exec inherits the image's bootstrap user, not the application's UID.
    process = docker("exec", name, "cat", "/proc/1/status").stdout
    uid = next(line.split()[1:] for line in process.splitlines() if line.startswith("Uid:"))
    assert uid == ["10001"] * 4, uid


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
        data = Path(directory, "data")
        data.mkdir()
        legacy_time = "2026-11-27T21:30:00Z"
        Path(data, "state.json").write_text(json.dumps({
            "schema_version": 1, "last_run": legacy_time, "last_results": [
                {"provider": "image-smoke", "model": "legacy-model", "success": True}
            ], "logs": []
        }), encoding="utf-8")
        data_mount = f"type=bind,source={data.resolve()},target=/app/data"
        common = ["--platform", platform, "--mount", f"type=bind,source={config.resolve()},target=/app/config.yaml,readonly",
                  "--mount", data_mount, "--env", "QUOTA_WARMER_MANAGEMENT_KEY=image-smoke-management-key"]
        try:
            # Reproduce a bind directory created by root, with existing private state.
            docker("run", "--rm", "--platform", platform, "--user", "0", "--entrypoint", "sh", "--mount", data_mount, image,
                   "-ec", "mkdir -p /app/data/nested; printf keep > /app/data/nested/keep; ln -s /etc/passwd /app/data/escape; "
                   "chown -R 0:0 /app/data; find /app/data -type d -exec chmod 700 {} +; find /app/data -type f -exec chmod 600 {} +")
            docker("run", "--detach", "--name", name, *common, image)
            wait_healthy(name)
            meta = json.loads(docker("exec", name, "wget", "-qO-", "http://127.0.0.1:1688/api/meta").stdout)
            assert meta["management_enabled"] and not meta["cpa_enabled"]
            status = api(name, "status")
            assert status["account_count"] == 1 and not status.get("state_error"), status
            assert "image-smoke-api-key" not in json.dumps(status)
            assert status["last_results"][0]["finished_at"] == legacy_time
            verify_process_user(name)
            assert docker("exec", name, "cat", "/app/data/nested/keep").stdout == "keep"
            assert docker("exec", name, "stat", "-c", "%u", "/etc/passwd").stdout.strip() == "0", "Initialization followed a symlink"
            docker("exec", name, "test", "-s", "/app/data/state.json")
            result = api(name, "run", "--post-data={}")
            assert not result["status"].get("state_error"), result
            finished_at = result["results"][0]["finished_at"]
            docker("restart", name)
            wait_healthy(name)
            status = api(name, "status")
            assert not status.get("state_error") and status["last_results"][0]["finished_at"] == finished_at, status
            verify_process_user(name)
            docker("stop", name)
            # Explicit non-root users bypass bootstrap and still receive CLI arguments.
            docker("run", "--detach", "--name", name + "-nonroot", "--user", "10001:10001", *common,
                   image, "-config", "/app/config.yaml")
            wait_healthy(name + "-nonroot")
            verify_process_user(name + "-nonroot")
            assert not api(name + "-nonroot", "status").get("state_error")
            docker("stop", name + "-nonroot")
            denied = docker("run", "--rm", "--platform", platform, "--mount", data_mount + ",readonly", image, check=False)
            assert denied.returncode != 0 and "Cannot initialize /app/data permissions" in denied.stderr, denied
            print(f"Image bind permissions, non-root process, symlink safety, state retention/restart and read-only failure checks passed: {platform}")
        except Exception:
            logs = docker("logs", name, check=False)
            print(logs.stdout + logs.stderr)
            raise
        finally:
            docker("rm", "--force", name, check=False)
            docker("rm", "--force", name + "-nonroot", check=False)
            # The fixture is owned by UID 10001; let the host remove its temporary tree.
            docker("run", "--rm", "--platform", platform, "--user", "0", "--entrypoint", "sh", "--mount", data_mount, image,
                   "-ec", "find /app/data -type d -exec chmod a+rwx {} +; find /app/data -type f -exec chmod a+rw {} +", check=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--platform", default="linux/amd64")
    arguments = parser.parse_args()
    verify(arguments.image, arguments.platform)
