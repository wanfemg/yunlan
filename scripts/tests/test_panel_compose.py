import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "check-panel-compose.py"
SPEC = importlib.util.spec_from_file_location("check_panel_compose", SCRIPT)
panel = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(panel)


class FakeCompose:
    def __init__(self, root):
        self.calls = []
        self.projects = [{"Name": "yunlan", "Status": "running(3)", "ConfigFiles": str(root / ".yunlan.runtime.yaml")}]
        self.services = [{"ID": "a" * 64, "Service": "site-admin"}, {"ID": "b" * 64, "Service": "finance"}]
        self.logs = "site-admin | listening\nfinance | started\n"
        self.ps_format = "list"
        self.failed = None

    def __call__(self, command, **kwargs):
        self.calls.append(command)
        operation = next(name for name in ("ls", "ps", "logs") if name in command)
        if operation == self.failed:
            return subprocess.CompletedProcess(command, 1, "database-password", "database-password")
        if operation == "ls":
            body = json.dumps(self.projects)
        elif operation == "ps":
            body = (json.dumps(self.services) if self.ps_format == "list" else
                    "\n".join(json.dumps(service) for service in self.services))
        else:
            body = self.logs
        return subprocess.CompletedProcess(command, 0, body, "")


class PanelComposeTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="yunlan-panel-compose-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        (self.root / ".env").write_text("APP_MODE=demo\n", encoding="utf-8")
        (self.root / ".yunlan.runtime.yaml").write_text("name: yunlan\nservices: {}\n", encoding="utf-8")

    def check(self, docker):
        messages = []
        with patch.object(panel.subprocess, "run", side_effect=docker):
            panel.check_panel_compose(self.root, report=messages.append)
        return messages

    def test_success_checks_same_single_file_for_status_and_logs(self):
        docker = FakeCompose(self.root)
        messages = self.check(docker)
        self.assertTrue(any("通过" in message for message in messages))
        self.assertEqual(len(docker.calls), 3)
        listing = docker.calls[0]
        self.assertEqual(listing, ["docker", "compose", "ls", "--all", "--format", "json"])
        for command in docker.calls[1:]:
            self.assertEqual(command.count("-f"), 1)
            self.assertEqual(command[command.index("-f") + 1], str(self.root / ".yunlan.runtime.yaml"))
            self.assertEqual(command[command.index("--project-name") + 1], "yunlan")
        self.assertIn("--all", docker.calls[1])
        self.assertIn("--no-color", docker.calls[2])
        self.assertEqual(docker.calls[2][docker.calls[2].index("--tail") + 1], "5")

    def test_newline_ps_json_is_supported(self):
        docker = FakeCompose(self.root)
        docker.ps_format = "lines"
        self.check(docker)

    def test_comma_separated_project_file_fails_before_status(self):
        docker = FakeCompose(self.root)
        docker.projects[0]["ConfigFiles"] = str(self.root / "compose.yaml") + "," + str(self.root / ".yunlan.compose.yaml")
        with self.assertRaisesRegex(panel.PanelComposeError, "单一"):
            self.check(docker)
        self.assertEqual(len(docker.calls), 1)

    def test_runtime_file_must_exist(self):
        (self.root / ".yunlan.runtime.yaml").unlink()
        docker = FakeCompose(self.root)
        with self.assertRaises(panel.PanelComposeError):
            self.check(docker)
        self.assertEqual(docker.calls, [])

    def test_symlinked_runtime_is_rejected(self):
        docker = FakeCompose(self.root)
        runtime = self.root / ".yunlan.runtime.yaml"
        with patch.object(Path, "is_symlink", autospec=True, side_effect=lambda path: path == runtime):
            with self.assertRaises(panel.PanelComposeError):
                self.check(docker)
        self.assertEqual(docker.calls, [])

    def test_foreign_project_does_not_count_as_yunlan(self):
        docker = FakeCompose(self.root)
        docker.projects[0]["Name"] = "newapi"
        with self.assertRaisesRegex(panel.PanelComposeError, "yunlan"):
            self.check(docker)
        self.assertEqual(len(docker.calls), 1)

    def test_duplicate_yunlan_projects_fail(self):
        docker = FakeCompose(self.root)
        docker.projects.append(dict(docker.projects[0]))
        with self.assertRaises(panel.PanelComposeError):
            self.check(docker)

    def test_exited_initializer_cannot_report_panel_ready(self):
        docker = FakeCompose(self.root)
        docker.projects[0]["Status"] = "exited(1), running(3)"
        with self.assertRaisesRegex(panel.PanelComposeError, "状态"):
            self.check(docker)
        self.assertEqual(len(docker.calls), 1)

    def test_missing_or_invalid_project_status_cannot_report_panel_ready(self):
        for value in (None, "", 3, "exited(3)", "restarting(1), running(2)"):
            with self.subTest(value=value):
                docker = FakeCompose(self.root)
                if value is None:
                    docker.projects[0].pop("Status")
                else:
                    docker.projects[0]["Status"] = value
                with self.assertRaisesRegex(panel.PanelComposeError, "状态"):
                    self.check(docker)
                self.assertEqual(len(docker.calls), 1)

    def test_empty_container_list_fails(self):
        docker = FakeCompose(self.root)
        docker.services = []
        with self.assertRaisesRegex(panel.PanelComposeError, "容器"):
            self.check(docker)
        self.assertEqual(len(docker.calls), 2)

    def test_missing_finance_service_fails(self):
        docker = FakeCompose(self.root)
        docker.services = [docker.services[0]]
        with self.assertRaisesRegex(panel.PanelComposeError, "finance"):
            self.check(docker)

    def test_empty_logs_fail(self):
        docker = FakeCompose(self.root)
        docker.logs = " \n"
        with self.assertRaisesRegex(panel.PanelComposeError, "日志"):
            self.check(docker)

    def test_docker_failures_do_not_expose_raw_response(self):
        for operation in ("ls", "ps", "logs"):
            with self.subTest(operation=operation):
                docker = FakeCompose(self.root)
                docker.failed = operation
                with self.assertRaises(panel.PanelComposeError) as caught:
                    self.check(docker)
                self.assertNotIn("database-password", str(caught.exception))

    def test_invalid_json_never_echoes_response(self):
        secret = "database-password"

        def failed_json(command, **kwargs):
            return subprocess.CompletedProcess(command, 0, secret, "")

        with patch.object(panel.subprocess, "run", side_effect=failed_json), \
                self.assertRaises(panel.PanelComposeError) as caught:
            panel.check_panel_compose(self.root)
        self.assertNotIn(secret, str(caught.exception))

    def test_main_reports_failure_as_nonzero(self):
        with patch.object(panel, "check_panel_compose", side_effect=panel.PanelComposeError("面板检查失败。")), \
                contextlib.redirect_stderr(io.StringIO()) as stderr:
            result = panel.main(["--root", str(self.root)])
        self.assertEqual(result, 1)
        self.assertIn("面板检查失败", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
