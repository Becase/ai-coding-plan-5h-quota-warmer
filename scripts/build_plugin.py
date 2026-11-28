"""Build and load a plugin on its native platform before creating a store ZIP."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PLUGIN_ID = "coding-plan-5h-quota-warmer"
EXTENSIONS = {"linux": ".so", "darwin": ".dylib", "windows": ".dll"}
RELEASE_TARGETS = tuple((system, arch) for system in EXTENSIONS for arch in ("amd64", "arm64"))


def source_version():
    source = (ROOT / "src/warmer.go").read_text(encoding="utf-8")
    return re.search(r'serviceVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"', source)[1]


def native_target():
    system = platform.system().lower()
    machine = platform.machine().lower()
    arch = {"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
    if system not in EXTENSIONS or arch is None:
        raise ValueError(f"Unsupported native platform: {system}/{machine}")
    return system, arch


def package_library(library, output, version, system, arch):
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f"{PLUGIN_ID}_{version}_{system}_{arch}.zip"
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
        bundle.write(library, PLUGIN_ID + EXTENSIONS[system])
    with zipfile.ZipFile(archive) as bundle:
        if bundle.namelist() != [PLUGIN_ID + EXTENSIONS[system]]:
            raise ValueError("Invalid store ZIP layout")
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / f"checksums-{system}-{arch}.txt").write_bytes(f"{digest}  {archive.name}\n".encode("utf-8"))
    return archive


def write_registry(repository, output):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Expected GitHub owner/repository, without a URL")
    owner = repository.split("/")[0]
    registry = {"schema_version": 1, "plugins": [{
        "id": PLUGIN_ID, "name": "AI Coding Plan 配额预热器",
        "description": "按日程预热 CPA 托管账号，模型可输入或点选，同时支持手动 API Key。",
        "author": owner, "repository": f"https://github.com/{repository}",
        "tags": ["warmup", "codex", "antigravity"],
    }]}
    output.mkdir(parents=True, exist_ok=True)
    (output / "registry.json").write_text(json.dumps(registry, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def assemble_release(output, version):
    expected = {f"{PLUGIN_ID}_{version}_{system}_{arch}.zip" for system, arch in RELEASE_TARGETS}
    actual = {archive.name for archive in output.glob("*.zip")}
    if actual != expected:
        raise ValueError(f"Incomplete release: missing={sorted(expected - actual)}, unexpected={sorted(actual - expected)}")
    checksums = []
    for system, arch in RELEASE_TARGETS:
        archive = output / f"{PLUGIN_ID}_{version}_{system}_{arch}.zip"
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        line = f"{digest}  {archive.name}"
        recorded = (output / f"checksums-{system}-{arch}.txt").read_text(encoding="utf-8").splitlines()
        if recorded != [line]:
            raise ValueError("Checksum mismatch: " + archive.name)
        with zipfile.ZipFile(archive) as bundle:
            if bundle.namelist() != [PLUGIN_ID + EXTENSIONS[system]]:
                raise ValueError("Invalid store ZIP layout: " + archive.name)
        checksums.append(line)
    (output / "checksums.txt").write_bytes(("\n".join(checksums) + "\n").encode("utf-8"))
    print(f"Verified {len(checksums)} platform ZIPs; wrote checksums.txt")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-tag", default="")
    parser.add_argument("--target", default="", help="Require this native OS_arch")
    parser.add_argument("--output", type=Path, default=ROOT / "plugin-dist")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--registry", metavar="OWNER/REPO", help="Generate registry.json only")
    mode.add_argument("--assemble-release", action="store_true", help="Require and verify all six platform ZIPs before writing checksums.txt")
    args = parser.parse_args()
    output = args.output.resolve()
    version = source_version()
    if args.release_tag and args.release_tag != "v" + version:
        parser.error("Release tag must match serviceVersion: v" + version)
    if args.registry:
        write_registry(args.registry, output)
        return
    if args.assemble_release:
        assemble_release(output, version)
        return
    system, arch = native_target()
    if args.target and args.target != f"{system}_{arch}":
        parser.error(f"Expected native {args.target}, found {system}_{arch}")
    environment = dict(os.environ, CGO_ENABLED="1", GOOS=system, GOARCH=arch)
    library = output / "native" / (PLUGIN_ID + EXTENSIONS[system])
    library.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["go", "build", "-tags", "cpa_plugin", "-buildmode=c-shared", "-trimpath",
                    "-ldflags=-s -w", "-o", str(library), "."], cwd=ROOT / "src", env=environment, check=True)
    subprocess.run([sys.executable, str(ROOT / "scripts/native_smoke.py"), str(library)], check=True)
    archive = package_library(library, output, version, system, arch)
    print(archive)


if __name__ == "__main__":
    main()
