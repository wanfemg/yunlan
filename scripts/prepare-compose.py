#!/usr/bin/env python3
"""Create one stable Compose file while leaving credentials in the dotenv file."""
from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path
import stat
import subprocess
import sys


MAX_MANIFEST_BYTES = 2 * 1024 * 1024
MAX_ENV_BYTES = 256 * 1024
MAX_OVERLAY_BYTES = 256 * 1024


class PreparationError(Exception):
    pass


class PreparationParser(argparse.ArgumentParser):
    def error(self, message):
        raise PreparationError("参数无效，请使用 --root 指定安装目录。")


def check_managed_file(path, maximum, required=True):
    if path.is_symlink():
        raise PreparationError("受管理的编排或配置路径不能是符号链接。")
    try:
        details = path.stat()
    except FileNotFoundError:
        if required:
            raise PreparationError("缺少必要的编排或配置文件。") from None
        return False
    if not stat.S_ISREG(details.st_mode):
        raise PreparationError("受管理的编排或配置路径必须是普通文件。")
    if details.st_size > maximum:
        raise PreparationError("编排或配置文件超过大小限制。")
    return True


def validate_document(document, overlay):
    if not isinstance(document, dict):
        raise PreparationError("生成的编排内容格式无效。")
    services = document.get("services")
    if not isinstance(services, dict):
        raise PreparationError("生成的编排缺少必要服务。")
    for name in ("initialize-data", "site-admin", "finance", "login-probe"):
        if not isinstance(services.get(name), dict):
            raise PreparationError("生成的编排缺少后台、财务或登录探针服务。")
    profiles = services["login-probe"].get("profiles")
    if not isinstance(profiles, list) or "probe" not in profiles:
        raise PreparationError("生成的编排未保留登录探针的独立启用设置。")
    for name in ("finance", "login-probe"):
        if services[name].get("network_mode") != "service:site-admin":
            raise PreparationError("生成的编排未保留服务网络共享设置。")
    if not overlay:
        return
    networks = document.get("networks", {})
    site_networks = services["site-admin"].get("networks", {})
    if not isinstance(networks, dict) or not isinstance(site_networks, (dict, list)):
        raise PreparationError("生成的编排未保留已有的 Docker 网络连接。")
    for key, expected in overlay.get("networks", {}).items():
        if not isinstance(expected, dict) or expected.get("external") is not True:
            raise PreparationError("自动识别的外部网络配置格式无效。")
        actual = networks.get(key)
        if (not isinstance(actual, dict) or actual.get("external") is not True
                or actual.get("name") != expected.get("name") or key not in site_networks):
            raise PreparationError("生成的编排未完整保留已有的外部网络。")


def atomic_save(path, content):
    helper = Path(__file__).resolve().with_name("configure-install.py")
    specification = importlib.util.spec_from_file_location("yunlan_compose_config", helper)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    module.atomic_write(path, content)


def prepare(root, probe=False):
    if root.is_symlink() or not root.is_dir():
        raise PreparationError("安装目录必须是实际存在的普通目录。")
    root = root.resolve()
    environment = root / ".env"
    source = root / "compose.yaml"
    overlay_path = root / ".yunlan.compose.yaml"
    target = root / ".yunlan.runtime.yaml"
    check_managed_file(environment, MAX_ENV_BYTES)
    check_managed_file(source, MAX_MANIFEST_BYTES)
    has_overlay = check_managed_file(overlay_path, MAX_OVERLAY_BYTES, required=False)
    check_managed_file(target, MAX_MANIFEST_BYTES, required=False)
    overlay = {}
    if has_overlay:
        try:
            overlay = json.loads(overlay_path.read_text(encoding="utf-8-sig"))
        except (ValueError, UnicodeError):
            raise PreparationError("自动识别的网络配置无法读取。") from None
        if not isinstance(overlay, dict) or not isinstance(overlay.get("networks"), dict):
            raise PreparationError("自动识别的网络配置格式无效。")
    command = [
        "docker", "compose", "--project-name", "yunlan",
        "--project-directory", str(root), "--env-file", str(environment),
        "-f", str(source),
    ]
    if has_overlay:
        command.extend(("-f", str(overlay_path)))
    command.extend(("--profile", "probe", "config", "--no-interpolate", "--format", "json"))
    try:
        result = subprocess.run(
            command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            encoding="utf-8", errors="strict", timeout=30, check=False,
        )
    except FileNotFoundError:
        raise PreparationError("未找到 Docker 命令，无法生成编排。") from None
    except subprocess.TimeoutExpired:
        raise PreparationError("生成编排超时，已有文件保持不变。") from None
    if result.returncode:
        raise PreparationError(
            f"生成编排失败（退出码 {result.returncode}），请检查 Compose 是否支持 JSON 输出及保留变量占位符。"
        )
    if not result.stdout.strip() or len(result.stdout.encode("utf-8")) > MAX_MANIFEST_BYTES:
        raise PreparationError("生成的编排为空或超过大小限制。")
    try:
        document = json.loads(result.stdout)
    except ValueError:
        raise PreparationError("Compose 返回了无效的编排内容。") from None
    validate_document(document, overlay)
    if probe:
        document["services"]["login-probe"].pop("profiles", None)
    else:
        document["services"].pop("login-probe")
    # Older Compose emits null defaults which its own YAML reader rejects.
    for service in document["services"].values():
        for key in ("command", "entrypoint"):
            if service.get(key) is None:
                service.pop(key, None)
    content = json.dumps(document, ensure_ascii=False, indent=2) + "\n"
    if len(content.encode("utf-8")) > MAX_MANIFEST_BYTES:
        raise PreparationError("生成的编排超过大小限制。")
    check_managed_file(target, MAX_MANIFEST_BYTES, required=False)
    atomic_save(target, content)
    return target


def main(arguments=None):
    try:
        parser = PreparationParser(add_help=False)
        parser.add_argument("--root", type=Path, required=True)
        parser.add_argument("--probe", action="store_true")
        options = parser.parse_args(arguments)
        target = prepare(options.root, probe=options.probe)
        print(target)
        return 0
    except PreparationError as error:
        print("[失败] " + str(error), file=sys.stderr)
    except Exception:
        print("[失败] 生成单文件编排失败，请检查目录权限与 Compose 配置。", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
