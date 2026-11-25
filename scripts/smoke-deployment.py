#!/usr/bin/env python3
"""Exercise a deployed demo through its published HTTP ports."""
import argparse
from http.cookiejar import CookieJar
import json
import re
from urllib.error import HTTPError
from urllib.parse import urljoin, urlsplit
from urllib.request import HTTPCookieProcessor, Request, build_opener


def smoke(base_url, probe_url=None):
    client = build_opener(HTTPCookieProcessor(CookieJar()))

    def request(path, payload=None, csrf=None):
        headers = {}
        body = None
        if payload is not None:
            body = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        if csrf:
            headers["X-CSRF-Token"] = csrf
        target = urljoin(base_url.rstrip("/") + "/", path)
        headers["Origin"] = base_url.rstrip("/")
        with client.open(Request(target, data=body, headers=headers), timeout=20) as response:
            assert response.status == 200, path
            return response.read()

    page = request("/").decode()
    assert re.search(r"<html\b", page, re.IGNORECASE), "frontend HTML missing"
    assets = re.findall(r'(?:src|href)="([^"]+\.(?:js|css))"', page)
    assert assets, "frontend assets missing"
    for asset in assets:
        target = urljoin(base_url, asset)
        assert urlsplit(target).netloc == urlsplit(base_url).netloc, "unexpected asset origin"
        assert len(request(target)) > 100, "empty frontend asset"
    session = json.loads(request("/api/auth/session"))
    assert session["success"] and session["data"]["mode"] == "demo", "demo session unavailable"
    assert session["data"]["login_ready"], "login not ready"
    try:
        request("/finance/api/status")
    except HTTPError as error:
        assert error.code == 401, "unexpected finance authentication response"
    else:
        raise AssertionError("finance accepted an anonymous request")
    logged_in = json.loads(request(
        "/api/auth/login", {"username": "root", "password": "123456"},
        session["data"]["csrf_token"],
    ))
    assert logged_in["success"] and logged_in["data"]["authenticated"], "demo login failed"
    dashboard = json.loads(request("/api/dashboard"))
    assert isinstance(dashboard, dict) and "error" not in dashboard, "dashboard failed"
    finance = json.loads(request("/finance/api/status"))
    assert isinstance(finance, dict) and "version" in finance, "finance proxy failed"
    if probe_url:
        with client.open(probe_url.rstrip("/") + "/_siteadmin_probe/healthz", timeout=10) as response:
            assert response.status == 200, "probe health failed"
    print("HTTP smoke passed: frontend assets, owner login, dashboard, finance proxy and enabled probe.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:18080")
    parser.add_argument("--probe-url")
    options = parser.parse_args()
    smoke(options.base_url, options.probe_url)


if __name__ == "__main__":
    main()
