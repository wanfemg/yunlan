"""Verify the panel-compatible single-file Compose preparation boundary."""
from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("yunlan_prepare_compose", SCRIPTS / "prepare-compose.py")
preparation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(preparation)


def rendered_compose():
    return {
        "name": "yunlan",
        "services": {
            "initialize-data": {"image": "alpine:3.22", "restart": "no"},
            "site-admin": {
                "image": "${YUNLAN_IMAGE:-ghcr.io/wanfemg/yunlan:latest}",
                "environment": {
                    "MAIN_DB_DSN": "${MAIN_DB_DSN:-}",
                    "FINANCE_PROXY_SECRET": "${FINANCE_PROXY_SECRET:?Run install.sh}",
                },
                "networks": {"default": None, "yunlan_external_1": None},
                "depends_on": {"initialize-data": {"condition": "service_completed_successfully"}},
            },
            "finance": {
                "network_mode": "service:site-admin",
                "depends_on": {"site-admin": {"condition": "service_healthy"}},
            },
            "login-probe": {"profiles": ["probe"], "network_mode": "service:site-admin"},
        },
        "networks": {
            "default": {"name": "yunlan_default"},
            "yunlan_external_1": {"external": True, "name": "newapi_default"},
        },
        "volumes": {"site-admin-data": {}, "finance-data": {}, "probe-data": {}},
    }


class PrepareComposeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="yunlan-compose-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.environment = self.root / ".env"
        self.source = self.root / "compose.yaml"
        self.overlay = self.root / ".yunlan.compose.yaml"
        self.target = self.root / ".yunlan.runtime.yaml"
        self.environment.write_text("ADMIN_TOKEN=fixture-private-value\n", encoding="utf-8")
        self.source.write_text("services: {}\n", encoding="utf-8")

    def invoke(self, document=None, result=None, exception=None, probe=False):
        if result is None:
            result = subprocess.CompletedProcess(
                ["docker"], 0, json.dumps(document or rendered_compose()), "",
            )
        output, errors = io.StringIO(), io.StringIO()
        with (
            patch.object(preparation.subprocess, "run", autospec=True,
                         return_value=result, side_effect=exception) as run,
            contextlib.redirect_stdout(output),
            contextlib.redirect_stderr(errors),
        ):
            arguments = ["--root", str(self.root)]
            if probe:
                arguments.append("--probe")
            code = preparation.main(arguments)
        return code, output.getvalue(), errors.getvalue(), run

    def test_single_manifest_preserves_placeholders_profile_and_external_networks(self):
        overlay = {
            "services": {"site-admin": {"networks": ["default", "yunlan_external_1"]}},
            "networks": {"yunlan_external_1": {"external": True, "name": "newapi_default"}},
        }
        self.overlay.write_text(json.dumps(overlay), encoding="utf-8")
        original_source = self.source.read_bytes()
        original_overlay = self.overlay.read_bytes()
        document = rendered_compose()
        rendered = json.dumps(document, ensure_ascii=False, indent=2)
        code, output, errors, run = self.invoke(
            result=subprocess.CompletedProcess(["docker"], 0, rendered, ""),
            probe=True,
        )
        self.assertEqual(code, 0, errors)
        self.assertEqual(output, str(self.target) + "\n")
        self.assertEqual(errors, "")
        expected = rendered_compose()
        expected["services"].pop("initialize-data")
        expected["services"]["site-admin"].pop("depends_on")
        expected["services"]["login-probe"].pop("profiles")
        expected["services"]["site-admin"]["ports"] = [
            {"target": 18080, "published": "18080", "host_ip": "127.0.0.1", "protocol": "tcp"},
            {"target": 18081, "published": "18081", "host_ip": "127.0.0.1", "protocol": "tcp"},
        ]
        self.assertEqual(
            self.target.read_text(encoding="utf-8"),
            json.dumps(expected, ensure_ascii=False, indent=2) + "\n",
        )
        self.assertEqual(json.loads(self.target.read_text(encoding="utf-8")), expected)
        self.assertEqual(self.source.read_bytes(), original_source)
        self.assertEqual(self.overlay.read_bytes(), original_overlay)
        self.assertNotIn("fixture-private-value", self.target.read_text(encoding="utf-8"))
        command = run.call_args.args[0]
        self.assertEqual(command, [
            "docker", "compose", "--project-name", "yunlan", "--project-directory",
            str(self.root), "--env-file", str(self.environment), "-f", str(self.source),
            "-f", str(self.overlay), "--profile", "probe", "config",
            "--no-interpolate", "--format", "json",
        ])
        if os.name != "nt":
            self.assertEqual(self.target.stat().st_mode & 0o777, 0o600)

    def test_disabled_probe_is_absent_from_runtime_service_set(self):
        code, _output, errors, _run = self.invoke(probe=False)
        self.assertEqual(code, 0, errors)
        document = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertEqual(set(document["services"]), {"site-admin", "finance"})

    def test_runtime_ports_use_configured_typed_settings_without_expanding_secrets(self):
        self.environment.write_text(
            "ADMIN_TOKEN=fixture-private-value\nBIND_ADDRESS=0.0.0.0\nHOST_PORT=28080\n"
            "PROBE_BIND_ADDRESS=::1\nPROBE_HOST_PORT=28081\n",
            encoding="utf-8",
        )
        code, output, errors, _run = self.invoke(probe=True)
        self.assertEqual(code, 0, errors)
        document = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertEqual(document["services"]["site-admin"]["ports"], [
            {"target": 18080, "published": "28080", "host_ip": "0.0.0.0", "protocol": "tcp"},
            {"target": 18081, "published": "28081", "host_ip": "::1", "protocol": "tcp"},
        ])
        self.assertIn("${MAIN_DB_DSN:-}", self.target.read_text(encoding="utf-8"))
        self.assertNotIn("fixture-private-value", output + errors + self.target.read_text(encoding="utf-8"))

    def test_runtime_ports_reject_invalid_addresses_and_port_boundaries(self):
        self.target.write_text("old manifest\n", encoding="utf-8")
        for key, value in (
            ("BIND_ADDRESS", "private-invalid-address"),
            ("HOST_PORT", "private-invalid-port"),
            ("HOST_PORT", "0"),
            ("HOST_PORT", "65536"),
            ("PROBE_BIND_ADDRESS", "private-invalid-probe-address"),
            ("PROBE_HOST_PORT", "-1"),
            ("PROBE_HOST_PORT", "65536"),
        ):
            with self.subTest(key=key, value=value):
                self.environment.write_text("ADMIN_TOKEN=fixture-private-value\n" + key + "=" + value + "\n",
                                            encoding="utf-8")
                code, output, errors, _run = self.invoke()
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                self.assertNotIn("private", errors)
                self.assertEqual(self.target.read_text(encoding="utf-8"), "old manifest\n")

    def test_enabled_probe_is_managed_without_a_profile_flag(self):
        code, _output, errors, _run = self.invoke(probe=True)
        self.assertEqual(code, 0, errors)
        document = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertEqual(set(document["services"]), {
            "site-admin", "finance", "login-probe",
        })
        self.assertNotIn("profiles", document["services"]["login-probe"])

    def test_runtime_removes_only_initialize_dependency_and_preserves_other_dependencies_and_volumes(self):
        document = rendered_compose()
        document["services"]["site-admin"]["depends_on"]["extra-service"] = {
            "condition": "service_started",
        }
        code, _output, errors, _run = self.invoke(document=document, probe=True)
        self.assertEqual(code, 0, errors)
        saved = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertNotIn("initialize-data", saved["services"])
        self.assertEqual(saved["services"]["site-admin"]["depends_on"], {
            "extra-service": {"condition": "service_started"},
        })
        self.assertEqual(saved["services"]["finance"]["depends_on"], {
            "site-admin": {"condition": "service_healthy"},
        })
        self.assertEqual(saved["volumes"], document["volumes"])

    def test_runtime_omits_empty_siteadmin_dependencies_after_initialize_removal(self):
        code, _output, errors, _run = self.invoke()
        self.assertEqual(code, 0, errors)
        saved = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertNotIn("depends_on", saved["services"]["site-admin"])

    def test_null_command_and_entrypoint_are_removed_without_changing_other_nulls(self):
        document = rendered_compose()
        document["services"]["finance"].update(command=None, entrypoint=None)
        document["services"]["site-admin"].update(command=[], entrypoint="")
        document["services"]["site-admin"]["environment"]["UNSET_VALUE"] = None
        code, _output, errors, _run = self.invoke(document=document, probe=True)
        self.assertEqual(code, 0, errors)
        saved = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertNotIn("command", saved["services"]["finance"])
        self.assertNotIn("entrypoint", saved["services"]["finance"])
        self.assertEqual(saved["services"]["site-admin"]["command"], [])
        self.assertEqual(saved["services"]["site-admin"]["entrypoint"], "")
        self.assertIsNone(saved["services"]["site-admin"]["environment"]["UNSET_VALUE"])

    def test_default_required_dependency_is_omitted_for_older_compose(self):
        document = rendered_compose()
        document["services"]["finance"]["depends_on"]["site-admin"]["required"] = True
        code, _output, errors, _run = self.invoke(document=document)
        self.assertEqual(code, 0, errors)
        saved = json.loads(self.target.read_text(encoding="utf-8"))
        self.assertEqual(saved["services"]["finance"]["depends_on"], {
            "site-admin": {"condition": "service_healthy"},
        })

    def test_unsupported_optional_or_invalid_dependency_keeps_existing_manifest(self):
        self.target.write_text("old manifest\n", encoding="utf-8")
        for required in (False, None, "private-invalid-required", 1):
            with self.subTest(required=required):
                document = rendered_compose()
                document["services"]["finance"]["depends_on"]["site-admin"]["required"] = required
                code, output, errors, _run = self.invoke(document=document)
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                self.assertNotIn("private", errors)
                self.assertEqual(self.target.read_text(encoding="utf-8"), "old manifest\n")

    def test_no_overlay_and_deterministic_rerun(self):
        first, *_ = self.invoke()
        self.assertEqual(first, 0)
        original = self.target.read_bytes()
        second, output, errors, run = self.invoke()
        self.assertEqual(second, 0, errors)
        self.assertEqual(self.target.read_bytes(), original)
        self.assertEqual(output, str(self.target) + "\n")
        self.assertNotIn(str(self.overlay), run.call_args.args[0])

    def test_renderer_failure_keeps_previous_manifest_and_hides_output(self):
        self.target.write_text("old manifest\n", encoding="utf-8")
        code, output, errors, _ = self.invoke(result=subprocess.CompletedProcess(
            ["docker"], 7, "private-stdout-value", "private-stderr-value",
        ))
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertNotIn("private", errors)
        self.assertEqual(self.target.read_text(encoding="utf-8"), "old manifest\n")

    def test_missing_service_and_invalid_json_cannot_replace_existing_manifest(self):
        self.target.write_text("old manifest\n", encoding="utf-8")
        incomplete = rendered_compose()
        del incomplete["services"]["login-probe"]
        for rendered in (json.dumps(incomplete), "private invalid json", "[]"):
            with self.subTest(rendered=rendered):
                code, output, errors, _ = self.invoke(result=subprocess.CompletedProcess(
                    ["docker"], 0, rendered, "",
                ))
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                self.assertNotIn("private", errors)
                self.assertEqual(self.target.read_text(encoding="utf-8"), "old manifest\n")

    def test_overlay_external_network_must_survive_rendering(self):
        self.overlay.write_text(json.dumps({
            "services": {"site-admin": {"networks": ["default", "yunlan_external_1"]}},
            "networks": {"yunlan_external_1": {"external": True, "name": "newapi_default"}},
        }), encoding="utf-8")
        document = rendered_compose()
        document["networks"]["yunlan_external_1"]["name"] = "wrong_network"
        code, output, _errors, _run = self.invoke(document=document)
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertFalse(self.target.exists())

    def test_all_managed_paths_refuse_symlinks_without_invoking_docker(self):
        outside = self.root / "outside"
        outside.write_text("unchanged\n", encoding="utf-8")
        try:
            probe = self.root / "symlink-probe"
            probe.symlink_to(outside)
            probe.unlink()
        except OSError:
            self.skipTest("Creating symlinks is unavailable")
        for path in (self.environment, self.source, self.overlay, self.target):
            with self.subTest(path=path.name):
                original = path.read_bytes() if path.exists() else None
                if path.exists():
                    path.unlink()
                path.symlink_to(outside)
                try:
                    code, output, _errors, run = self.invoke()
                    self.assertEqual(code, 1)
                    self.assertEqual(output, "")
                    run.assert_not_called()
                    self.assertEqual(outside.read_text(encoding="utf-8"), "unchanged\n")
                finally:
                    path.unlink()
                    if original is not None:
                        path.write_bytes(original)

    def test_managed_paths_refuse_directories(self):
        self.target.mkdir()
        code, output, _errors, run = self.invoke()
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        run.assert_not_called()

    def test_symlink_guard_is_checked_before_stat_on_every_managed_path(self):
        for path in (self.environment, self.source, self.overlay, self.target):
            with self.subTest(path=path.name):
                with patch.object(Path, "is_symlink", autospec=True,
                                  side_effect=lambda candidate: candidate == path):
                    code, output, _errors, run = self.invoke()
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                run.assert_not_called()

    def test_non_list_profiles_are_rejected_without_traceback(self):
        document = rendered_compose()
        document["services"]["login-probe"]["profiles"] = None
        code, output, errors, _run = self.invoke(document=document)
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertNotIn("Traceback", errors)
        self.assertFalse(self.target.exists())

    def test_oversized_input_is_rejected_before_invoking_docker(self):
        self.source.write_text("x" * (preparation.MAX_MANIFEST_BYTES + 1), encoding="utf-8")
        code, output, _errors, run = self.invoke()
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        run.assert_not_called()

    def test_failed_atomic_save_keeps_previous_manifest_and_reports_generic_error(self):
        self.target.write_text("old manifest\n", encoding="utf-8")
        with patch.object(preparation, "atomic_save", side_effect=RuntimeError("private-value")):
            code, output, errors, _run = self.invoke()
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertNotIn("private", errors)
        self.assertEqual(self.target.read_text(encoding="utf-8"), "old manifest\n")

    def test_oversized_rendered_manifest_is_not_saved(self):
        code, output, _errors, _run = self.invoke(result=subprocess.CompletedProcess(
            ["docker"], 0, "x" * (preparation.MAX_MANIFEST_BYTES + 1), "",
        ))
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertFalse(self.target.exists())

    def test_docker_unavailable_and_timeout_do_not_print_command(self):
        for error in (
            FileNotFoundError("private-docker-path"),
            subprocess.TimeoutExpired(["private-command"], 30, output="private-output"),
        ):
            with self.subTest(error=type(error).__name__):
                code, output, errors, _run = self.invoke(exception=error)
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                self.assertNotIn("private", errors)
                self.assertFalse(self.target.exists())


if __name__ == "__main__":
    unittest.main()
