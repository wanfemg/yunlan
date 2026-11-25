import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "verify-deployment.py"
SPEC = importlib.util.spec_from_file_location("verify_deployment", SCRIPT)
verification = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verification)


def container(service, *, running=True, status="running", health="healthy"):
    return {
        "Id": {"site-admin": "a", "finance": "b", "login-probe": "c"}[service] * 64,
        "Config": {"Labels": {
            "com.docker.compose.project": "yunlan",
            "com.docker.compose.service": service,
            "com.docker.compose.oneoff": "False",
        }},
        "State": {"Running": running, "Status": status, "Health": {"Status": health}},
    }


class FakeDocker:
    def __init__(self, containers=None, *, mode="demo", login_ready=True, checks=None):
        self.containers = containers if containers is not None else [
            container("site-admin"), container("finance"),
        ]
        self.mode = mode
        self.login_ready = login_ready
        self.checks = checks if checks is not None else [
            {"id": name, "state": "connected"} for name in ("main", "logs", "management", "newapi")
        ]
        self.calls = []
        self.html = "<!doctype html><html><body><div id='root'></div></body></html>"
        self.health = {"status": "ok"}

    def __call__(self, command, **kwargs):
        self.calls.append(command)
        if command[1] == "ps":
            body = "\n".join(item["Id"] for item in self.containers)
        elif command[1] == "inspect":
            body = "\n".join(json.dumps(item) for item in self.containers)
        elif command[1] == "exec":
            script = command[-1]
            if "/api/system/connections/check" in script:
                body = json.dumps({"success": True, "data": {"checks": self.checks}})
            elif "/api/auth/session" in script:
                body = json.dumps({"success": True, "data": {
                    "mode": self.mode, "login_configured": True, "login_ready": self.login_ready,
                }})
            elif "/api/health" in script:
                body = json.dumps(self.health)
            else:
                body = self.html
        else:
            raise AssertionError(command)
        return subprocess.CompletedProcess(command, 0, body, "")


class VerificationTests(unittest.TestCase):
    def run_verifier(self, docker, *, timeout=2, mode="demo", probe=False, compose_file=None):
        now = [0.0]
        messages = []

        def sleep(seconds):
            now[0] += seconds

        with patch.object(verification.subprocess, "run", side_effect=docker), \
                patch.object(verification.time, "monotonic", side_effect=lambda: now[0]), \
                patch.object(verification.time, "sleep", side_effect=sleep):
            verification.verify_deployment(
                "yunlan", timeout, mode=mode, probe=probe, report=messages.append,
                compose_file=compose_file,
            )
        return messages

    def test_no_containers_cannot_pass(self):
        with self.assertRaisesRegex(verification.VerificationError, "site-admin.*finance"):
            self.run_verifier(FakeDocker([]))

    def test_missing_finance_cannot_pass(self):
        with self.assertRaisesRegex(verification.VerificationError, "finance"):
            self.run_verifier(FakeDocker([container("site-admin")]))

    def test_crashed_service_fails_without_waiting_for_http(self):
        docker = FakeDocker([container("site-admin", running=False, status="exited")])
        with self.assertRaisesRegex(verification.VerificationError, "site-admin.*已退出"):
            self.run_verifier(docker)
        self.assertFalse(any(call[1] == "exec" for call in docker.calls))

    def test_unhealthy_service_cannot_pass(self):
        docker = FakeDocker([container("site-admin"), container("finance", health="unhealthy")])
        with self.assertRaisesRegex(verification.VerificationError, "finance.*健康检查"):
            self.run_verifier(docker)

    def test_demo_requires_health_html_and_ready_login(self):
        docker = FakeDocker()
        self.run_verifier(docker)
        scripts = [command[-1] for command in docker.calls if command[1] == "exec"]
        self.assertEqual(len(scripts), 3)
        self.assertTrue(any("/api/health" in script for script in scripts))
        self.assertTrue(any("/api/auth/session" in script for script in scripts))
        self.assertFalse(any("/connections/check" in script for script in scripts))

    def test_missing_frontend_cannot_pass_constant_health(self):
        docker = FakeDocker()
        docker.html = '{"error":"frontend missing"}'
        with self.assertRaisesRegex(verification.VerificationError, "前端"):
            self.run_verifier(docker)

    def test_unreachable_newapi_login_cannot_pass_constant_health(self):
        docker = FakeDocker(mode="live", login_ready=False)
        with self.assertRaisesRegex(verification.VerificationError, "登录"):
            self.run_verifier(docker, mode="live")

    def test_running_in_wrong_mode_cannot_pass(self):
        with self.assertRaisesRegex(verification.VerificationError, "模式"):
            self.run_verifier(FakeDocker(mode="demo"), mode="live")

    def test_live_database_failure_cannot_pass_constant_health(self):
        for failing in ("main", "logs", "management"):
            with self.subTest(failing=failing):
                checks = [
                    {"id": name, "state": "failed" if name == failing else "connected"}
                    for name in ("main", "logs", "management", "newapi")
                ]
                with self.assertRaisesRegex(verification.VerificationError, failing):
                    self.run_verifier(FakeDocker(mode="live", checks=checks), mode="live")

    def test_live_newapi_bridge_failure_cannot_pass(self):
        checks = [
            {"id": name, "state": "failed" if name == "newapi" else "connected"}
            for name in ("main", "logs", "management", "newapi")
        ]
        with self.assertRaisesRegex(verification.VerificationError, "NewAPI"):
            self.run_verifier(FakeDocker(mode="live", checks=checks), mode="live")

    def test_optional_newapi_management_credentials_are_reported(self):
        checks = [
            {"id": name, "state": "missing" if name == "newapi" else "connected"}
            for name in ("main", "logs", "management", "newapi")
        ]
        docker = FakeDocker(mode="live", checks=checks)
        messages = self.run_verifier(docker, mode="live")
        self.assertTrue(any("尚未配置" in message for message in messages))
        scripts = [command[-1] for command in docker.calls if command[1] == "exec"]
        connection_script = next(script for script in scripts if "/connections/check" in script)
        self.assertIn("${ADMIN_TOKEN", connection_script)

    def test_probe_profile_requires_probe_container(self):
        with self.assertRaisesRegex(verification.VerificationError, "login-probe"):
            self.run_verifier(FakeDocker(), probe=True)

    def test_container_labels_are_verified_after_inspect(self):
        foreign = container("finance")
        foreign["Config"]["Labels"]["com.docker.compose.project"] = "another-project"
        with self.assertRaisesRegex(verification.VerificationError, "finance"):
            self.run_verifier(FakeDocker([container("site-admin"), foreign]))

    def test_comma_separated_compose_file_labels_cannot_pass(self):
        path = "/opt/yunlan/.yunlan.runtime.yaml"
        docker = FakeDocker()
        for item in docker.containers:
            item["Config"]["Labels"]["com.docker.compose.project.config_files"] = path
        docker.containers[0]["Config"]["Labels"]["com.docker.compose.project.config_files"] = (
            "/opt/yunlan/compose.yaml,/opt/yunlan/.yunlan.compose.yaml"
        )
        with self.assertRaisesRegex(verification.VerificationError, "编排文件"):
            self.run_verifier(docker, compose_file=path)

    def test_single_compose_file_label_is_required_on_every_service(self):
        path = "/opt/yunlan/.yunlan.runtime.yaml"
        docker = FakeDocker()
        for item in docker.containers:
            item["Config"]["Labels"]["com.docker.compose.project.config_files"] = path
        self.run_verifier(docker, compose_file=path)

    def test_stale_initializer_label_cannot_pass_panel_validation(self):
        path = "/opt/yunlan/.yunlan.runtime.yaml"
        docker = FakeDocker()
        for item in docker.containers:
            item["Config"]["Labels"]["com.docker.compose.project.config_files"] = path
        initializer = container("site-admin", running=False, status="exited")
        initializer["Id"] = "d" * 64
        initializer["Config"]["Labels"]["com.docker.compose.service"] = "initialize-data"
        initializer["Config"]["Labels"]["com.docker.compose.project.config_files"] = (
            "/opt/yunlan/compose.yaml,/opt/yunlan/.yunlan.compose.yaml"
        )
        docker.containers.append(initializer)
        with self.assertRaisesRegex(verification.VerificationError, "initialize-data.*编排文件"):
            self.run_verifier(docker, compose_file=path)

    def test_initializer_without_health_is_safe_to_inspect(self):
        initializer = {
            "Id": "d" * 64,
            "Config": {"Labels": {
                "com.docker.compose.project": "yunlan",
                "com.docker.compose.service": "initialize-data",
                "com.docker.compose.oneoff": "False",
            }},
            "State": {"Running": False, "Status": "exited", "ExitCode": 0},
        }
        docker = FakeDocker([initializer, container("site-admin"), container("finance")])
        self.run_verifier(docker)
        command = next(command for command in docker.calls if command[1] == "inspect")
        template = command[command.index("--format") + 1]
        self.assertIn('"State":{{json .State}}', template)
        self.assertNotIn(".State.Health", template)
        self.assertNotIn(".Config.Env", template)

    def test_docker_failure_never_prints_raw_output(self):
        secret = "a-database-password-that-must-not-be-reported"

        def failed(command, **kwargs):
            return subprocess.CompletedProcess(command, 1, secret, secret)

        with self.assertRaises(verification.VerificationError) as caught:
            self.run_verifier(failed)
        self.assertNotIn(secret, str(caught.exception))

    def test_incomplete_connection_response_cannot_pass(self):
        with self.assertRaisesRegex(verification.VerificationError, "management"):
            self.run_verifier(FakeDocker(mode="live", checks=[
                {"id": "main", "state": "connected"},
                {"id": "logs", "state": "connected"},
            ]), mode="live")


if __name__ == "__main__":
    unittest.main()
