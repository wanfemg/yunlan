"""Regression coverage for the installer configuration used before Compose."""
from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import docker_discovery as discovery

SPEC = importlib.util.spec_from_file_location("yunlan_installer", SCRIPTS / "configure-install.py")
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class InstallConfigurationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="yunlan-install-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "compose.yaml").write_text("services: {}\n", encoding="utf-8")
        (self.root / ".env.example").write_text(
            "APP_MODE=demo\nMAIN_DB_DRIVER=postgres\nMAIN_DB_DSN=\n"
            "NEWAPI_UPSTREAM=\nBIND_ADDRESS=127.0.0.1\nHOST_PORT=18080\n",
            encoding="utf-8",
        )

    def configure(self, environment=None, container=None, containers=None, arguments=()):
        with (
            patch.object(sys, "argv", [
                "configure-install.py", "--root", str(self.root), "--live", *arguments,
            ]),
            patch.dict(os.environ, environment or {}, clear=True),
            patch.object(installer, "saved_workspace_connections", autospec=True, return_value={}),
            patch.object(installer, "open_terminal", autospec=True, return_value=None),
            patch.object(installer, "select_container", autospec=True, return_value=container),
            patch.object(installer, "inspect_running_containers", autospec=True, return_value=containers or []),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            return installer.main()

    def test_complete_fresh_live_configuration_without_tty_uses_access_defaults(self):
        self.assertEqual(self.configure({
            "MAIN_DB_DSN": "postgres://test:dummy@db:5432/newapi",
            "NEWAPI_UPSTREAM": "http://new-api:3000",
        }), 0)
        _, saved = installer.read_env(self.root / ".env")
        self.assertEqual(saved["APP_MODE"], "live")
        self.assertEqual(saved["BIND_ADDRESS"], "0.0.0.0")
        self.assertEqual(saved["HOST_PORT"], "18080")
        self.assertTrue(saved["ADMIN_TOKEN"])
        self.assertTrue(saved["FINANCE_PROXY_SECRET"])

    def test_fresh_live_configuration_without_tty_keeps_explicit_access_settings(self):
        self.assertEqual(self.configure({
            "MAIN_DB_DSN": "postgres://test:dummy@db:5432/newapi",
            "NEWAPI_UPSTREAM": "http://new-api:3000",
            "BIND_ADDRESS": "127.0.0.1",
            "HOST_PORT": "28080",
        }), 0)
        _, saved = installer.read_env(self.root / ".env")
        self.assertEqual(saved["BIND_ADDRESS"], "127.0.0.1")
        self.assertEqual(saved["HOST_PORT"], "28080")

    def test_explicit_image_overrides_are_saved_for_compose(self):
        images = {
            "YUNLAN_IMAGE": "example.invalid/site-admin:v1",
            "YUNLAN_FINANCE_IMAGE": "example.invalid/finance:v1",
            "YUNLAN_PROBE_IMAGE": "example.invalid/probe:v1",
        }
        self.assertEqual(self.configure({
            "MAIN_DB_DSN": "postgres://test:dummy@db:5432/newapi",
            "NEWAPI_UPSTREAM": "http://new-api:3000",
            **images,
        }), 0)
        _, saved = installer.read_env(self.root / ".env")
        for key, image in images.items():
            self.assertEqual(saved[key], image)

    def test_missing_required_database_without_tty_still_stops(self):
        with self.assertRaisesRegex(installer.InstallError, "--non-interactive"):
            self.configure({"NEWAPI_UPSTREAM": "http://new-api:3000"})
        self.assertFalse((self.root / ".env").exists())

    def test_missing_required_upstream_without_tty_still_stops(self):
        with self.assertRaisesRegex(installer.InstallError, "--non-interactive"):
            self.configure({"MAIN_DB_DSN": "postgres://test:dummy@db:5432/newapi"})
        self.assertFalse((self.root / ".env").exists())

    def test_automatic_newapi_discovery_without_tty_saves_connections_and_network(self):
        newapi = {
            "Name": "/new-api",
            "Config": {"Env": [
                "SQL_DSN=postgres://test:dummy@postgres:5432/newapi", "PORT=3000",
            ]},
            "HostConfig": {"NetworkMode": "newapi_default"},
            "NetworkSettings": {"Networks": {
                "newapi_default": {"IPAddress": "172.20.0.2", "Aliases": ["new-api"]},
            }},
        }
        database = {
            "Name": "/postgres",
            "Config": {"ExposedPorts": {"5432/tcp": {}}},
            "NetworkSettings": {"Networks": {
                "newapi_default": {"IPAddress": "172.20.0.3", "Aliases": ["postgres"]},
            }},
        }
        self.assertEqual(self.configure(container=newapi, containers=[newapi, database]), 0)
        _, saved = installer.read_env(self.root / ".env")
        self.assertEqual(saved["MAIN_DB_DSN"], "postgres://test:dummy@postgres:5432/newapi")
        self.assertEqual(saved["NEWAPI_UPSTREAM"], "http://new-api:3000")
        overlay = json.loads((self.root / ".yunlan.compose.yaml").read_text(encoding="utf-8"))
        self.assertEqual(overlay["networks"]["yunlan_external_1"]["name"], "newapi_default")


class DatabaseEndpointRewriteTests(unittest.TestCase):
    def test_postgres_keyword_dsn_without_port_updates_host_and_appends_port(self):
        original = "host=127.0.0.1 user=test password='keep\\' quote\\\\slash' dbname=newapi sslmode=disable"
        endpoint = discovery.parse_database_endpoint(original, "postgres")
        actual = discovery.replace_database_endpoint(original, endpoint, "postgres", 15432)
        self.assertEqual(actual, original.replace("host=127.0.0.1", "host=postgres") + " port=15432")
        self.assertEqual(discovery.parse_database_endpoint(actual, "postgres").host, "postgres")

    def test_postgres_keyword_dsn_with_port_keeps_other_parameters(self):
        original = "host='127.0.0.1' port=5432 user=test password='dummy pass' dbname=newapi"
        endpoint = discovery.parse_database_endpoint(original, "postgres")
        self.assertEqual(
            discovery.replace_database_endpoint(original, endpoint, "postgres", 15432),
            "host=postgres port=15432 user=test password='dummy pass' dbname=newapi",
        )


if __name__ == "__main__":
    unittest.main()
