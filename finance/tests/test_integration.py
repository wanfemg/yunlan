"""Targeted integrated-source and notification checks; only loopback mocks run."""

from contextlib import contextmanager
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / "app"))
sys.path.insert(0, str(BASE))

import config
import demo
from integration import InheritedSource, SOURCE_UNAVAILABLE
import notify
import poller
import report
from source import SourceDB, SourceError, parse_dsn
from store import Store
import webui

SECRET = "siteadmin-integration-test-connector-v1"
MAIN_DSN = "postgresql://main:server-only-password@127.0.0.1/newapi"


@contextmanager
def running(server):
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()


def call(server, path, payload=None):
    headers = {"X-Siteadmin-Finance": SECRET}
    body = None
    if payload is not None:
        body = json.dumps(payload).encode()
        headers.update({"X-Requested-With": "newapi-order-notifier", "Content-Type": "application/json"})
    connection = HTTPConnection("127.0.0.1", server.server_port, timeout=5)
    try:
        connection.request("POST" if payload is not None else "GET", "/finance" + path, body, headers)
        response = connection.getresponse()
        return response.status, json.loads(response.read())
    finally:
        connection.close()


class MockSiteAdmin(BaseHTTPRequestHandler):
    def do_GET(self):
        status = self.server.source_status
        if self.path != "/internal/finance/source" or self.headers.get("X-Siteadmin-Finance") != SECRET:
            status = 401
        payload = ({"mode": "live", "source": "siteadmin-main", "db_type": "postgres", "db_dsn": MAIN_DSN}
                   if status == 200 else {"error": "source unavailable"})
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class MockDingTalk(BaseHTTPRequestHandler):
    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.messages.append((self.path, payload))
        failed = self.server.fail_next
        self.server.fail_next = False
        body = json.dumps({"errcode": 130101 if failed else 0, "errmsg": "mock retry" if failed else "ok"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class IntegratedFinanceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="finance-integration-test-")
        self.store = Store(str(Path(self.directory.name) / "state.sqlite3"))
        self.admin = ThreadingHTTPServer(("127.0.0.1", 0), MockSiteAdmin)
        self.admin.source_status = 200
        self.admin_context = running(self.admin)
        self.admin_context.__enter__()
        self.store.inherited_source = InheritedSource(
            SECRET, f"http://127.0.0.1:{self.admin.server_port}", refresh_seconds=0)

    def tearDown(self):
        self.admin_context.__exit__(None, None, None)
        self.store._conn.close()
        self.directory.cleanup()

    def test_inheritance_ignores_standalone_and_recovers_from_late_gateway(self):
        self.store.set_configs({"db_type": "sqlite", "sqlite_path": "/do-not-open.db",
                                "db_dsn": "old-secret", "poll_interval": "invalid-old-value"})
        with patch.dict("os.environ", {"SQL_DSN": "mysql://wrong:secret@db/live", "SQLITE_PATH": "/wrong.db"}):
            inherited = config.effective_config(self.store)
            self.assertEqual(inherited["db_dsn"], MAIN_DSN)
            self.assertEqual(inherited["sqlite_path"], "")
            self.assertEqual(inherited["poll_interval"], 10)
            self.admin.source_status = 503
            unavailable = config.effective_config(self.store)
            self.assertEqual(config.validate(unavailable), [SOURCE_UNAVAILABLE])
            self.assertEqual(unavailable["db_dsn"], "")
            self.admin.source_status = 200
            self.store.inherited_source._retry_at = 0
            self.assertEqual(config.effective_config(self.store)["db_dsn"], MAIN_DSN)

    def test_browser_config_hides_sources_and_real_dingtalk_implementation_works(self):
        target = ThreadingHTTPServer(("127.0.0.1", 0), MockDingTalk)
        target.messages, target.fail_next = [], False
        with running(target):
            self.store.set_configs({
                "dingtalk_webhook": f"http://127.0.0.1:{target.server_port}/robot/send",
                "dingtalk_secret": "mock-signing-secret", "dingtalk_keyword": "integration-keyword",
                "dingtalk_at_mobiles": "13800138000, 13900139000",
            })
            server = webui.build_server(self.store, poller.Poller(self.store), report.DailyReporter(self.store),
                                        "127.0.0.1", 0, base_path="/finance", proxy_secret=SECRET)
            with running(server):
                status, result = call(server, "/api/config")
                self.assertEqual(status, 200)
                self.assertFalse(config.MANAGED_KEYS.intersection(result["config"]))
                self.assertNotIn("server-only-password", json.dumps(result))
                self.assertNotIn("db_dsn", result["secrets"])
                self.assertEqual(call(server, "/api/config", {"config": {"db_dsn": "override"}})[0], 400)
                self.assertEqual(call(server, "/api/config", {"config": {"poll_interval": 100}})[0], 400)
                self.assertEqual(call(server, "/api/config", {"config": {"push_failed": False}})[0], 200)
                self.assertEqual(call(server, "/api/test", {})[0], 200)
            path, payload = target.messages[-1]
            self.assertIn("timestamp=", path)
            self.assertIn("&sign=", path)
            self.assertIn("integration-keyword", payload["markdown"]["text"])
            self.assertEqual(payload["at"]["atMobiles"], ["13800138000", "13900139000"])

    def test_poller_switch_retry_and_reports_use_inherited_source(self):
        target = ThreadingHTTPServer(("127.0.0.1", 0), MockDingTalk)
        target.messages, target.fail_next = [], False
        order = {
            "kind": "wallet", "id": 1, "user_id": 101, "trade_no": "CLOSED", "status": "success",
            "money": 99, "amount": 49500000, "create_time": int(time.time()), "complete_time": int(time.time()),
            "payment_method": "alipay", "payment_provider": "epay", "plan_id": 0, "provider_payload": "",
        }
        expected_dsn = MAIN_DSN

        class FakeSource:
            def __init__(self, values):
                assert values["db_dsn"] == expected_dsn
                assert not values["sqlite_path"]

            def orders(self, *args, **kwargs):
                return [dict(order)]

            def usernames(self, ids):
                return {101: "demo_alice"}

            def plan_titles(self, ids):
                return {}

            def daily_recharge(self, *args):
                return [{"user_id": 101, "money": 99, "orders": 1}]

        with running(target), patch.object(poller, "SourceDB", FakeSource), patch.object(report, "SourceDB", FakeSource):
            self.store.set_configs({
                "dingtalk_webhook": f"http://127.0.0.1:{target.server_port}/robot/send",
                "enabled": False, "daily_report": True, "weekly_report": True,
            })
            self.store.set_state("initialized", "1")
            worker = poller.Poller(self.store)
            self.assertEqual(worker.run_once(), 0)
            self.assertEqual(len(target.messages), 0)
            self.store.set_configs({"enabled": True})
            order["trade_no"] = "RETRY"
            target.fail_next = True
            self.assertEqual(worker.run_once(), 0)
            self.assertFalse(self.store.is_pushed("wallet", "RETRY", "success"))
            self.assertEqual(worker.run_once(), 1)
            self.assertTrue(self.store.is_pushed("wallet", "RETRY", "success"))
            self.assertEqual(worker.run_once(), 0)
            self.assertEqual(len(target.messages), 2)
            reporter = report.DailyReporter(self.store)
            self.assertTrue(reporter.run_daily()[0])
            self.assertTrue(reporter.run_weekly()[0])
            self.assertTrue(self.store.get_state("daily_report_last"))
            self.assertTrue(self.store.get_state("weekly_report_last"))
            self.assertEqual(len(target.messages), 4)

    def test_database_dsn_compatibility(self):
        mysql = parse_dsn("main:p@ss:word@tcp([::1]:3306)/new%2Dapi?parseTime=true&loc=Local")
        self.assertEqual((mysql["host"], mysql["port"], mysql["password"], mysql["dbname"]),
                         ("::1", 3306, "p@ss:word", "new-api"))
        captured = {}

        class Connection:
            def set_session(self, **kwargs):
                captured.update(kwargs)

        class Driver:
            @staticmethod
            def connect(dsn, **kwargs):
                captured["dsn"] = dsn
                return Connection()

        for dsn in ("postgresql://main:encoded%20pass@localhost/newapi", "host=localhost dbname=newapi password='two words'"):
            with patch.dict(sys.modules, {"psycopg2": Driver}):
                values = dict(config.DEFAULTS, db_type="postgres", db_dsn=dsn)
                SourceDB(values)._connect()
                self.assertEqual(captured["dsn"], dsn)
        class MySQLDriver:
            @staticmethod
            def connect(**kwargs):
                captured["mysql"] = kwargs
                return Connection()

        with patch.dict(sys.modules, {"pymysql": MySQLDriver}):
            SourceDB(dict(config.DEFAULTS, db_type="mysql",
                          db_dsn="main:p@ss@tcp([::1]:3306)/newapi?parseTime=true&loc=Local&readTimeout=2s"))._connect()
            self.assertEqual(captured["mysql"]["password"], "p@ss")
            self.assertEqual(captured["mysql"]["read_timeout"], 2)
            self.assertNotIn("parseTime", captured["mysql"])
            self.assertNotIn("loc", captured["mysql"])
        capabilities, authentications = [0], []

        class TLSBase:
            def __init__(self, **kwargs):
                self.server_capabilities = capabilities[0]
                self._request_authentication()

            def _request_authentication(self):
                authentications.append(True)

        tls_driver = SimpleNamespace(
            connect=lambda **kwargs: None,
            connections=SimpleNamespace(Connection=TLSBase),
            constants=SimpleNamespace(CLIENT=SimpleNamespace(SSL=2048)),
        )
        with patch.dict(sys.modules, {"pymysql": tls_driver}):
            source = SourceDB(dict(config.DEFAULTS, db_type="mysql",
                                   db_dsn="main:pass@tcp(localhost:3306)/newapi?tls=true"))
            with self.assertRaises(SourceError):
                source._connect()
            self.assertFalse(authentications)
            capabilities[0] = 2048
            source._connect()
            self.assertEqual(len(authentications), 1)

    def test_demo_mutations_remain_local_and_source_is_not_exposed(self):
        with tempfile.TemporaryDirectory(prefix="finance-local-demo-") as directory:
            server = demo.build_demo_server(directory, port=0, proxy_secret=SECRET, days=30, users=6)
            try:
                with running(server), patch.object(notify, "send_markdown", side_effect=AssertionError("external send")), \
                        patch.object(webui, "send_markdown", side_effect=AssertionError("external send")), \
                        patch.object(poller, "send_markdown", side_effect=AssertionError("external send")), \
                        patch.object(report, "send_markdown", side_effect=AssertionError("external send")), \
                        patch.object(config, "effective_config", side_effect=AssertionError("inherited production source")):
                    status, result = call(server, "/api/config")
                    self.assertEqual(status, 200)
                    self.assertFalse(config.MANAGED_KEYS.intersection(result["config"]))
                    self.assertNotIn(directory, json.dumps(result))
                    self.assertFalse(server.store.admin_hash())
                    for path, payload in (
                        ("/api/config", {"config": {"dingtalk_keyword": "local-demo"}}),
                        ("/api/test", {}), ("/api/daily-report", {}), ("/api/weekly-report", {}),
                        ("/api/backfill", {"hours": 24}), ("/api/history/clear", {}),
                    ):
                        self.assertEqual(call(server, path, payload)[0], 200)
            finally:
                server.demo.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
