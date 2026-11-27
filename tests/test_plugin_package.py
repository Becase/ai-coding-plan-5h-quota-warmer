import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("build_plugin", ROOT / "scripts/build_plugin.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class StorePackageTests(unittest.TestCase):
    def test_zip_and_checksum_for_each_platform(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for system, extension in builder.EXTENSIONS.items():
                library = root / ("input" + extension)
                library.write_bytes(b"package-test-fixture")
                archive = builder.package_library(library, root, "0.1.0", system, "amd64")
                with zipfile.ZipFile(archive) as bundle:
                    self.assertEqual(bundle.namelist(), [builder.PLUGIN_ID + extension])
                    self.assertEqual(bundle.read(bundle.namelist()[0]), library.read_bytes())
                checksum = (root / f"checksums-{system}-amd64.txt").read_text()
                self.assertEqual(checksum, hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")

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
