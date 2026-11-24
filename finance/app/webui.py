"""内嵌 Web 配置界面：站长后台认证或独立口令登录。

安全基线：口令用 PBKDF2-SHA256 加盐存散列；会话令牌放 HttpOnly + SameSite=Strict
Cookie 且仅在内存中保存；登录失败限速；写操作要求 JSON 与自定义请求头（CSRF）。
接入站长后台时，仅接受环回代理发送的每请求认证头。
"""

import base64
import csv
import hashlib
import hmac
import io
import ipaddress
import json
import logging
import os
import re
import secrets
import socket
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import config as config_module
import finance as finance_module
from delivery import delivery_readiness, record_delivery
from notify import send_markdown
from poller import pay_method_label
from source import SourceDB

WEB_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "web")
STATIC_TYPES = {
    "index.html": "text/html; charset=utf-8",
    "app.js": "application/javascript; charset=utf-8",
    "style.css": "text/css; charset=utf-8",
}

SESSION_COOKIE = "notifier_session"
PROXY_AUTH_HEADER = "X-Siteadmin-Finance"
SESSION_TTL = 12 * 3600
LOGIN_WINDOW = 300
LOGIN_MAX_ATTEMPTS = 8
PBKDF2_ROUNDS = 200_000
CSRF_HEADER = "X-Requested-With"
CSRF_VALUE = "newapi-order-notifier"
MAX_BODY = 64 * 1024

SECURITY_HEADERS = {
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "no-referrer",
    "Cache-Control": "no-store",
}
HTML_CSP = (
    "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; "
    "img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)


def validate_proxy_secret(secret, bind, base_path):
    if not secret:
        return ""
    if not re.fullmatch(r"[A-Za-z0-9_-]{32,128}", secret):
        raise ValueError("FINANCE_PROXY_SECRET 必须为 32 至 128 个 URL 安全字符")
    try:
        loopback = ipaddress.ip_address(bind).is_loopback and "%" not in bind
    except ValueError:
        loopback = False
    if not loopback or base_path != "/finance":
        raise ValueError("集成模式必须使用本机 IP 和 UI_BASE_PATH=/finance")
    return secret


def proxy_secret_from_env(bind, base_path):
    try:
        return validate_proxy_secret(os.environ.get("FINANCE_PROXY_SECRET", ""), bind, base_path)
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc


def hash_password(password, salt=None):
    salt = salt or secrets.token_bytes(16)
    digest = hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, PBKDF2_ROUNDS)
    return "pbkdf2_sha256${}${}${}".format(
        PBKDF2_ROUNDS,
        base64.b64encode(salt).decode("ascii"),
        base64.b64encode(digest).decode("ascii"),
    )


def verify_password(password, stored):
    try:
        algorithm, rounds, salt_b64, digest_b64 = stored.split("$")
        if algorithm != "pbkdf2_sha256":
            return False
        salt = base64.b64decode(salt_b64)
        expected = base64.b64decode(digest_b64)
    except (ValueError, TypeError):
        return False
    digest = hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, int(rounds))
    return hmac.compare_digest(digest, expected)


def ensure_admin(store):
    """首次启动初始化管理口令。返回 (口令, 是否自动生成) 或 None。"""
    if store.admin_hash():
        return None
    password = os.environ.get("UI_PASSWORD", "").strip()
    generated = not password
    if generated:
        password = secrets.token_urlsafe(12)
    if len(password) < 8:
        raise SystemExit("UI_PASSWORD 至少需要 8 位")
    store.set_admin_hash(hash_password(password))
    return password, generated


FLOW_STATUS_TEXT = {"success": "成功", "failed": "失败", "pending": "待支付"}
FLOW_BUCKETS = ("success", "failed", "pending")


def flow_bucket(status):
    """超时归入失败，未到阈值的待支付订单不视为失败。"""
    return "failed" if status == "expired" else (status or "pending")


def flow_status_text(status, derived):
    if status == "expired":
        return "失败·超时（推导）" if derived else "失败·超时"
    return FLOW_STATUS_TEXT.get(status, status)


def csv_cell(value):
    """阻止外部文本被电子表格解释为公式。"""
    text = str(value or "")
    if text.lstrip().startswith(("=", "+", "-", "@")) or text.startswith(("\t", "\r", "\n")):
        return "'" + text
    return text


class Sessions:
    def __init__(self):
        self._lock = threading.Lock()
        self._tokens = {}

    def create(self):
        token = secrets.token_urlsafe(32)
        with self._lock:
            self._expire()
            self._tokens[token] = time.time() + SESSION_TTL
        return token

    def valid(self, token):
        if not token:
            return False
        with self._lock:
            self._expire()
            return token in self._tokens

    def drop(self, token):
        with self._lock:
            self._tokens.pop(token, None)

    def _expire(self):
        now = time.time()
        for token in [t for t, expiry in self._tokens.items() if expiry < now]:
            del self._tokens[token]


class LoginLimiter:
    def __init__(self):
        self._lock = threading.Lock()
        self._hits = {}

    def allow(self, ip):
        now = time.time()
        with self._lock:
            recent = [t for t in self._hits.get(ip, []) if now - t < LOGIN_WINDOW]
            self._hits[ip] = recent
            return len(recent) < LOGIN_MAX_ATTEMPTS

    def record_failure(self, ip):
        with self._lock:
            self._hits.setdefault(ip, []).append(time.time())

    def clear(self, ip):
        with self._lock:
            self._hits.pop(ip, None)


class Handler(BaseHTTPRequestHandler):
    server_version = "newapi-order-notifier"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        logging.getLogger("web").info("%s %s", self.address_string(), fmt % args)

    # ---------- 路由 ----------

    def _route_path(self, path):
        base = self.server.base_path
        if not base or path.startswith(base + "/"):
            return path[len(base):]
        return None

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        if self.server.base_path and url.path == self.server.base_path:
            location = self.server.base_path + "/"
            if url.query:
                location += "?" + url.query
            return self._send_bytes(b"", "text/plain; charset=utf-8",
                                    {"Location": location}, status=308)
        path = self._route_path(url.path)
        if path is None:
            return self._json({"error": "接口不存在"}, 404)
        if path == "/healthz":
            return self._send_bytes(b"ok", "text/plain; charset=utf-8")
        if path in ("/", "/index.html"):
            return self._serve_static("index.html", html=True)
        if path.startswith("/static/"):
            name = path[len("/static/"):]
            if name != "index.html":
                return self._serve_static(name)
        if path == "/api/session":
            if self.server.proxy_secret and not self._require_auth():
                return
            session = {"authed": True, "integrated": True} if self.server.proxy_secret else {"authed": self._cookie_authed()}
            if self.server.preview:
                session["preview"] = True
            return self._json(session)
        if not self._require_auth():
            return
        if path == "/api/config":
            current = self._config()
            integrated = bool(self.server.proxy_secret)
            visible = config_module.public_config(current, integrated)
            return self._json({
                "config": visible,
                "secrets": {key: value for key, value in config_module.secret_flags(current).items()
                            if key in visible},
                "env_overridden": [] if self.server.preview or integrated else config_module.env_overrides(),
                "warnings": config_module.validate(current),
                "source": {"mode": "demo" if self.server.preview else "inherited" if integrated else "standalone",
                           "ready": not bool(current.get("_source_error"))},
            })
        if path == "/api/status":
            return self._json(self._status())
        if path == "/api/readiness":
            return self._json(self._readiness())
        if path == "/api/history":
            return self._json({"items": self.server.store.history(200)})
        if path == "/api/finance/summary":
            return self._finance_summary()
        if path == "/api/finance/trend":
            return self._finance_trend()
        if path == "/api/finance/ranking":
            return self._finance_ranking()
        if path == "/api/finance/users":
            return self._finance_users()
        if path == "/api/reports":
            config = self._config()
            return self._json({
                "daily_report": config["daily_report"],
                "weekly_report": config["weekly_report"],
                "daily_last": self.server.store.get_state("daily_report_last"),
                "weekly_last": self.server.store.get_state("weekly_report_last"),
            })
        if path == "/api/orders":
            return self._orders()
        if path == "/api/orders/export.csv":
            return self._orders_export()
        return self._json({"error": "接口不存在"}, 404)

    def do_POST(self):
        # Some handlers return before reading the body. Do not reuse those connections.
        self.close_connection = True
        path = self._route_path(urllib.parse.urlsplit(self.path).path)
        if path is None:
            return self._json({"error": "接口不存在"}, 404)
        if self.server.proxy_secret and path in ("/api/login", "/api/logout"):
            if not self._require_auth():
                return
            return self._json({"error": "请通过站长后台管理登录"}, 404)
        if path == "/api/login":
            return self._login()
        if not self._require_auth():
            return
        if not self._csrf_ok():
            return self._json({"error": "缺少合法的请求头"}, 403)
        if path == "/api/logout":
            self.server.sessions.drop(self._session_token())
            return self._json({"ok": True}, cookie="")
        if self.server.preview:
            return self._json({"error": "演示模式下不能修改配置或发送推送"}, 403)
        if path == "/api/config":
            return self._save_config()
        if path == "/api/test":
            return self._test_dingtalk()
        if path == "/api/backfill":
            return self._backfill()
        if path == "/api/daily-report":
            return self._daily_report()
        if path == "/api/weekly-report":
            return self._weekly_report()
        if path == "/api/check":
            return self._check_source()
        if path == "/api/history/clear":
            self.server.store.clear_history()
            return self._json({"ok": True})
        return self._json({"error": "接口不存在"}, 404)

    # ---------- 业务 ----------

    def _config(self):
        if self.server.preview:
            return dict(config_module.DEFAULTS, db_type="sqlite", sqlite_path=self.server.preview_source,
                        enabled=False, daily_report=False, weekly_report=False)
        return config_module.effective_config(self.server.store)

    def _login(self):
        ip = self.client_address[0]
        if not self.server.limiter.allow(ip):
            logging.warning("登录请求过多被拒 ip=%s", ip)
            return self._json({"error": "尝试过于频繁，请稍后再试"}, 429)
        body = self._read_json()
        password = str((body or {}).get("password") or "")
        if not verify_password(password, self.server.store.admin_hash()):
            self.server.limiter.record_failure(ip)
            logging.warning("管理口令校验失败 ip=%s", ip)
            return self._json({"error": "口令错误"}, 401)
        self.server.limiter.clear(ip)
        token = self.server.sessions.create()
        logging.info("管理界面登录成功 ip=%s", ip)
        return self._json({"ok": True}, cookie=token)

    def _save_config(self):
        body = self._read_json() or {}
        if not isinstance(body, dict):
            return self._json({"error": "配置需要是 JSON 对象"}, 400)
        incoming = body.get("config") if isinstance(body.get("config"), dict) else body
        if not isinstance(incoming, dict):
            return self._json({"error": "配置需要是 JSON 对象"}, 400)
        if self.server.proxy_secret and config_module.MANAGED_KEYS.intersection(incoming):
            return self._json({"error": "数据源与运行参数由站长后台统一管理"}, 400)
        values = {}
        for key, value in incoming.items():
            if key not in config_module.DEFAULTS:
                continue
            try:
                coerced = config_module.coerce(key, value)
            except ValueError as exc:
                return self._json({"error": str(exc)}, 400)
            # 敏感项提交的是占位文案或空值时说明用户没打算改，保持已保存的值
            if key in config_module.SECRET_KEYS and coerced in ("", config_module.MASK):
                continue
            values[key] = coerced
        if not values:
            return self._json({"error": "没有可保存的配置项"}, 400)
        self.server.store.set_configs(values)
        current = self._config()
        return self._json({"ok": True, "warnings": config_module.validate(current)})

    def _test_dingtalk(self):
        config = config_module.apply_overrides(self._config(), self._form_overrides())
        if not config["dingtalk_webhook"]:
            return self._json({"error": "请先填写并保存钉钉 Webhook"}, 400)
        text = (
            "### 🔔 测试消息\n"
            "- **来源**：new-api 订单通知服务\n"
            f"- **时间**：{time.strftime('%Y-%m-%d %H:%M:%S')}\n"
            "- **说明**：收到这条消息说明钉钉推送配置正常"
        )
        ok, detail = send_markdown(
            config["dingtalk_webhook"], "🔔 测试消息", text,
            secret=config["dingtalk_secret"], keyword=config["dingtalk_keyword"],
            at_mobiles=[m for m in (config["dingtalk_at_mobiles"] or "").split(",") if m.strip()],
        )
        record_delivery(self.server.store, config, ok)
        self.server.store.add_history({
            "kind": "test", "trade_no": "钉钉配置测试",
            "status": "success" if ok else "failed",
            "reason": "手动测试通知", "reason_source": "通知测试",
            "ok": ok, "detail": "钉钉测试投递成功" if ok else "钉钉测试投递失败",
        })
        return self._json({"ok": ok, "detail": detail}, 200 if ok else 400)

    def _check_source(self):
        try:
            detail = SourceDB(self._config()).ping()
        except Exception as exc:
            return self._json({"ok": False, "detail": self._source_message(exc)}, 400)
        return self._json({"ok": True, "detail": detail})

    def _status(self):
        status = dict(self.server.poller.status)
        status["pushed_total"] = self.server.store.pushed_count()
        status["version"] = config_module.VERSION
        status["warnings"] = config_module.validate(self._config())
        if self.server.proxy_secret and status.get("last_error") and not status["warnings"]:
            status["last_error"] = "通知处理遇到错误，请检查站长后台连接与钉钉推送记录"
        return status

    def _readiness(self):
        """Explicit, read-only readiness check; never sends a notification."""
        current = self._config()
        source_ready = False
        if not current.get("_source_error"):
            try:
                SourceDB(current).ping()
                source_ready = True
            except Exception:
                pass
        state = dict(self.server.poller.status)
        worker = getattr(self.server, "poller_thread", None)
        reporter = getattr(self.server, "reporter_thread", None)
        webhook = bool(str(current.get("dingtalk_webhook", "")).strip())
        preview = bool(self.server.preview)
        receipt = ({"delivery_verified": False, "last_success": 0, "last_error_at": 0}
                   if preview else delivery_readiness(self.server.store, current))
        return {
            "mode": "demo" if preview else "live",
            "integrated": bool(self.server.proxy_secret),
            "source_ready": source_ready,
            "ready": source_ready,
            "poller": {
                "running": bool(worker and worker.is_alive()),
                "last_run": int(state.get("last_run") or 0),
                "runs": int(state.get("runs") or 0),
                "has_error": bool(state.get("last_error")),
            },
            "reporter_running": bool(reporter and reporter.is_alive()),
            "notifications": {
                "configured": webhook,
                "signature_configured": bool(str(current.get("dingtalk_secret", "")).strip()),
                "automatic_enabled": bool(current.get("enabled")) and not preview,
                "daily_enabled": bool(current.get("daily_report")) and not preview,
                "weekly_enabled": bool(current.get("weekly_report")) and not preview,
                "external_delivery": not preview,
                **receipt,
            },
        }

    def _backfill(self):
        """补发最近 N 小时的订单：忽略去重记录，消息标注「补发推送」。"""
        body = self._read_json() or {}
        try:
            hours = int(body.get("hours", 24))
        except (TypeError, ValueError):
            return self._json({"error": "回溯小时数需要是整数"}, 400)
        if not 1 <= hours <= 720:
            return self._json({"error": "回溯小时数需要在 1 ~ 720 之间"}, 400)
        config = self._config()
        problems = config_module.validate(config)
        if problems:
            return self._json({"error": "配置不完整：" + "；".join(problems)}, 400)
        if not config["dingtalk_webhook"]:
            return self._json({"error": "请先填写并保存钉钉 Webhook"}, 400)
        try:
            count = self.server.poller.backfill(hours)
        except Exception as exc:
            return self._json({"error": self._source_message(exc)}, 400)
        return self._json({"ok": True, "count": count, "hours": hours})

    def _daily_report(self):
        """手动生成一份今天的充值统计（不受开关与去重限制），便于验证。"""
        try:
            ok, detail = self.server.reporter.run_daily(
                offset_days=0, force=True, overrides=self._form_overrides())
        except Exception as exc:
            return self._json({"ok": False, "detail": self._source_message(exc)}, 400)
        return self._json({"ok": ok, "detail": detail}, 200 if ok else 400)

    def _weekly_report(self):
        """手动推送本周截至当前的统计，不改变自动报告状态。"""
        try:
            ok, detail = self.server.reporter.run_weekly(
                offset_weeks=0, force=True, overrides=self._form_overrides())
        except Exception as exc:
            return self._json({"ok": False, "detail": self._source_message(exc)}, 400)
        return self._json({"ok": ok, "detail": detail}, 200 if ok else 400)

    # ---------- 财务看板 ----------

    def _query(self):
        return dict(urllib.parse.parse_qsl(urllib.parse.urlparse(self.path).query))

    def _qint(self, name, default, minimum, maximum):
        raw = self._query().get(name, "")
        try:
            value = int(raw)
        except (TypeError, ValueError):
            return default
        return max(minimum, min(maximum, value))

    def _finance_success(self, days):
        """窗口内的成功订单行，供概览/趋势/排行复用。"""
        start, end = finance_module.range_window(days)
        source = SourceDB(self._config())
        return source.success_orders(int(start), int(end))

    def _finance_summary(self):
        days = self._qint("days", 7, 1, 365)
        try:
            rows = self._finance_success(days)
        except Exception as exc:
            return self._json({"error": f"读取财务数据失败：{self._source_message(exc)}"}, 500)
        today_start, _ = finance_module.range_window(1)
        today_rows = [row for row in rows if int(row.get("create_time") or 0) >= today_start]
        return self._json({
            "days": days,
            "total": finance_module.totals(rows),
            "today": finance_module.totals(today_rows),
            "breakdown": finance_module.breakdown(rows),
            "methods": finance_module.methods(rows),
        })

    def _finance_trend(self):
        days = self._qint("days", 7, 1, 365)
        try:
            rows = self._finance_success(days)
            return self._json(finance_module.trend(rows, days))
        except Exception as exc:
            return self._json({"error": f"读取财务数据失败：{self._source_message(exc)}"}, 500)

    def _finance_ranking(self):
        days = self._qint("days", 7, 1, 365)
        limit = self._qint("limit", 10, 1, 50)
        try:
            rows = self._finance_success(days)
            names = SourceDB(self._config()).usernames([row["user_id"] for row in rows])
        except Exception as exc:
            return self._json({"error": f"读取财务数据失败：{self._source_message(exc)}"}, 500)
        return self._json({"days": days, "items": finance_module.ranking(rows, names, limit)})

    def _finance_users(self):
        days = self._qint("days", 7, 1, 365)
        try:
            rows = self._finance_success(days)
            names = SourceDB(self._config()).usernames([row["user_id"] for row in rows])
        except Exception as exc:
            return self._json({"error": f"读取财务数据失败：{self._source_message(exc)}"}, 500)
        return self._json({"days": days, "items": finance_module.users(rows, names)})

    def _flow_rows(self, days, limit, buckets):
        """窗口内的流水订单，超时未支付的 pending 按 expire_minutes 推导为超时。"""
        start, end = finance_module.range_window(days)
        config = self._config()
        expire_minutes = int(config["expire_minutes"] or 0)
        expire_before = int(time.time()) - expire_minutes * 60 if expire_minutes > 0 else None
        source = SourceDB(config)
        rows = source.window_orders(int(start), int(end), limit, buckets, expire_before)
        for row in rows:
            if (row.get("status") == "pending" and expire_before is not None
                    and int(row.get("create_time") or 0) <= expire_before):
                row["status"] = "expired"
                row["expired_derived"] = True
        return rows

    def _usernames(self, rows, config):
        try:
            return SourceDB(config).usernames([row["user_id"] for row in rows])
        except Exception as exc:
            logging.warning("读取用户名失败：%s", exc)
            return {}

    def _order_item(self, row, bucket, names):
        uid = row.get("user_id")
        return {
            "kind": row.get("kind"),
            "trade_no": row.get("trade_no") or "-",
            "user_id": uid,
            "username": names.get(int(uid)) if uid is not None else None,
            "money": round(float(row.get("money") or 0), 2),
            "method": pay_method_label(row.get("payment_method"), row.get("payment_provider")),
            "bucket": bucket,
            "status": row.get("status"),
            "derived": bool(row.get("expired_derived")),
            "create_time": row.get("create_time"),
            "complete_time": row.get("complete_time"),
            "plan_id": row.get("plan_id"),
        }

    def _order_filters(self):
        raw_status = self._query().get("status", "success,failed")
        return {token for token in raw_status.split(",") if token in FLOW_BUCKETS}

    def _orders(self):
        days = self._qint("days", 7, 1, 365)
        limit = self._qint("limit", 500, 1, 2000)
        wanted = self._order_filters()
        try:
            rows = self._flow_rows(days, limit + 1, wanted)
            has_more = len(rows) > limit
            rows = rows[:limit]
            config = self._config()
            names = self._usernames(rows, config)
            items = [
                self._order_item(row, flow_bucket(row.get("status")), names)
                for row in rows
                if flow_bucket(row.get("status")) in wanted
            ]
        except Exception as exc:
            return self._json({"error": f"读取订单失败：{self._source_message(exc)}"}, 500)
        return self._json({"days": days, "items": items, "has_more": has_more})

    def _orders_export(self):
        days = self._qint("days", 7, 1, 365)
        limit = self._qint("limit", 2000, 1, 10000)
        try:
            rows = self._flow_rows(days, limit + 1, self._order_filters())
            if len(rows) > limit:
                return self._json({"error": "订单超出导出上限，请缩小日期范围后重试"}, 400)
            config = self._config()
            names = self._usernames(rows, config)
        except Exception as exc:
            return self._json({"error": f"导出订单失败：{self._source_message(exc)}"}, 500)
        buffer = io.StringIO()
        writer = csv.writer(buffer)
        writer.writerow(["时间", "状态", "订单号", "用户", "金额", "支付方式", "类型"])
        for row in rows:
            bucket = flow_bucket(row.get("status"))
            uid = row.get("user_id")
            username = names.get(int(uid)) or f"用户#{uid}" if uid is not None else "-"
            writer.writerow([
                time.strftime("%Y-%m-%d %H:%M:%S",
                              time.localtime(int(row.get("create_time") or 0))),
                flow_status_text(row.get("status"), bool(row.get("expired_derived"))),
                csv_cell(row.get("trade_no") or "-"),
                csv_cell(username),
                f"{float(row.get('money') or 0):.2f}",
                csv_cell(pay_method_label(row.get("payment_method"), row.get("payment_provider"))),
                "钱包充值" if row.get("kind") == "wallet" else "订阅开通",
            ])
        body = ("\ufeff" + buffer.getvalue()).encode("utf-8")
        extra = {"Content-Disposition": "attachment; filename=orders.csv"}
        return self._send_bytes(body, "text/csv; charset=utf-8", extra)

    def _form_overrides(self):
        """读请求体里的钉钉推送项，供测试发送 / 生成统计即时使用（未保存也生效）。"""
        body = self._read_json() or {}
        incoming = body.get("config") if isinstance(body.get("config"), dict) else body
        return {key: incoming[key] for key in config_module.DINGTALK_KEYS if key in incoming}

    # ---------- 基础设施 ----------

    def _source_message(self, error):
        return "站长后台主数据源暂不可用，请稍后重试" if self.server.proxy_secret else str(error)

    def _session_token(self):
        for part in self.headers.get("Cookie", "").split(";"):
            name, _, value = part.strip().partition("=")
            if name == SESSION_COOKIE:
                return value
        return ""

    def _cookie_authed(self):
        return self.server.sessions.valid(self._session_token())

    def _require_auth(self):
        if self.server.proxy_secret:
            values = self.headers.get_all(PROXY_AUTH_HEADER, [])
            if len(values) == 1 and hmac.compare_digest(values[0].encode("latin-1"), self.server.proxy_secret):
                return True
            self._json({"error": "未经站长后台验证"}, 401)
            return False
        if self._cookie_authed():
            return True
        self._json({"error": "未登录"}, 401)
        return False

    def _csrf_ok(self):
        if self.headers.get(CSRF_HEADER, "") != CSRF_VALUE:
            return False
        return "application/json" in (self.headers.get("Content-Type", "").lower())

    def _read_json(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            return None
        if length <= 0 or length > MAX_BODY:
            return None
        try:
            return json.loads(self.rfile.read(length).decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError):
            return None

    def _json(self, payload, status=200, cookie=None):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers = {"Content-Type": "application/json; charset=utf-8"}
        if cookie is not None:
            age = SESSION_TTL if cookie else 0
            secure = "; Secure" if self.server.secure_cookie else ""
            headers["Set-Cookie"] = (
                f"{SESSION_COOKIE}={cookie}; Path={self.server.base_path}/; "
                f"HttpOnly; SameSite=Strict; Max-Age={age}{secure}")
        self._send_bytes(body, headers.pop("Content-Type"), headers, status=status)

    def _send_bytes(self, body, content_type, extra=None, status=200):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        if self.close_connection:
            self.send_header("Connection", "close")
        for key, value in SECURITY_HEADERS.items():
            self.send_header(key, value)
        for key, value in (extra or {}).items():
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _serve_static(self, name, html=False):
        content_type = STATIC_TYPES.get(name)
        path = os.path.join(WEB_DIR, name)
        if not content_type or not os.path.isfile(path):
            return self._send_bytes(b"not found", "text/plain; charset=utf-8", status=404)
        with open(path, "rb") as handle:
            body = handle.read()
        if html and self.server.proxy_secret:
            body = body.replace(b'data-finance-mode="standalone"', b'data-finance-mode="integrated"', 1)
        extra = {"Content-Security-Policy": HTML_CSP} if html else None
        self._send_bytes(body, content_type, extra)


class Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class IPv6Server(Server):
    address_family = socket.AF_INET6


def build_server(store, poller, reporter, bind, port, *, base_path="", secure_cookie=False,
                 preview=False, preview_source=None, proxy_secret=""):
    if preview and not preview_source:
        raise ValueError("演示模式需要指定本地 SQLite 数据源")
    proxy_secret = validate_proxy_secret(proxy_secret, bind, base_path)
    server_class = IPv6Server if ":" in bind else Server
    server = server_class((bind, port), Handler)
    server.store = store
    server.poller = poller
    server.reporter = reporter
    server.sessions = Sessions()
    server.limiter = LoginLimiter()
    server.base_path = base_path
    server.secure_cookie = secure_cookie
    server.preview = preview
    server.preview_source = preview_source
    server.proxy_secret = proxy_secret.encode("ascii")
    return server


def serve(store, poller, reporter, bind, port, *, proxy_secret=""):
    build_server(store, poller, reporter, bind, port,
                 base_path=config_module.UI_BASE_PATH,
                 secure_cookie=config_module.UI_SECURE_COOKIE,
                 proxy_secret=proxy_secret).serve_forever()
