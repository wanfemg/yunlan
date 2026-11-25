"""Verify failure diagnostics stay useful without exposing deployment secrets."""
from __future__ import annotations

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("yunlan_diagnostics", SCRIPTS / "deploy-diagnostics.py")
diagnostics = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(diagnostics)


class RedactionTests(unittest.TestCase):
    def test_configured_secrets_and_password_components_are_hidden(self):
        settings = {
            "MAIN_DB_DSN": "postgres://user:url%40secret@database:5432/newapi",
            "LOG_DB_DSN": "user:mysql@secret@tcp(database:3306)/newapi",
            "MANAGEMENT_DB_DSN": "host=database password='pg\\'secret\\\\slash' dbname=newapi",
            "ADMIN_TOKEN": "admin-token-value",
            "FINANCE_PROXY_SECRET": "finance-secret-value",
        }
        raw_values = list(settings.values()) + [
            "url%40secret", "url@secret", "mysql@secret", "pg'secret\\slash",
        ]
        result = diagnostics.redact("\n".join(raw_values), diagnostics.secret_values(settings))
        for value in raw_values:
            self.assertNotIn(value, result)

    def test_unknown_url_mysql_and_keyword_passwords_use_format_fallback(self):
        logs = (
            "connect postgres://user:unknown-url@db:5432/main failed\n"
            "open user:unknown-mysql@tcp(db:3306)/main failed\n"
            "host=db password='unknown\\'keyword' dbname=main\n"
            'password="unknown-double" token=unknown-token\n'
        )
        result = diagnostics.redact(logs)
        for secret in ("unknown-url", "unknown-mysql", "unknown", "unknown-double", "unknown-token"):
            self.assertNotIn(secret, result)
        self.assertIn("db:3306", result)

    def test_bearer_and_basic_authorization_credentials_are_hidden(self):
        logs = (
            "Authorization: Bearer unlisted-bearer-credential\n"
            "authorization=Basic dW5saXN0ZWQ6cGFzc3dvcmQ=\n"
            "Proxy-Authorization: Bearer unlisted-proxy-credential\n"
        )
        result = diagnostics.redact(logs)
        for secret in (
            "unlisted-bearer-credential", "dW5saXN0ZWQ6cGFzc3dvcmQ=",
            "unlisted-proxy-credential",
        ):
            self.assertNotIn(secret, result)

    def test_known_decoded_password_is_hidden_in_json_escaped_messages(self):
        password = "pg'secret\\slash"
        settings = {"MAIN_DB_DSN": "host=database password='pg\\'secret\\\\slash' dbname=newapi"}
        result = diagnostics.redact(
            json.dumps({"message": "login failed: " + password}),
            diagnostics.secret_values(settings),
        )
        self.assertNotIn(json.dumps(password)[1:-1], result)
        self.assertNotIn("secret", result)

    def test_quoted_json_credential_keys_are_hidden(self):
        logs = (
            '{"token":"unlisted-json-token","password":"unlisted-json-password",'
            '"proxy_secret":"unlisted-json-secret",'
            '"Authorization":"Bearer unlisted-json-auth",'
            '"accessToken":"unlisted-camel-token","API_TOKEN":12345}\n'
        )
        result = diagnostics.redact(logs)
        for secret in (
            "unlisted-json-token", "unlisted-json-password", "unlisted-json-secret",
            "unlisted-json-auth", "unlisted-camel-token", "12345",
        ):
            self.assertNotIn(secret, result)

    def test_prefixed_credentials_not_present_in_old_config_are_hidden(self):
        logs = (
            "NEWAPI_ADMIN_TOKEN=unlisted-newapi-token\n"
            'PROBE_SHARED_SECRET="unlisted-probe-secret"\n'
            "MAIN_DB_DSN=host=unlisted-dsn-host user=unlisted-dsn-user "
            "password='unlisted-dsn-password' dbname=newapi\n"
            '{"MAIN_DB_DSN":"host=unlisted-json-dsn-host password=unlisted-json-dsn-password"}\n'
        )
        result = diagnostics.redact(logs, diagnostics.secret_values({"ADMIN_TOKEN": "old-admin-token"}))
        self.assertNotIn("unlisted", result)


class DeploymentDiagnosticsTests(unittest.TestCase):
    def invoke(self, run, settings=None):
        output = io.StringIO()
        with (
            patch.object(diagnostics, "read_settings", autospec=True, return_value=settings or {}),
            patch.object(diagnostics.subprocess, "run", autospec=True, side_effect=run),
            contextlib.redirect_stderr(output),
        ):
            result = diagnostics.main([
                "--root", str(SCRIPTS.parent), "--stage", "启动服务",
                "--exit-code", "17", "--", "--project-name", "yunlan",
            ])
        return result, output.getvalue()

    def test_empty_container_list_explains_missing_containers(self):
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            output = "NAME  IMAGE  COMMAND  SERVICE  CREATED  STATUS  PORTS\n" if "ps" in command else ""
            return subprocess.CompletedProcess(command, 0, output, "")

        result, output = self.invoke(run)
        self.assertEqual(result, 0)
        self.assertIn("当前编排没有容器", output)
        self.assertIn("退出码：17", output)
        self.assertEqual(calls[0], ["docker", "compose", "--project-name", "yunlan", "ps", "--all"])
        self.assertEqual(calls[1][-4:], ["logs", "--no-color", "--tail", "60"])

    def test_docker_unavailable_never_masks_original_failure(self):
        result, output = self.invoke(FileNotFoundError("missing docker"))
        self.assertEqual(result, 0)
        self.assertIn("Docker 命令不可用", output)
        self.assertIn("退出码：17", output)

    def test_failed_diagnostics_redacts_stderr(self):
        secret = "private-diagnostic-token"
        result, output = self.invoke(
            lambda command, **kwargs: subprocess.CompletedProcess(command, 1, "", "failed: " + secret),
            {"ADMIN_TOKEN": secret},
        )
        self.assertEqual(result, 0)
        self.assertNotIn(secret, output)
        self.assertIn("读取失败", output)

    def test_timeout_does_not_print_command_or_captured_secrets(self):
        result, output = self.invoke(subprocess.TimeoutExpired(
            ["docker", "private-argument"], 20, output="private-output",
        ))
        self.assertEqual(result, 0)
        self.assertIn("读取超时", output)
        self.assertNotIn("private", output)

    def test_output_is_redacted_before_line_and_byte_limits(self):
        secret = "secret-with-a-long-value"
        result, output = self.invoke(
            lambda command, **kwargs: subprocess.CompletedProcess(
                command, 0, ("x" * 300 + secret + "\n") * 200, "",
            ),
            {"PROBE_SHARED_SECRET": secret},
        )
        self.assertEqual(result, 0)
        self.assertNotIn(secret, output)
        self.assertLessEqual(len(output.encode("utf-8")), diagnostics.MAX_BYTES)
        self.assertLessEqual(len(output.splitlines()), diagnostics.MAX_LINES)
        self.assertIn("退出码：17", output)

    def test_invalid_arguments_still_return_success_for_diagnostic_helper(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(diagnostics.main(["--invalid"]), 0)

    def test_unreadable_config_suppresses_raw_output_and_log_collection(self):
        output = io.StringIO()
        with (
            patch.object(diagnostics, "read_settings", side_effect=OSError("unreadable")),
            patch.object(diagnostics.subprocess, "run", autospec=True, return_value=subprocess.CompletedProcess(
                ["docker"], 0, "container arbitrary-bare-credential", "other-bare-credential",
            )) as run,
            contextlib.redirect_stderr(output),
        ):
            self.assertEqual(diagnostics.main([
                "--root", str(SCRIPTS.parent), "--stage", "启动服务",
                "--exit-code", "17", "--", "--project-name", "yunlan",
            ]), 0)
        self.assertNotIn("bare-credential", output.getvalue())
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][-2:], ["ps", "--all"])


if __name__ == "__main__":
    unittest.main()
