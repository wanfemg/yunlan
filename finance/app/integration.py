"""Server-only inheritance of the site-admin main database configuration."""

import ipaddress
import json
import os
import threading
import time
import urllib.parse
import urllib.request


SOURCE_UNAVAILABLE = "站长后台主数据源暂未就绪，财务服务将在后台重试"


class InheritedSourceUnavailable(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file, code, message, headers, new_url):
        return None


def source_origin_from_env(origin=None):
    raw = origin if origin is not None else os.environ.get("SITEADMIN_INTERNAL_URL", "").strip()
    if not raw:
        port = os.environ.get("PORT", "18080").strip() or "18080"
        raw = f"http://127.0.0.1:{port}"
    try:
        parsed = urllib.parse.urlsplit(raw)
        address = ipaddress.ip_address(parsed.hostname or "")
        if (parsed.scheme != "http" or not address.is_loopback or "%" in str(address)
                or parsed.username is not None or parsed.password is not None
                or parsed.path not in ("", "/") or parsed.query or parsed.fragment
                or parsed.port is None or not 1 <= parsed.port <= 65535):
            raise ValueError
    except ValueError:
        raise ValueError("SITEADMIN_INTERNAL_URL 必须是带端口的本机 HTTP 地址") from None
    return raw.rstrip("/")


class InheritedSource:
    def __init__(self, secret, origin=None, *, refresh_seconds=30):
        self.origin = source_origin_from_env(origin)
        self.secret = secret
        self.refresh_seconds = refresh_seconds
        self._cached = None
        self._expires_at = 0
        self._retry_at = 0
        self._lock = threading.RLock()
        # Ignore HTTP_PROXY and redirects so the machine key and source
        # configuration cannot leave the loopback endpoint.
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def snapshot(self):
        with self._lock:
            now = time.monotonic()
            if self._cached is not None and now < self._expires_at:
                return dict(self._cached)
            if now < self._retry_at:
                raise InheritedSourceUnavailable(SOURCE_UNAVAILABLE)
            request = urllib.request.Request(
                self.origin + "/internal/finance/source",
                headers={"X-Siteadmin-Finance": self.secret, "Accept": "application/json"},
            )
            try:
                with self._opener.open(request, timeout=3) as response:
                    body = response.read(65537)
                    if len(body) > 65536:
                        raise ValueError
                    data = json.loads(body)
                if (not isinstance(data, dict) or data.get("mode") != "live"
                        or data.get("source") != "siteadmin-main"
                        or data.get("db_type") not in ("postgres", "mysql")
                        or not isinstance(data.get("db_dsn"), str) or not data["db_dsn"].strip()):
                    raise ValueError
            except Exception:
                self._retry_at = now + 5
                # Never return a stored standalone DSN or log an HTTP response
                # that may contain database credentials.
                raise InheritedSourceUnavailable(SOURCE_UNAVAILABLE) from None
            self._cached = {"db_type": data["db_type"], "db_dsn": data["db_dsn"], "sqlite_path": ""}
            self._expires_at = now + self.refresh_seconds
            self._retry_at = 0
            return dict(self._cached)
