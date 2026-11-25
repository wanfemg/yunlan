import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "migrate-compose.py"
SPEC = importlib.util.spec_from_file_location("migrate_compose", SCRIPT)
migration = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(migration)


class FakeDocker:
    def __init__(self, containers):
        self.containers = containers
        self.calls = []
        self.failed_operation = None
        self.identifiers = None

    def __call__(self, command, **kwargs):
        self.calls.append(command)
        operation = command[1]
        if operation == self.failed_operation:
            return subprocess.CompletedProcess(command, 1, "database-password", "database-password")
        if operation == "ps":
            body = self.identifiers if self.identifiers is not None else "\n".join(
                item["Id"] for item in self.containers
            )
        elif operation == "inspect":
            body = "\n".join(json.dumps(item) for item in self.containers)
        elif operation == "compose":
            body = ""
        else:
            raise AssertionError(command)
        return subprocess.CompletedProcess(command, 0, body, "")


class ComposeMigrationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="yunlan-compose-migration-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        (self.root / ".env").write_text("APP_MODE=demo\n", encoding="utf-8")
        (self.root / ".yunlan.runtime.yaml").write_text("name: yunlan\nservices: {}\n", encoding="utf-8")

    def container(self, service, *, current=False, project="yunlan", root=None):
        root = self.root if root is None else root
        filenames = (str(root / ".yunlan.runtime.yaml") if current else
                     str(root / "compose.yaml") + "," + str(root / ".yunlan.compose.yaml"))
        return {
            "Id": {"site-admin": "a", "finance": "b", "initialize-data": "c", "login-probe": "d"}[service] * 64,
            "Labels": {
                "com.docker.compose.project": project,
                "com.docker.compose.project.working_dir": str(root),
                "com.docker.compose.project.config_files": filenames,
                "com.docker.compose.service": service,
            },
        }

    def run_migration(self, docker, *, project="yunlan"):
        messages = []
        with patch.object(migration.subprocess, "run", side_effect=docker):
            changed = migration.migrate(self.root, project, report=messages.append)
        return changed, messages

    def test_no_existing_containers_needs_no_migration(self):
        docker = FakeDocker([])
        changed, _ = self.run_migration(docker)
        self.assertFalse(changed)
        self.assertEqual([command[1] for command in docker.calls], ["ps"])

    def test_all_current_labels_leave_containers_running(self):
        docker = FakeDocker([
            self.container(name, current=True)
            for name in ("site-admin", "finance", "initialize-data", "login-probe")
        ])
        changed, _ = self.run_migration(docker)
        self.assertFalse(changed)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_initializer_old_label_triggers_complete_project_migration(self):
        docker = FakeDocker([
            self.container("site-admin", current=True), self.container("finance", current=True),
            self.container("initialize-data"),
        ])
        changed, messages = self.run_migration(docker)
        self.assertTrue(changed)
        self.assertTrue(any("重启" in message for message in messages))
        commands = [command for command in docker.calls if command[1] == "compose"]
        self.assertEqual(len(commands), 1)
        command = commands[0]
        self.assertEqual(command.count("-f"), 1)
        self.assertEqual(command[command.index("-f") + 1], str(self.root / ".yunlan.runtime.yaml"))
        self.assertIn("--remove-orphans", command)
        self.assertEqual(command[command.index("--profile") + 1], "probe")
        self.assertIn("down", command)
        self.assertNotIn("-v", command)
        self.assertNotIn("--volumes", command)
        self.assertNotIn("--rmi", command)

    def test_inactive_old_probe_is_included_without_oneoff_filter(self):
        docker = FakeDocker([
            self.container("site-admin", current=True), self.container("finance", current=True),
            self.container("login-probe"),
        ])
        changed, _ = self.run_migration(docker)
        self.assertTrue(changed)
        listing = docker.calls[0]
        self.assertIn("-a", listing)
        self.assertFalse(any("oneoff" in argument for argument in listing))

    def test_single_base_file_label_is_legacy(self):
        item = self.container("site-admin")
        item["Labels"]["com.docker.compose.project.config_files"] = str(self.root / "compose.yaml")
        changed, _ = self.run_migration(FakeDocker([item]))
        self.assertTrue(changed)

    def test_foreign_project_is_never_removed(self):
        docker = FakeDocker([self.container("site-admin", project="newapi")])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_same_project_from_other_directory_is_never_removed(self):
        docker = FakeDocker([self.container("site-admin", root=self.root / "other-install")])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_config_file_outside_install_directory_is_never_removed(self):
        item = self.container("site-admin")
        item["Labels"]["com.docker.compose.project.config_files"] = str(self.root.parent / "compose.yaml")
        docker = FakeDocker([item])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_malformed_identifiers_prevent_inspect_and_removal(self):
        docker = FakeDocker([])
        docker.identifiers = "--unexpected-argument"
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertEqual(len(docker.calls), 1)

    def test_inspect_failure_cannot_remove_or_leak_output(self):
        docker = FakeDocker([self.container("site-admin")])
        docker.failed_operation = "inspect"
        with self.assertRaises(migration.MigrationError) as caught:
            self.run_migration(docker)
        self.assertNotIn("database-password", str(caught.exception))
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_migration_failure_cannot_leak_output(self):
        docker = FakeDocker([self.container("site-admin")])
        docker.failed_operation = "compose"
        with self.assertRaises(migration.MigrationError) as caught:
            self.run_migration(docker)
        self.assertNotIn("database-password", str(caught.exception))

    def test_runtime_file_must_exist_before_any_docker_operation(self):
        (self.root / ".yunlan.runtime.yaml").unlink()
        docker = FakeDocker([])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertEqual(docker.calls, [])

    def test_symlinked_runtime_prevents_any_docker_operation(self):
        docker = FakeDocker([])
        runtime = self.root / ".yunlan.runtime.yaml"
        with patch.object(Path, "is_symlink", autospec=True, side_effect=lambda path: path == runtime):
            with self.assertRaises(migration.MigrationError):
                self.run_migration(docker)
        self.assertEqual(docker.calls, [])

    def test_missing_directory_label_prevents_removal(self):
        item = self.container("site-admin")
        item["Labels"].pop("com.docker.compose.project.working_dir")
        docker = FakeDocker([item])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_missing_config_file_label_prevents_removal(self):
        item = self.container("site-admin")
        item["Labels"].pop("com.docker.compose.project.config_files")
        docker = FakeDocker([item])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker)
        self.assertFalse(any(command[1] == "compose" for command in docker.calls))

    def test_invalid_project_name_never_runs_docker(self):
        docker = FakeDocker([])
        with self.assertRaises(migration.MigrationError):
            self.run_migration(docker, project="../newapi")
        self.assertEqual(docker.calls, [])

    def test_inspect_only_requests_labels_and_identifier(self):
        docker = FakeDocker([self.container("site-admin", current=True)])
        self.run_migration(docker)
        command = next(command for command in docker.calls if command[1] == "inspect")
        template = command[command.index("--format") + 1]
        self.assertIn(".Config.Labels", template)
        self.assertNotIn(".Config.Env", template)
        self.assertNotIn("json .}}", template)

    def test_main_turns_failure_into_nonzero_exit(self):
        with patch.object(migration, "migrate", side_effect=migration.MigrationError("检查失败。")), \
                contextlib.redirect_stderr(io.StringIO()) as stderr:
            result = migration.main(["--root", str(self.root)])
        self.assertEqual(result, 1)
        self.assertIn("检查失败", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
