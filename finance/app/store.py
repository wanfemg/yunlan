"""本程序自己的 SQLite：配置、推送去重、推送历史。"""

import json
import os
import sqlite3
import threading
import time

_SCHEMA = """
CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pushed_orders (
    kind      TEXT NOT NULL,
    trade_no  TEXT NOT NULL,
    status    TEXT NOT NULL,
    pushed_at INTEGER NOT NULL,
    PRIMARY KEY (kind, trade_no, status)
);
CREATE TABLE IF NOT EXISTS push_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    pushed_at     INTEGER NOT NULL,
    kind          TEXT NOT NULL,
    trade_no      TEXT NOT NULL,
    status        TEXT NOT NULL,
    username      TEXT NOT NULL,
    money         REAL NOT NULL,
    reason        TEXT NOT NULL,
    reason_source TEXT NOT NULL,
    ok            INTEGER NOT NULL,
    backfill      INTEGER NOT NULL DEFAULT 0,
    detail        TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_credential (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""

HISTORY_KEEP = 500
DELIVERY_STATE_PREFIX = "dingtalk_delivery:"
DELIVERY_STATE_KEEP = 32


class Store:
    def __init__(self, path):
        parent = os.path.dirname(os.path.abspath(path))
        os.makedirs(parent, exist_ok=True)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._lock = threading.RLock()
        with self._lock:
            self._conn.executescript("PRAGMA journal_mode=WAL;")
            self._conn.executescript(_SCHEMA)
            self._migrate()

    def _migrate(self):
        """老库补列：已存在的 push_history 表不会因为 _SCHEMA 变化而自动加列。"""
        columns = {row["name"] for row in self._conn.execute("PRAGMA table_info(push_history)")}
        if "backfill" not in columns:
            self._conn.execute(
                "ALTER TABLE push_history ADD COLUMN backfill INTEGER NOT NULL DEFAULT 0")
            self._conn.commit()

    def _write(self, sql, args=()):
        with self._lock:
            cursor = self._conn.execute(sql, args)
            self._conn.commit()
            return cursor

    def _read(self, sql, args=()):
        with self._lock:
            return self._conn.execute(sql, args).fetchall()

    # ---------- 配置 ----------

    def config_map(self):
        result = {}
        for row in self._read("SELECT key, value FROM config"):
            try:
                result[row["key"]] = json.loads(row["value"])
            except json.JSONDecodeError:
                result[row["key"]] = row["value"]
        return result

    def set_configs(self, values):
        with self._lock:
            for key, value in values.items():
                self._conn.execute(
                    "INSERT INTO config (key, value) VALUES (?, ?) "
                    "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                    (key, json.dumps(value, ensure_ascii=False)),
                )
            self._conn.commit()

    # ---------- 推送去重 ----------

    def is_pushed(self, kind, trade_no, status):
        rows = self._read(
            "SELECT 1 FROM pushed_orders WHERE kind = ? AND trade_no = ? AND status = ?",
            (kind, trade_no, status),
        )
        return bool(rows)

    def mark_pushed(self, kind, trade_no, status):
        self._write(
            "INSERT OR IGNORE INTO pushed_orders (kind, trade_no, status, pushed_at) "
            "VALUES (?, ?, ?, ?)",
            (kind, trade_no, status, int(time.time())),
        )

    def pushed_count(self):
        return self._read("SELECT COUNT(*) AS n FROM pushed_orders")[0]["n"]

    def add_history(self, record):
        self._write(
            "INSERT INTO push_history "
            "(pushed_at, kind, trade_no, status, username, money, reason, reason_source, ok, "
            "backfill, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                int(record.get("pushed_at") or time.time()),
                record["kind"], record["trade_no"], record["status"],
                record.get("username", ""), float(record.get("money") or 0),
                record.get("reason", ""), record.get("reason_source", ""),
                1 if record.get("ok") else 0, 1 if record.get("backfill") else 0,
                record.get("detail", ""),
            ),
        )
        self._write(
            "DELETE FROM push_history WHERE id NOT IN "
            "(SELECT id FROM push_history ORDER BY id DESC LIMIT ?)",
            (HISTORY_KEEP,),
        )

    def clear_history(self):
        self._write("DELETE FROM push_history")

    def history(self, limit=100):
        rows = self._read(
            "SELECT * FROM push_history ORDER BY id DESC LIMIT ?", (int(limit),))
        return [dict(row) for row in rows]

    # ---------- 管理口令 ----------

    def admin_hash(self):
        rows = self._read("SELECT password_hash FROM admin_credential WHERE id = 1")
        return rows[0]["password_hash"] if rows else ""

    def set_admin_hash(self, password_hash):
        self._write(
            "INSERT INTO admin_credential (id, password_hash) VALUES (1, ?) "
            "ON CONFLICT(id) DO UPDATE SET password_hash = excluded.password_hash",
            (password_hash,),
        )

    # ---------- 运行状态 ----------

    def get_state(self, key, default=""):
        rows = self._read("SELECT value FROM runtime_state WHERE key = ?", (key,))
        return rows[0]["value"] if rows else default

    def set_state(self, key, value):
        self._write(
            "INSERT INTO runtime_state (key, value) VALUES (?, ?) "
            "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            (key, str(value)),
        )

    def notification_delivery_state(self, fingerprint):
        """A receipt is valid only for its exact effective notification config."""
        empty = {"last_success": 0, "last_error_at": 0, "last_ok": False}
        if not fingerprint:
            return empty
        try:
            state = json.loads(self.get_state(DELIVERY_STATE_PREFIX + fingerprint, "{}"))
        except (TypeError, ValueError):
            return empty
        if not isinstance(state, dict):
            return empty
        return {
            "last_success": state.get("last_success", 0)
            if type(state.get("last_success")) is int and state["last_success"] > 0 else 0,
            "last_error_at": state.get("last_error_at", 0)
            if type(state.get("last_error_at")) is int and state["last_error_at"] > 0 else 0,
            "last_ok": state.get("last_ok") is True,
        }

    def record_notification_delivery(self, fingerprint, ok):
        """Atomically retain the latest actual result and timestamps per config."""
        if len(fingerprint) != 64 or any(ch not in "0123456789abcdef" for ch in fingerprint):
            raise ValueError("Invalid notification configuration fingerprint")
        key = DELIVERY_STATE_PREFIX + fingerprint
        with self._lock, self._conn:
            state = self.notification_delivery_state(fingerprint)
            now = int(time.time())
            state["last_ok"] = bool(ok)
            state["last_success" if ok else "last_error_at"] = now
            state["updated_ns"] = time.time_ns()
            self._conn.execute(
                "INSERT INTO runtime_state (key, value) VALUES (?, ?) "
                "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                (key, json.dumps(state, separators=(",", ":"))),
            )
            # Multiple in-flight requests may finish after settings change.
            # Keep each configuration separate so an old request cannot replace
            # the latest configuration's receipt. Bound superseded receipts.
            rows = self._conn.execute(
                "SELECT key, value FROM runtime_state WHERE key GLOB ?",
                (DELIVERY_STATE_PREFIX + "*",),
            ).fetchall()
            if len(rows) > DELIVERY_STATE_KEEP:
                updated = []
                for row in rows:
                    try:
                        value = json.loads(row["value"])
                        timestamp = int(value.get("updated_ns") or 0) if isinstance(value, dict) else 0
                    except (TypeError, ValueError):
                        timestamp = 0
                    updated.append((timestamp, row["key"]))
                updated.sort(reverse=True)
                self._conn.executemany(
                    "DELETE FROM runtime_state WHERE key = ?",
                    [(old_key,) for _, old_key in updated[DELIVERY_STATE_KEEP:] if old_key != key],
                )
