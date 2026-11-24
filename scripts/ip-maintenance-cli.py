#!/usr/bin/env python3
"""Read or queue IP intelligence maintenance through the local yunlan API."""
from __future__ import annotations

import argparse
import importlib.util
import ipaddress
import json
from pathlib import Path
import sys
from urllib.error import HTTPError, URLError
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


class LocalOnlyRedirects(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def load_env(root: Path) -> dict[str, str]:
    helper = root / "scripts" / "configure-install.py"
    spec = importlib.util.spec_from_file_location("yunlan_install_config", helper)
    if spec is None or spec.loader is None:
        raise RuntimeError("无法加载部署配置读取器。")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    try:
        _, values = module.read_env(root / ".env")
    except module.InstallError as error:
        raise RuntimeError(str(error)) from None
    return values


def local_origin(values: dict[str, str]) -> str:
    address = values.get("BIND_ADDRESS", "127.0.0.1") or "127.0.0.1"
    try:
        parsed = ipaddress.ip_address(address)
    except ValueError as error:
        raise RuntimeError("BIND_ADDRESS 不是有效的 IP 地址。") from error
    if parsed.is_unspecified:
        address = "127.0.0.1" if parsed.version == 4 else "::1"
    host = f"[{address}]" if ":" in address else address
    port = values.get("HOST_PORT", "18080") or "18080"
    if not port.isdecimal() or not 1 <= int(port) <= 65535:
        raise RuntimeError("HOST_PORT 不是有效的 TCP 端口。")
    return f"http://{host}:{port}"


def request_api(origin: str, token: str, path: str, method: str = "GET") -> dict:
    request = Request(origin + path, method=method, headers={"Authorization": "Bearer " + token})
    try:
        # Never forward the local machine token through an environment proxy
        # or follow redirects to another origin.
        opener = build_opener(ProxyHandler({}), LocalOnlyRedirects())
        with opener.open(request, timeout=8) as response:
            payload = json.loads(response.read(256 * 1024))
    except HTTPError as error:
        raise RuntimeError(f"本机 yunlan 接口返回 HTTP {error.code}。") from None
    except (URLError, TimeoutError, OSError, json.JSONDecodeError) as error:
        raise RuntimeError("无法连接本机 yunlan；先启动服务再重试。") from error
    if not isinstance(payload, dict) or payload.get("success") is not True:
        raise RuntimeError("本机 yunlan 接口返回的数据格式无效。")
    return payload.get("data") if isinstance(payload.get("data"), dict) else {}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("status", "update"))
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    try:
        root = args.root.resolve()
        values = load_env(root)
        token = values.get("ADMIN_TOKEN", "")
        if not token:
            raise RuntimeError("yunlan 配置中缺少 ADMIN_TOKEN。")
        origin = local_origin(values)
        if args.action == "update":
            result = request_api(origin, token, "/api/ip/intelligence/update", "POST")
            print("IP 情报更新已排队或正在执行；请通过状态菜单查看下载结果。")
            state = result.get("state")
            stage = result.get("stage")
            if state:
                print("任务状态：" + str(state) + (" · " + str(stage) if stage else ""))
            return 0
        result = request_api(origin, token, "/api/ip/maintenance")
        maintenance = result.get("maintenance", {})
        intelligence = result.get("intelligence", {})
        if not isinstance(maintenance, dict) or not isinstance(intelligence, dict):
            raise RuntimeError("IP 情报状态数据不完整。")
        print("IP 情报维护状态：" + str(maintenance.get("state", "unknown")))
        if maintenance.get("stage"):
            print("当前阶段：" + str(maintenance["stage"]))
        if maintenance.get("last_error"):
            print("最近错误：" + str(maintenance["last_error"]))
        geo = intelligence.get("geo")
        for key, label in (("city_available", "城市地理库"), ("asn_available", "ASN 库"),
                           ("isp_available", "ISP 库"), ("anonymous_available", "匿名代理库")):
            print(label + "：" + ("可用" if isinstance(geo, dict) and geo.get(key) else "未加载"))
        print("代理与网络名单：" + ("至少一份可用，详见逐项状态" if intelligence.get("available") else "暂不可用"))
        files = intelligence.get("files")
        if isinstance(files, list):
            for item in files:
                if isinstance(item, dict):
                    status = {"loaded": "已加载", "missing": "缺失", "error": "错误"}.get(
                        str(item.get("status", "")), str(item.get("status", "未知"))
                    )
                    print(f"  {item.get('name', '数据项')}：{status}")
        return 0
    except (OSError, RuntimeError, ValueError) as error:
        print("yunlan IP 情报管理：" + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
