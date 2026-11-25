"""Exercise the deployment entry point at its shell/Docker boundary."""
import os
from pathlib import Path
import shutil
import shlex
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
BASH = shutil.which("bash")
if BASH is None and Path("C:/Program Files/Git/bin/bash.exe").is_file():
    BASH = "C:/Program Files/Git/bin/bash.exe"


@unittest.skipUnless(BASH, "Bash is required")
class DeployCLITests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="yunlan-deploy-cli-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / "scripts").mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        shutil.copyfile(ROOT / "deploy.sh", self.root / "deploy.sh")
        (self.root / "compose.yaml").write_text("services: {}\n", encoding="utf-8")
        (self.root / ".env").write_text("APP_MODE=demo\n", encoding="utf-8")
        for name in ("configure-install.py", "verify-deployment.py", "deploy-diagnostics.py",
                     "prepare-compose.py", "migrate-compose.py", "check-panel-compose.py"):
            (self.root / "scripts" / name).write_text("# fixture\n", encoding="utf-8")
        self.log = self.root / "calls"
        self.fixture(
            "docker",
            """#!/usr/bin/env bash
printf 'docker %s\\n' "$*" >> "$FAKE_CALLS"
if [[ "$*" == "compose version" || "$*" == "info" ]]; then exit 0; fi
if [[ "$*" == "compose up --help" ]]; then
  printf 'up -d --build\\n'
  exit 0
fi
case " $* " in
  *" pull "*) exit "${FAKE_PULL_FAIL:-0}" ;;
  *" up "*) exit "${FAKE_UP_FAIL:-0}" ;;
  *" port site-admin 18080 "*) printf '127.0.0.1:18080\\n' ;;
esac
""",
        )
        self.fixture(
            "python3",
            """#!/usr/bin/env bash
printf 'python3 %s\\n' "$*" >> "$FAKE_CALLS"
case "$1" in
  */configure-install.py)
    case " $* " in
      *" --read LOGIN_PROBE_ENABLED "*) printf '%s\\n' "${FAKE_PROBE_ENABLED:-false}" ;;
      *" --read APP_MODE "*) printf 'demo\\n' ;;
    esac ;;
  */verify-deployment.py) exit "${FAKE_VERIFY_FAIL:-0}" ;;
  */deploy-diagnostics.py) printf 'fixture diagnostics\\n' >&2 ;;
  */prepare-compose.py)
    if [[ "${FAKE_PREPARE_FAIL:-0}" != 0 ]]; then exit "$FAKE_PREPARE_FAIL"; fi
    printf '%s/.yunlan.runtime.yaml\\n' "$3" ;;
  */migrate-compose.py) exit "${FAKE_MIGRATE_FAIL:-0}" ;;
  */check-panel-compose.py) exit "${FAKE_PANEL_FAIL:-0}" ;;
esac
""",
        )

    def fixture(self, name, content):
        path = self.bin / name
        path.write_text(content, encoding="utf-8", newline="\n")
        path.chmod(0o755)

    def deploy(self, *arguments, **overrides):
        environment = dict(os.environ)
        environment.update(
            FAKE_CALLS=str(self.log).replace("\\", "/"),
            YUNLAN_MANAGE_MENU="1",
        )
        environment.update(overrides)
        # The wrapper avoids platform PATH delimiters while using real Bash.
        command = (
            'fixture_bin="$1"; '
            'if command -v cygpath >/dev/null; then fixture_bin="$(cygpath -u "$fixture_bin")"; fi; '
            'export PATH="$fixture_bin:$PATH"; shift; bash "$@"'
        )
        result = subprocess.run(
            [BASH, "-c", command, "deploy-test", self.bin.as_posix(),
             (self.root / "deploy.sh").as_posix(), "--non-interactive", *arguments],
            env=environment, text=True, encoding="utf-8", errors="replace",
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20,
        )
        calls = self.log.read_text(encoding="utf-8") if self.log.exists() else ""
        return result, calls

    def test_old_compose_without_wait_flags_starts_and_verifies(self):
        result, calls = self.deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn(" pull", calls)
        self.assertIn(" up -d --no-build", calls)
        self.assertIn("/verify-deployment.py", calls)
        self.assertNotIn("--wait", calls)
        self.assertNotIn(" build", calls)

    def test_explicit_source_build_does_not_pull_application_images(self):
        result, calls = self.deploy("--build")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn(" build", calls)
        self.assertNotIn(" pull\n", calls)

    def test_failed_image_pull_does_not_start_or_report_success(self):
        result, calls = self.deploy(FAKE_PULL_FAIL="17")
        self.assertEqual(result.returncode, 17, result.stdout)
        self.assertNotIn(" up ", calls)
        self.assertIn("/deploy-diagnostics.py", calls)
        self.assertNotIn("[部署成功]", result.stdout)

    def test_failed_container_start_reports_diagnostics(self):
        result, calls = self.deploy(FAKE_UP_FAIL="18")
        self.assertEqual(result.returncode, 18, result.stdout)
        self.assertIn("/deploy-diagnostics.py", calls)
        self.assertNotIn("[部署成功]", result.stdout)

    def test_failed_readiness_never_reports_deployment_success(self):
        result, calls = self.deploy(FAKE_VERIFY_FAIL="19")
        self.assertEqual(result.returncode, 19, result.stdout)
        self.assertIn("/verify-deployment.py", calls)
        self.assertIn("/deploy-diagnostics.py", calls)
        self.assertNotIn("[部署成功]", result.stdout)

    def test_network_overlay_never_reaches_container_file_labels(self):
        (self.root / ".yunlan.compose.yaml").write_text(
            '{"services":{"site-admin":{"networks":["default","newapi"]}},'
            '"networks":{"newapi":{"external":true}}}', encoding="utf-8",
        )
        result, calls = self.deploy(FAKE_PROBE_ENABLED="true")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("/prepare-compose.py", calls)
        self.assertIn("/migrate-compose.py", calls)
        for line in calls.splitlines():
            if not line.startswith("docker compose --project-name"):
                continue
            arguments = shlex.split(line)
            self.assertEqual(arguments.count("-f"), 1, line)
            compose_file = arguments[arguments.index("-f") + 1]
            self.assertTrue(compose_file.endswith("/.yunlan.runtime.yaml"), line)
            self.assertNotIn(",", compose_file)
        self.assertLess(calls.index("/migrate-compose.py"), calls.index(" up -d"))

    def test_failed_manifest_preparation_never_creates_containers(self):
        result, calls = self.deploy(FAKE_PREPARE_FAIL="20")
        self.assertEqual(result.returncode, 20, result.stdout)
        self.assertNotIn(" pull", calls)
        self.assertNotIn(" up ", calls)
        self.assertNotIn("[部署成功]", result.stdout)

    def test_failed_label_migration_never_starts_or_reports_success(self):
        result, calls = self.deploy(FAKE_MIGRATE_FAIL="21")
        self.assertEqual(result.returncode, 21, result.stdout)
        self.assertNotIn(" up ", calls)
        self.assertIn("/deploy-diagnostics.py", calls)
        self.assertNotIn("[部署成功]", result.stdout)

    def test_failed_panel_container_or_log_check_never_reports_success(self):
        result, calls = self.deploy(FAKE_PANEL_FAIL="22")
        self.assertEqual(result.returncode, 22, result.stdout)
        self.assertIn("/check-panel-compose.py", calls)
        self.assertIn("/deploy-diagnostics.py", calls)
        self.assertNotIn("[部署成功]", result.stdout)


if __name__ == "__main__":
    unittest.main()
