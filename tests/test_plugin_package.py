import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("build_plugin", ROOT / "scripts/build_plugin.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class StorePackageTests(unittest.TestCase):
    def test_zip_and_checksum_for_each_platform(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for system, arch in builder.RELEASE_TARGETS:
                extension = builder.EXTENSIONS[system]
                library = root / ("input" + extension)
                library.write_bytes(b"package-test-fixture")
                archive = builder.package_library(library, root, "0.1.0", system, arch)
                with zipfile.ZipFile(archive) as bundle:
                    self.assertEqual(bundle.namelist(), [builder.PLUGIN_ID + extension])
                    self.assertEqual(bundle.read(bundle.namelist()[0]), library.read_bytes())
                checksum = (root / f"checksums-{system}-{arch}.txt").read_bytes()
                self.assertEqual(checksum, (hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n").encode("utf-8"))

    def make_release(self, root):
        required = {(system, arch) for system in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")}
        self.assertEqual(set(builder.RELEASE_TARGETS), required)
        for system, arch in sorted(required):
            library = root / ("fixture" + builder.EXTENSIONS[system])
            library.write_bytes(f"{system}-{arch}-package-fixture".encode("ascii"))
            builder.package_library(library, root, "1.2.3", system, arch)

    def test_windows_arm64_native_target(self):
        with mock.patch.object(builder.platform, "system", return_value="Windows"), mock.patch.object(builder.platform, "machine", return_value="ARM64"):
            self.assertEqual(builder.native_target(), ("windows", "arm64"))

    def test_complete_release_has_six_verified_lf_checksums(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_release(root)
            builder.assemble_release(root, "1.2.3")
            content = (root / "checksums.txt").read_bytes()
            self.assertNotIn(b"\r", content)
            self.assertEqual(len(content.splitlines()), 6)
            for line in content.decode("utf-8").splitlines():
                digest, name = line.split("  ")
                self.assertEqual(digest, hashlib.sha256((root / name).read_bytes()).hexdigest())

    def test_missing_windows_arm64_blocks_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_release(root)
            (root / f"{builder.PLUGIN_ID}_1.2.3_windows_arm64.zip").unlink()
            with self.assertRaisesRegex(ValueError, "Incomplete release.*windows_arm64"):
                builder.assemble_release(root, "1.2.3")
            self.assertFalse((root / "checksums.txt").exists())

    def test_modified_zip_blocks_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_release(root)
            archive = root / f"{builder.PLUGIN_ID}_1.2.3_windows_arm64.zip"
            archive.write_bytes(archive.read_bytes() + b"modified")
            with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
                builder.assemble_release(root, "1.2.3")
            self.assertFalse((root / "checksums.txt").exists())

    def test_wrong_zip_layout_blocks_release_even_with_matching_checksum(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_release(root)
            archive = root / f"{builder.PLUGIN_ID}_1.2.3_windows_arm64.zip"
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr("nested/" + builder.PLUGIN_ID + ".dll", b"fixture")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            (root / "checksums-windows-arm64.txt").write_bytes(f"{digest}  {archive.name}\n".encode("ascii"))
            with self.assertRaisesRegex(ValueError, "Invalid store ZIP layout"):
                builder.assemble_release(root, "1.2.3")
            self.assertFalse((root / "checksums.txt").exists())

    def test_unexpected_zip_blocks_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_release(root)
            (root / "unexpected.zip").write_bytes(b"fixture")
            with self.assertRaisesRegex(ValueError, "Incomplete release.*unexpected.zip"):
                builder.assemble_release(root, "1.2.3")

    def test_registry_uses_real_owner_and_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            builder.write_registry("Becase/ai-coding-plan-5h-quota-warmer", output)
            registry = json.loads((output / "registry.json").read_text(encoding="utf-8"))
            self.assertEqual(registry["schema_version"], 1)
            plugin = registry["plugins"][0]
            self.assertEqual(plugin["id"], builder.PLUGIN_ID)
            self.assertEqual(plugin["author"], "Becase")
            self.assertEqual(plugin["repository"], "https://github.com/Becase/ai-coding-plan-5h-quota-warmer")
            self.assertNotIn("license", plugin)
            with self.assertRaises(ValueError):
                builder.write_registry("https://github.com/owner/repository", output)

    def test_mismatched_tag_is_rejected_before_build(self):
        result = subprocess.run([sys.executable, str(ROOT / "scripts/build_plugin.py"), "--release-tag", "v999.0.0"], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"Release tag must match", result.stderr)


if __name__ == "__main__":
    unittest.main()
