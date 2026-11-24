"""Local finance demo: disposable SQLite data and fully local financial actions.

Run from the site-admin directory with the same FINANCE_PROXY_SECRET as its Go
gateway. No .env file is loaded. SQL_DSN, SQLITE_PATH and DATA_DIR are never used
by this entry point; every source query and write stays inside TemporaryDirectory.
No notifier/report worker is started and no notification sender is called.
"""

import argparse
from contextlib import closing
from datetime import date, timedelta
from decimal import Decimal
import json
import math
from pathlib import Path
import random
import sqlite3
import sys
from tempfile import TemporaryDirectory
import threading
import time
import urllib.parse

sys.path.insert(0, str(Path(__file__).resolve().parent / "app"))

import config as config_module
import finance as finance_module
from poller import Poller, render_message, resolve_reason
from report import day_window, render_report, summarize, week_window
from source import SourceDB
from store import Store
from webui import Handler, build_server, flow_bucket, proxy_secret_from_env


DEFAULT_PORT = 8765
DEFAULT_DAYS = 365
DEFAULT_USERS = 60
DEFAULT_SEED = 20261121
PLANS = (
    (1, "轻量体验周卡", 29.0),
    (2, "标准月订阅", 99.0),
    (3, "专业月订阅", 199.0),
    (4, "团队月订阅", 499.0),
    (5, "年度畅用订阅", 1299.0),
)
SOURCE_KEYS = {"db_type", "db_dsn", "sqlite_path"}
LOCAL_NOTICE = "本地演示：仅保存到临时 SQLite，未发送外部通知"


def demo_username(index):
    user_id = 101 + index
    initial = ("demo_alice", "demo_bob", "demo_carol", "demo_dan", "demo_eve", "demo_frank")
    return initial[index] if index < len(initial) else f"demo_user_{user_id:03d}"


def demo_display_name(index):
    initial = ("Alice", "Bob", "Carol", "Dan", "Eve", "Frank")
    return initial[index] if index < len(initial) else f"演示用户 {101 + index:03d}"


def create_sample_orders(path, *, days=DEFAULT_DAYS, users=DEFAULT_USERS,
                         seed=DEFAULT_SEED, now=None):
    """Generate a repeatable year of varied orders, including subscription mirrors."""
    now = int(time.time() if now is None else now)
    rng = random.Random(seed)
    today = date.fromtimestamp(now)
    wallet_rows, subscription_rows = [], []
    methods = (("alipay", "epay"), ("wxpay", "epay"), ("stripe", "stripe"),
               ("creem", "creem"), ("wxpay", "waffo"))
    amounts = (1, 6.6, 10, 19.9, 29, 49, 69, 99, 128, 198, 299, 498, 999, 1999)
    order_id = 0

    def append_order(user_id, created, status, subscription=False, fixture=False):
        nonlocal order_id
        order_id += 1
        method, provider = methods[rng.randrange(len(methods))]
        complete = min(now, created + rng.randint(8, 118)) if status == "success" else 0
        prefix = "S" if subscription else "W"
        trade_no = f"DEMO-{prefix}-{order_id:07d}"
        if subscription:
            plan_id, _, money = PLANS[rng.randrange(len(PLANS))]
            payload = json.dumps({
                "error": "演示：支付渠道拒绝交易", "code": "DEMO_PAYMENT_DECLINED",
            }, ensure_ascii=False) if status == "failed" else ""
            subscription_rows.append((
                order_id, user_id, plan_id, money, trade_no, method, provider,
                status, created, complete, payload,
            ))
            # NewAPI can write a wallet accounting mirror for a subscription.
            # The real finance SourceDB path must keep excluding these duplicates.
            if order_id % 7 == 0 and not fixture:
                wallet_rows.append((
                    order_id, user_id, 0, money, trade_no, method, provider,
                    created, complete, status,
                ))
        else:
            money = amounts[rng.randrange(len(amounts))]
            if user_id < 107 and status == "success":
                money = (299, 498, 999, 1999, 2999)[rng.randrange(5)]
            quota = int(Decimal(str(money)) * 500000)
            wallet_rows.append((
                order_id, user_id, quota, money, trade_no, method, provider,
                created, complete, status,
            ))

    for day in range(days - 1, -1, -1):
        local_day = today - timedelta(days=day)
        start = int(time.mktime((*local_day.timetuple()[:3], 0, 0, 0, 0, 0, -1)))
        # Growth, weekly variation and occasional campaigns make every range
        # visibly different while refreshing the page keeps the dataset stable.
        campaign = 27 if day % 29 in (0, 1, 2) else 0
        growth = 16 * (1 - day / max(1, days))
        count = max(22, round(63 + 16 * math.sin(day * math.pi * 2 / 7)
                              + 9 * math.cos(day * math.pi * 2 / 31) + growth + campaign))
        available = max(1, min(86400, now - start))
        for number in range(count):
            created = start + rng.randrange(available)
            roll = rng.randrange(100)
            status = ("success" if roll < 78 else "failed" if roll < 87
                      else "expired" if roll < 94 else "pending")
            # Some customers are regulars, others recharge only occasionally.
            user = 101 + (rng.randrange(min(12, users)) if number % 3 == 0 else rng.randrange(users))
            append_order(user, created, status, subscription=number % 4 == 1)

    # Guaranteed recent rows cover all filter states even when opened near
    # midnight. Pending rows below the expiry threshold remain genuinely pending.
    for index, status in enumerate(("success", "failed", "expired", "pending")):
        for subscription in (False, True):
            append_order(101 + index % users, now - 15 - index * 12 - int(subscription),
                         status, subscription, fixture=True)

    with closing(sqlite3.connect(path)) as db, db:
        db.executescript("""
            CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, display_name TEXT);
            CREATE TABLE top_ups (
                id INTEGER PRIMARY KEY, user_id INTEGER, amount INTEGER, money REAL,
                trade_no TEXT, payment_method TEXT, payment_provider TEXT, create_time INTEGER,
                complete_time INTEGER, status TEXT
            );
            CREATE TABLE subscription_plans (id INTEGER PRIMARY KEY, title TEXT);
            CREATE TABLE subscription_orders (
                id INTEGER PRIMARY KEY, user_id INTEGER, plan_id INTEGER, money REAL,
                trade_no TEXT, payment_method TEXT, payment_provider TEXT, status TEXT,
                create_time INTEGER, complete_time INTEGER, provider_payload TEXT
            );
            CREATE INDEX top_ups_window ON top_ups(create_time, status);
            CREATE INDEX top_ups_trade ON top_ups(trade_no);
            CREATE INDEX subscription_window ON subscription_orders(create_time, status);
            CREATE INDEX subscription_trade ON subscription_orders(trade_no);
        """)
        db.executemany("INSERT INTO users VALUES (?, ?, ?)", (
            (101 + index, demo_username(index), demo_display_name(index))
            for index in range(users)
        ))
        db.executemany("INSERT INTO subscription_plans VALUES (?, ?)",
                       [(plan_id, title) for plan_id, title, _ in PLANS])
        db.executemany("INSERT INTO top_ups VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", wallet_rows)
        db.executemany("INSERT INTO subscription_orders VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                       subscription_rows)
    return {
        "days": days, "users": users, "orders": order_id,
        "wallet_rows": len(wallet_rows), "subscription_rows": len(subscription_rows),
        "seed": seed, "created_at": now,
    }


class DemoWorkspace:
    """Own the only two database paths this demo can ever open."""

    def __init__(self, directory, *, days=DEFAULT_DAYS, users=DEFAULT_USERS, seed=DEFAULT_SEED):
        self.root = Path(directory).resolve(strict=True)
        self.source_path = self.root / "demo-orders.sqlite3"
        self.store = Store(str(self.root / "demo-state.sqlite3"))
        self.lock = threading.RLock()
        self.info = create_sample_orders(self.source_path, days=days, users=users, seed=seed)
        self.store.set_configs(dict(
            config_module.DEFAULTS,
            db_type="sqlite", db_dsn="", sqlite_path=str(self.source_path),
            dingtalk_webhook="https://demo.invalid/robot/send",
            dingtalk_secret="demo-local-only-secret", dingtalk_keyword="站长后台演示",
            enabled=True, daily_report=True, weekly_report=True,
            large_order_threshold=300.0, min_push_amount=1.0, expire_minutes=15,
        ))
        self.poller = DemoPoller(self)
        self.reporter = DemoReporter(self)
        self._seed_history()

    def config(self):
        # Never call effective_config(): inherited SQL_DSN / SQLITE_PATH and
        # saved connection strings must not redirect a demo query elsewhere.
        values = dict(config_module.DEFAULTS)
        values.update(self.store.config_map())
        values.update(db_type="sqlite", db_dsn="", sqlite_path=str(self.source_path))
        return values

    def source(self):
        return SourceDB(self.config())

    def close(self):
        # Closing the connection before TemporaryDirectory exits also allows
        # Windows to remove the SQLite file, WAL and SHM without locked handles.
        with self.store._lock:
            self.store._conn.close()

    def record_order(self, order, names, plans, *, backfill=False, ok=True):
        config = self.config()
        uid = int(order["user_id"])
        reason, reason_source = resolve_reason(order, config)
        title, text = render_message(
            order, names.get(uid, f"用户#{uid}"), plans.get(order.get("plan_id"), ""),
            reason, reason_source, backfill=backfill,
            large_threshold=config["large_order_threshold"],
        )
        self.store.add_history({
            "kind": order["kind"], "trade_no": order["trade_no"], "status": order["status"],
            "username": names.get(uid, f"用户#{uid}"), "money": order["money"],
            "reason": reason, "reason_source": reason_source, "ok": ok, "backfill": backfill,
            "pushed_at": int(order.get("complete_time") or order["create_time"]) + 2,
            "detail": f"{LOCAL_NOTICE}\n{title}\n{text}" if ok else "演示：接收端超时，等待本地重试",
        })
        if ok:
            self.store.mark_pushed(order["kind"], order["trade_no"], order["status"])

    def _seed_history(self):
        source, now = self.source(), self.info["created_at"]
        orders = source.orders(now - 14 * 86400, limit=260, expired_before=now - 15 * 60)
        names = source.usernames([order["user_id"] for order in orders])
        plans = source.plan_titles([order["plan_id"] for order in orders])
        for index, order in enumerate(reversed(orders)):
            self.record_order(order, names, plans, backfill=index % 17 == 0, ok=index % 19 != 0)
        for offset in range(7, 0, -1):
            self.reporter.run_daily(offset_days=offset)
        self.reporter.run_weekly(offset_weeks=1)
        self.poller.status.update(
            runs=288, last_run=now - 8, last_orders=len(orders),
            pushed=self.store.pushed_count(),
        )
        self.store.set_state("initialized", "1")


class DemoPoller:
    """Simulate backfills against fixture data without a worker or HTTP sender."""

    def __init__(self, workspace):
        self.workspace = workspace
        self.status = {"runs": 0, "last_run": 0, "last_orders": 0, "pushed": 0, "last_error": ""}

    def backfill(self, hours):
        with self.workspace.lock:
            config = self.workspace.config()
            now = int(time.time())
            expiry = int(config["expire_minutes"]) * 60
            orders = self.workspace.source().orders(
                now - max(int(hours) * 3600, expiry), limit=500,
                expired_before=now - expiry if expiry else None,
            )
            names = self.workspace.source().usernames([order["user_id"] for order in orders])
            plans = self.workspace.source().plan_titles([order["plan_id"] for order in orders])
            wanted = [order for order in orders if Poller._wanted(order, config)]
            for order in wanted:
                self.workspace.record_order(order, names, plans, backfill=True)
            self.status.update(last_run=now, last_orders=len(orders), last_error="")
            self.status["pushed"] += len(wanted)
            return len(wanted)


class DemoReporter:
    """Render real report aggregates and save the result locally."""

    def __init__(self, workspace):
        self.workspace = workspace

    def run_daily(self, offset_days=1, force=False, overrides=None):
        return self._run("daily", offset_days, force)

    def run_weekly(self, offset_weeks=1, force=False, overrides=None):
        return self._run("weekly", offset_weeks, force)

    def _run(self, kind, offset, force):
        with self.workspace.lock:
            config = self.workspace.config()
            if not config[f"{kind}_report"] and not force:
                return True, "演示定时报表已关闭"
            window = week_window if kind == "weekly" else day_window
            start, end, label = window(time.time(), offset)
            source = self.workspace.source()
            rows = source.daily_recharge(start, end)
            names = source.usernames([row["user_id"] for row in rows])
            title, text = render_report(rows, names, label, weekly=kind == "weekly")
            money, orders, users = summarize(rows)
            self.workspace.store.add_history({
                "kind": kind, "trade_no": label, "status": "success", "money": money,
                "reason": f"{orders} 笔 / {users} 人", "reason_source": kind,
                "ok": True, "detail": f"{LOCAL_NOTICE}\n{title}\n{text}",
            })
            if not force:
                self.workspace.store.set_state(f"{kind}_report_last", label)
            period = "周报" if kind == "weekly" else "日报"
            return True, f"演示{period}已生成：{label}，{orders} 笔 / {users} 人；已存本地记录，未发送通知"


class DemoHandler(Handler):
    """Replace only demo mutations; production Handler remains untouched."""

    def _config(self):
        return self.server.demo.config()

    def _status(self):
        return dict(super()._status(), demo=True, notifications="local-only")

    def do_GET(self):
        path = self._route_path(urllib.parse.urlsplit(self.path).path)
        if path == "/api/demo/info":
            if not self._require_auth():
                return
            return self._json(dict(self.server.demo.info, temporary=True, actions="local-only"))
        return super().do_GET()

    def do_POST(self):
        self.close_connection = True
        path = self._route_path(urllib.parse.urlsplit(self.path).path)
        actions = {
            "/api/config": self._save_demo_config,
            "/api/test": self._test_demo_notification,
            "/api/check": self._check_source,
            "/api/backfill": self._backfill,
            "/api/daily-report": self._daily_report,
            "/api/weekly-report": self._weekly_report,
            "/api/history/clear": self._clear_demo_history,
        }
        action = actions.get(path)
        if action is None:
            return super().do_POST()
        if not self._require_auth():
            return
        if not self._csrf_ok():
            return self._json({"error": "缺少合法的请求头"}, 403)
        with self.server.demo.lock:
            return action()

    def _save_demo_config(self):
        body = self._read_json()
        if not isinstance(body, dict):
            return self._json({"error": "配置需要是 JSON 对象"}, 400)
        incoming = body.get("config", body)
        if not isinstance(incoming, dict):
            return self._json({"error": "config 需要是 JSON 对象"}, 400)
        if self.server.proxy_secret and config_module.MANAGED_KEYS.intersection(incoming):
            return self._json({"error": "数据源与运行参数由站长后台统一管理"}, 400)
        current, values = self._config(), {}
        for key, value in incoming.items():
            if key not in config_module.DEFAULTS:
                continue
            try:
                value = config_module.coerce(key, value)
            except ValueError as exc:
                return self._json({"error": str(exc)}, 400)
            if key in config_module.SECRET_KEYS and value in ("", config_module.MASK):
                continue
            if key in SOURCE_KEYS:
                if value != current[key]:
                    return self._json({"error": "演示数据源固定为临时 SQLite，连接设置不会指向其他数据库"}, 400)
                continue
            values[key] = value
        if not values:
            return self._json({"error": "没有可保存的演示配置项"}, 400)
        errors = config_module.validate(dict(current, **values))
        if errors:
            return self._json({"error": "；".join(errors)}, 400)
        self.server.store.set_configs(values)
        return self._json({"ok": True, "warnings": [], "demo": True, "detail": LOCAL_NOTICE})

    def _test_demo_notification(self):
        self._form_overrides()  # Consume the same JSON request as the real action.
        self.server.store.add_history({
            "kind": "test", "trade_no": f"DEMO-TEST-{time.time_ns()}",
            "status": "success", "username": "", "money": 0,
            "reason": "本地测试通知", "reason_source": "demo",
            "ok": True, "detail": LOCAL_NOTICE,
        })
        return self._json({"ok": True, "demo": True, "detail": LOCAL_NOTICE})

    def _clear_demo_history(self):
        self.server.store.clear_history()
        return self._json({"ok": True, "demo": True})

    def _orders(self):
        """Keep the existing contract and add page/page_size for demo clients."""
        days = self._qint("days", 7, 1, 365)
        query = self._query()
        page_size = (self._qint("page_size", 50, 1, 2000) if "page_size" in query
                     else self._qint("limit", 500, 1, 2000))
        page = self._qint("page", 1, 1, 100000)
        wanted = self._order_filters()
        try:
            rows = self._flow_rows(days, 100001, wanted)
            total = len(rows)
            pages = max(1, math.ceil(total / page_size))
            page = min(page, pages)
            selected = rows[(page - 1) * page_size:page * page_size]
            names = self._usernames(selected, self._config())
            items = [
                self._order_item(row, flow_bucket(row.get("status")), names)
                for row in selected
                if flow_bucket(row.get("status")) in wanted
            ]
        except Exception as exc:
            return self._json({"error": f"读取演示订单失败：{exc}"}, 500)
        return self._json({
            "days": days, "items": items, "has_more": page < pages,
            "page": page, "page_size": page_size, "pages": pages, "total": total,
        })


def build_demo_server(directory, *, port=DEFAULT_PORT, proxy_secret,
                      days=DEFAULT_DAYS, users=DEFAULT_USERS, seed=DEFAULT_SEED):
    """Construct the demo without starting a persistent process or any worker."""
    if not proxy_secret:
        raise ValueError("演示模式需要 FINANCE_PROXY_SECRET，并与站长后台设置为相同值")
    workspace = DemoWorkspace(directory, days=days, users=users, seed=seed)
    try:
        server = build_server(
            workspace.store, workspace.poller, workspace.reporter, "127.0.0.1", port,
            base_path="/finance", preview=True, preview_source=str(workspace.source_path),
            proxy_secret=proxy_secret,
        )
    except Exception:
        workspace.close()
        raise
    # The injection is limited to this script. Live app/main.py continues to use
    # the original Handler, config handling, notification sender and datastore.
    server.RequestHandlerClass = DemoHandler
    server.demo = workspace
    return server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    parser.add_argument("--days", type=int, default=DEFAULT_DAYS)
    parser.add_argument("--users", type=int, default=DEFAULT_USERS)
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("--port 需要在 1 ~ 65535 之间")
    if not 30 <= args.days <= 365:
        parser.error("--days 需要在 30 ~ 365 之间")
    if not 6 <= args.users <= 1000:
        parser.error("--users 需要在 6 ~ 1000 之间")
    proxy_secret = proxy_secret_from_env("127.0.0.1", "/finance")
    if not proxy_secret:
        parser.error("需要设置 FINANCE_PROXY_SECRET，与站长后台的机器密钥保持一致；演示不设独立口令")
    with TemporaryDirectory(prefix="siteadmin-finance-demo-") as directory:
        server = build_demo_server(
            directory, port=args.port, proxy_secret=proxy_secret,
            days=args.days, users=args.users, seed=args.seed,
        )
        try:
            with server:
                print(
                    f"圈钱中心演示 http://127.0.0.1:{server.server_port}/finance/，"
                    f"{server.demo.info['users']} 位用户 / {server.demo.info['orders']} 笔订单 / "
                    f"{args.days} 天；通过站长后台进入，全部写操作仅存临时数据",
                    flush=True,
                )
                try:
                    server.serve_forever()
                except KeyboardInterrupt:
                    pass
        finally:
            server.demo.close()


if __name__ == "__main__":
    main()
