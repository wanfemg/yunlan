#!/usr/bin/env python3
"""Prepare yunlan's own files without evaluating a dotenv file as shell code."""
from __future__ import annotations

import argparse
import getpass
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile
from urllib.parse import unquote, urlsplit

from docker_discovery import (
    DiscoveryError,
    container_environment,
    container_host_network_mode,
    container_http_port,
    container_network_names,
    discover_host_gateway_origin,
    inspect_running_containers,
    resolve_database_dsn,
)


class InstallError(Exception):
    pass


class ChineseArgumentParser(argparse.ArgumentParser):
    def format_help(self):
        return (super().format_help().replace("usage: ", "用法：", 1)
                .replace("options:\n", "选项：\n")
                .replace("optional arguments:\n", "选项：\n")
                .replace("positional arguments:\n", "位置参数：\n"))

    def format_usage(self):
        return super().format_usage().replace("usage: ", "用法：", 1)

    def error(self, message):
        if message.startswith("the following arguments are required: "):
            message = "缺少必需参数：" + message.split(": ", 1)[1]
        elif message.startswith("unrecognized arguments: "):
            # Do not echo unknown values: they may contain credentials.
            message = "存在无法识别的参数，执行 --help 查看参数说明。"
        elif "not allowed with argument" in message:
            message = "参数组合冲突，--demo 与 --live 不能同时使用。"
        else:
            message = "参数格式无效，执行 --help 查看参数说明。"
        self.print_usage(sys.stderr)
        self.exit(2, "yunlan：" + message + "\n")


KEY = re.compile(r"^[A-Z][A-Z0-9_]*$")
SAFE_READ_KEYS = {"APP_MODE", "LOGIN_PROBE_ENABLED"}
SAVED_CONNECTION_KEYS = {
    "APP_MODE", "MAIN_DB_DRIVER", "MAIN_DB_DSN", "LOG_DB_DRIVER", "LOG_DB_DSN",
    "MANAGEMENT_DB_DRIVER", "MANAGEMENT_DB_DSN", "NEWAPI_UPSTREAM",
    "NEWAPI_ADMIN_TOKEN", "NEWAPI_ADMIN_USER_ID", "NETWORK_HISTORY_COMPLETE",
    "GEOIP_ISP_PATH", "GEOIP_ANONYMOUS_PATH",
}


def env_value(raw: str) -> str:
    value = raw.strip()
    if not value or value.startswith("#"):
        return ""
    if value[0] not in ("'", '"'):
        return re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
    quote, result, index = value[0], [], 1
    while index < len(value):
        char = value[index]
        if char == "\\" and index + 1 < len(value):
            following = value[index + 1]
            if following == quote or quote == '"' and following in ('\\', "n", "r", "t"):
                result.append({"n": "\n", "r": "\r", "t": "\t"}.get(following, following))
                index += 2
                continue
        if char == quote:
            tail = value[index + 1:].strip()
            if tail and not tail.startswith("#"):
                raise InstallError(".env 配置文件中的引号格式无效。")
            decoded = "".join(result)
            return decoded.replace("$$", "$") if quote == '"' else decoded
        result.append(char)
        index += 1
    raise InstallError(".env 配置文件中有未闭合的引号。")


def read_env(path: Path):
    if path.is_symlink():
        raise InstallError(".env 不能是符号链接。")
    if not path.is_file() or path.stat().st_size > 256 * 1024:
        raise InstallError("缺少部署配置 .env，或文件超过大小限制。")
    return parse_env_text(path.read_text(encoding="utf-8-sig"))


def parse_env_text(content):
    lines, values = content.splitlines(), {}
    for line in lines:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, raw = line.partition("=")
        key = key.strip()
        if not separator or not KEY.fullmatch(key):
            raise InstallError("部署配置 .env 中存在无效的配置行。")
        values[key] = env_value(raw)
    return lines, values


def atomic_write(path: Path, content: str):
    if path.is_symlink():
        raise InstallError("受管理的配置路径不能是符号链接。")
    descriptor, temporary = tempfile.mkstemp(prefix=".yunlan-config-", dir=path.parent)
    try:
        if hasattr(os, "fchmod"):
            os.fchmod(descriptor, 0o600)
        else:
            os.chmod(temporary, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def update_env(path: Path, lines, updates):
    rendered, remaining, emitted = [], dict(updates), set()
    for line in lines:
        key, separator, _ = line.partition("=")
        key = key.strip()
        if separator and key in updates:
            if key not in emitted:
                rendered.append(key + "=" + dotenv_quote(updates[key]))
                emitted.add(key)
                remaining.pop(key, None)
        else:
            rendered.append(line)
    if remaining:
        rendered.append("")
        rendered.extend(key + "=" + dotenv_quote(value) for key, value in remaining.items())
    atomic_write(path, "\n".join(rendered).rstrip() + "\n")


def dotenv_quote(value):
    if any(char in value for char in "\r\n\0"):
        raise InstallError("部署配置值必须写在单行中。")
    # Match Compose's double-quote escape/interpolation rules, including a
    # literal trailing backslash and passwords containing dollar signs.
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace("$", "$$") + '"'


def docker_json(*arguments):
    result = subprocess.run(
        ["docker", *arguments], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=25, check=False,
    )
    if result.returncode:
        raise InstallError("读取 Docker 信息失败；未修改 NewAPI。")
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise InstallError("Docker 返回了无效的检查结果。") from None


def docker_text(*arguments):
    result = subprocess.run(
        ["docker", *arguments], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=60, check=False,
    )
    if result.returncode:
        raise InstallError("无法读取现有站长后台连接配置，已停止部署，现有配置未修改。")
    return result.stdout


def saved_workspace_connections():
    """Read the running workspace or its exact data volume, never print JSON."""
    identifiers = docker_text(
        "ps", "-a", "--filter", "label=com.docker.compose.project=yunlan",
        "--filter", "label=com.docker.compose.service=site-admin", "--format", "{{.ID}}",
    ).split()
    if len(identifiers) > 1:
        raise InstallError("检测到多个站长后台容器，无法确定应读取哪份已保存配置。")
    read_command = (
        "if [ -L /app/data/connections.json ]; then exit 3; "
        "elif [ -e /app/data/connections.json ]; then "
        "[ -f /app/data/connections.json ] || exit 2; "
        "head -c 49153 /app/data/connections.json; fi"
    )
    volume = None
    if identifiers:
        inspected = docker_json("inspect", identifiers[0])
        if not isinstance(inspected, list) or len(inspected) != 1:
            raise InstallError("无法识别现有站长后台实例。")
        container = inspected[0]
        if container.get("State", {}).get("Running"):
            raw = docker_text("exec", identifiers[0], "/bin/sh", "-c", read_command)
        else:
            for mount in container.get("Mounts", []):
                if mount.get("Destination") == "/app/data":
                    if mount.get("Type") != "volume" or not mount.get("Name"):
                        raise InstallError("现有站长后台使用了自定义数据挂载。请先启动服务，再更改部署模式。")
                    volume = str(mount["Name"])
                    break
            if volume is None:
                raise InstallError("无法确定现有站长后台的数据卷。")
    else:
        listed = docker_text(
            "volume", "ls", "--filter", "name=yunlan_site-admin-data",
            "--format", "{{.Name}}",
        ).splitlines()
        if "yunlan_site-admin-data" not in listed:
            return {}
        volume = "yunlan_site-admin-data"
    if volume is not None:
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", volume):
            raise InstallError("现有站长后台的数据卷名称无效。")
        raw = docker_text(
            "run", "--rm", "--network", "none", "--read-only", "--entrypoint", "/bin/sh",
            "--mount", "type=volume,src=" + volume + ",dst=/app/data,readonly",
            "alpine:3.22", "-c", read_command,
        )
    if not raw.strip():
        return {}
    if len(raw.encode("utf-8")) > 49152:
        raise InstallError("已保存的站长后台连接配置超过大小限制。")
    try:
        document = json.loads(raw)
        stored = document["values"]
    except (ValueError, TypeError, KeyError):
        raise InstallError("已保存的站长后台连接配置无效，请恢复配置后再部署。") from None
    if not isinstance(stored, dict) or any(not isinstance(key, str) or not isinstance(value, str) for key, value in stored.items()):
        raise InstallError("已保存的站长后台连接配置格式无效。")
    selected = {key: value for key, value in stored.items() if key in SAVED_CONNECTION_KEYS}
    if selected.get("APP_MODE") not in (None, "", "live", "demo"):
        raise InstallError("已保存的站长后台部署模式无效。")
    return selected


class Terminal:
    """Use separate streams so a non-seekable TTY does not require r+ buffering."""

    def __init__(self):
        self.reader = open("/dev/tty", "r", encoding="utf-8", buffering=1)
        try:
            self.writer = open("/dev/tty", "w", encoding="utf-8", buffering=1)
        except OSError:
            self.reader.close()
            raise

    def write(self, value):
        return self.writer.write(value)

    def flush(self):
        return self.writer.flush()

    def readline(self):
        return self.reader.readline()

    def close(self):
        self.reader.close()
        self.writer.close()


def open_terminal():
    try:
        return Terminal()
    except OSError:
        return None


def prompt(tty, label, default="", secret=False):
    if tty is None:
        raise InstallError("当前配置需要交互式终端。请在终端运行安装命令，或使用 --non-interactive 并提供完整配置。")
    suffix = "（回车保留现有值）" if secret and default else ("（默认：" + default + "）" if default else "")
    if secret:
        value = getpass.getpass(label + suffix + "：", stream=tty)
    else:
        tty.write(label + suffix + "：")
        tty.flush()
        value = tty.readline()
        if value == "":
            raise InstallError("配置输入已关闭。")
        value = value.rstrip("\r\n")
    return value or default


def select_container(args, tty):
    explicit = args.newapi_container or os.environ.get("YUNLAN_NEWAPI_CONTAINER")
    if explicit:
        inspected = docker_json("inspect", explicit)
    else:
        result = subprocess.run(
            ["docker", "ps", "--format", "{{json .}}"], stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace",
            check=False, timeout=25,
        )
        if result.returncode:
            raise InstallError("无法读取正在运行的 Docker 容器列表。")
        running = []
        for line in result.stdout.splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue
            name = str(row.get("Names", ""))
            identifier = str(row.get("ID", ""))
            if identifier:
                running.append((identifier, name, str(row.get("Image", ""))))

        # Follow NewAPI Tools discovery order and token boundaries. The loose
        # old pattern also matched database/helper images containing "newapi".
        selected = next((identifier for identifier, name, _image in running
                         if re.search(r"(^|[-_])new-api([-_]|$)", name, re.IGNORECASE)), "")
        if not selected:
            labeled = subprocess.run(
                ["docker", "ps", "-q", "--filter", "label=com.docker.compose.service=new-api"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                encoding="utf-8", errors="replace", check=False, timeout=25,
            )
            if labeled.returncode:
                raise InstallError("无法读取 Docker Compose 服务标签。")
            selected = next(iter(labeled.stdout.split()), "")
        if not selected:
            selected = next((identifier for identifier, _name, image in running
                             if re.search(r"(^|/)new-api([-_:]|$)", image, re.IGNORECASE)), "")
        if not selected:
            return None
        inspected = docker_json("inspect", selected)
    if not isinstance(inspected, list) or len(inspected) != 1:
        raise InstallError("没有找到指定的 NewAPI 容器。")
    return inspected[0]


def driver_for(dsn):
    if dsn.startswith(("postgres://", "postgresql://")) or re.search(r"(?:^|\s)(?:host|dbname)\s*=", dsn):
        return "postgres", dsn
    if dsn.startswith("mysql://"):
        parsed = urlsplit(dsn)
        if not parsed.hostname or not parsed.path.strip("/"):
            raise InstallError("MySQL 连接地址不完整。")
        host = parsed.hostname
        if ":" in host:
            host = "[" + host + "]"
        converted = unquote(parsed.username or "") + ":" + unquote(parsed.password or "")
        converted += "@tcp(" + host + ":" + str(parsed.port or 3306) + ")/" + parsed.path.lstrip("/")
        if parsed.query:
            converted += "?" + parsed.query
        return "mysql", converted
    if "@tcp(" in dsn and ")/" in dsn:
        return "mysql", dsn
    raise InstallError("目前支持 PostgreSQL 或 MySQL。SQLite 和其他数据库需要手动填写可访问的 PostgreSQL/MySQL 连接地址。")

def dsn_uses_loopback(dsn):
    if dsn.startswith(("postgres://", "postgresql://", "mysql://")):
        return (urlsplit(dsn).hostname or "").lower() in ("localhost", "127.0.0.1", "::1")
    match = re.search(r"@tcp\(([^)]+)\)", dsn)
    if match:
        host = match.group(1).rsplit(":", 1)[0].strip("[]")
        return host.lower() in ("localhost", "127.0.0.1", "::1")
    return bool(re.search(r"(?:^|\s)host\s*=\s*['\"]?(localhost|127\.0\.0\.1|::1)(?:['\"]|\s|$)", dsn))


def validated_origin(value):
    try:
        parsed = urlsplit(value)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username is not None or parsed.password is not None or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
            raise ValueError
        parsed.port
    except ValueError:
        raise InstallError("NewAPI 访问地址必须是 HTTP 或 HTTPS 网站根地址，不能包含账号密码、路径或查询参数。") from None
    return value.rstrip("/")


def main():
    parser = ChineseArgumentParser(description="站长后台安装配置向导。自动识别 NewAPI 容器、数据库和网络；已有配置会保留。", add_help=False)
    parser.add_argument("-h", "--help", action="help", help="显示中文帮助并退出")
    parser.add_argument("--root", required=True, metavar="目录", help="站长后台安装目录（由部署脚本自动传入）")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--demo", action="store_true", help="使用演示数据部署")
    mode.add_argument("--live", action="store_true", help="连接现有 NewAPI 实例（默认模式）")
    parser.add_argument("--non-interactive", action="store_true", help="关闭交互输入，缺少必需配置时退出")
    parser.add_argument("--newapi-container", metavar="名称或ID", help="指定要连接的 NewAPI 容器")
    parser.add_argument("--read", choices=sorted(SAFE_READ_KEYS), help=argparse.SUPPRESS)
    args = parser.parse_args()
    root = Path(args.root).resolve()
    path = root / ".env"
    if args.read:
        _, values = read_env(path)
        if args.read == "APP_MODE":
            stored = saved_workspace_connections()
            print(stored.get("APP_MODE") or values.get("APP_MODE", ""))
            return 0
        print(values.get(args.read, ""))
        return 0
    if not (root / "compose.yaml").is_file():
        raise InstallError("安装目录中缺少 compose.yaml，请从完整的站长后台项目目录运行部署。")
    fresh = not path.exists()
    if fresh:
        lines, saved = parse_env_text((root / ".env.example").read_text(encoding="utf-8-sig"))
    else:
        lines, saved = read_env(path)
    values, updates = dict(saved), {}
    allowed = (
        "APP_MODE", "MAIN_DB_DRIVER", "MAIN_DB_DSN", "LOG_DB_DRIVER", "LOG_DB_DSN",
        "MANAGEMENT_DB_DRIVER", "MANAGEMENT_DB_DSN", "NEWAPI_UPSTREAM",
        "NEWAPI_ADMIN_TOKEN", "NEWAPI_ADMIN_USER_ID", "BIND_ADDRESS", "HOST_PORT",
        "LOGIN_PROBE_ENABLED", "SITEADMIN_COOKIE_SECURE", "TIMEZONE",
        "ADMIN_TOKEN", "FINANCE_PROXY_SECRET", "PROBE_SHARED_SECRET",
        "YUNLAN_IMAGE", "YUNLAN_FINANCE_IMAGE", "YUNLAN_PROBE_IMAGE",
    )
    for key in allowed:
        if os.environ.get(key):
            values[key] = updates[key] = os.environ[key]
    explicit_mode = "demo" if args.demo else "live" if args.live else os.environ.get("APP_MODE")
    stored_connections = saved_workspace_connections()
    stored_mode = stored_connections.get("APP_MODE")
    if explicit_mode and stored_mode and explicit_mode != stored_mode:
        raise InstallError(
            "本次选择的模式与站长后台已保存的模式冲突。"
            "请先在后台“系统连接”中切换模式，再重新运行安装器；现有配置未修改。"
        )
    selected_mode = explicit_mode or stored_mode or values.get("APP_MODE")
    if fresh and not args.demo and not args.live and "APP_MODE" not in os.environ:
        selected_mode = stored_mode or "live"
    if selected_mode not in ("live", "demo"):
        raise InstallError("APP_MODE 只能设置为 live（正式模式）或 demo（演示模式）。")
    values["APP_MODE"] = updates["APP_MODE"] = selected_mode
    # Backend/finance also give these saved fields priority. Reuse them only
    # for effective configuration validation; never copy them back into .env.
    values.update(stored_connections)
    values["APP_MODE"] = selected_mode
    tty = None if args.non_interactive else open_terminal()
    try:
        print("\n========== NewAPI 自动识别与连接配置 ==========", flush=True)
        print("部署模式：" + ("正式模式，连接现有 NewAPI" if selected_mode == "live" else "演示模式，使用示例数据"), flush=True)
        if not fresh or stored_connections:
            print("[保留] 沿用已有安装配置和后台保存的连接，只补全缺失项目。", flush=True)
        container, detected_networks, missing_detected_log = None, [], False
        newapi_networks = []
        if args.newapi_container or os.environ.get("YUNLAN_NEWAPI_CONTAINER") or selected_mode == "live" and (not values.get("MAIN_DB_DSN") or not values.get("NEWAPI_UPSTREAM")):
            print("[检测] 正在查找本机 NewAPI 容器、数据库及 Docker 网络…", flush=True)
            container = select_container(args, tty)
            if container is None:
                print("[提示] 没有找到匹配的 NewAPI 容器，接下来只需补全缺失的连接信息。", flush=True)
        if container:
            try:
                running_containers = inspect_running_containers()
            except DiscoveryError as error:
                raise InstallError(str(error)) from None
            environment = container_environment(container)
            newapi_networks = container_network_names(container)
            detected_networks = list(newapi_networks)
            host_mode = container_host_network_mode(container)
            print("[成功] NewAPI 容器：" + str(container.get("Name", "")).lstrip("/"), flush=True)
            print("[成功] 网络模式：" + ("Host（与宿主机共享网络）" if host_mode else "Docker 桥接网络"), flush=True)
            for prefix, upstream_key, env_key in (
                ("MAIN_DB", "MAIN_DB_DSN", "SQL_DSN"),
                ("LOG_DB", "LOG_DB_DSN", "LOG_SQL_DSN"),
            ):
                source_dsn = values.get(upstream_key) or environment.get(env_key, "")
                if not source_dsn:
                    continue
                saved_workspace_dsn = stored_connections.get(upstream_key) == source_dsn
                try:
                    driver, dsn = driver_for(source_dsn)
                except InstallError:
                    # Keep any explicit/saved value untouched. An unsupported
                    # NewAPI database still needs a clear manual configuration.
                    if values.get(upstream_key):
                        raise
                    if prefix == "MAIN_DB":
                        raise
                    missing_detected_log = True
                    print("[提示] 检测到独立日志库，但当前连接类型不受支持，需要补充可访问的日志库连接。", flush=True)
                    continue
                dsn, database_networks, resolution_message = resolve_database_dsn(
                    dsn, driver, running_containers, newapi_host_mode=host_mode,
                )
                if resolution_message:
                    if saved_workspace_dsn:
                        raise InstallError("已保存的数据库地址无法用于当前站长后台网络，请在后台“系统连接”中更新该连接；原配置已保留。")
                    print("[提示] " + ("主数据库：" if prefix == "MAIN_DB" else "日志数据库：") + resolution_message, flush=True)
                    if prefix == "MAIN_DB":
                        if not values.get(upstream_key):
                            values.pop("MAIN_DB_DRIVER", None)
                        values.pop(upstream_key, None)
                        updates.pop("MAIN_DB_DRIVER", None)
                        updates.pop(upstream_key, None)
                    else:
                        missing_detected_log = True
                        if not values.get(upstream_key):
                            values.pop("LOG_DB_DRIVER", None)
                        values.pop(upstream_key, None)
                        updates.pop("LOG_DB_DRIVER", None)
                        updates.pop(upstream_key, None)
                    continue
                if saved_workspace_dsn and dsn != source_dsn:
                    raise InstallError("已保存的数据库连接需要调整网络地址，请在后台“系统连接”中更新；原配置已保留。")
                detected_networks.extend(database_networks)
                values[prefix + "_DRIVER"] = driver
                values[upstream_key] = dsn
                if not saved_workspace_dsn and saved.get(upstream_key) != dsn:
                    updates[prefix + "_DRIVER"] = driver
                    updates[upstream_key] = dsn
                print("[成功] " + ("主数据库" if prefix == "MAIN_DB" else "独立日志库") + "：" + ("PostgreSQL" if driver == "postgres" else "MySQL") + "，连接信息已自动读取（隐藏密码）。", flush=True)
            if not environment.get("LOG_SQL_DSN") and not values.get("LOG_DB_DSN"):
                print("[成功] 未启用独立日志库，调用日志沿用主数据库。", flush=True)
            detected_networks = list(dict.fromkeys(name for name in detected_networks if name and len(name) <= 200))[:16]
            if not values.get("NEWAPI_UPSTREAM"):
                port = container_http_port(container, environment)
                if host_mode:
                    origin = discover_host_gateway_origin(port)
                    if origin:
                        values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = origin
                        print("[成功] 已识别可访问的 NewAPI 宿主机网关地址。", flush=True)
                    else:
                        print("[提示] NewAPI 使用 Host 网络，未检测到可用的宿主机网关，需要填写可从站长后台访问的 NewAPI 网站地址。", flush=True)
                elif newapi_networks:
                    name = str(container.get("Name", "")).lstrip("/")
                    values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = "http://" + name + ":" + str(port)
                    print("[成功] 已自动配置 NewAPI 内网访问地址，登录将使用现有站长账号和密码。", flush=True)
                else:
                    print("[提示] NewAPI 没有可接入的 Docker 网络，需要填写站长后台可访问的网站地址。", flush=True)
        if selected_mode == "live":
            if not values.get("MAIN_DB_DSN"):
                if args.non_interactive:
                    raise InstallError("尚未识别主数据库连接，请设置 MAIN_DB_DSN，或使用 --newapi-container 指定 NewAPI 容器。")
                print("[手动配置] 未能自动读取主数据库。填写完整连接字符串，数据库类型会自动识别。", flush=True)
                dsn = prompt(tty, "NewAPI 主数据库连接字符串（输入不显示）", secret=True)
                driver, dsn = driver_for(dsn)
                values["MAIN_DB_DRIVER"] = updates["MAIN_DB_DRIVER"] = driver
                values["MAIN_DB_DSN"] = updates["MAIN_DB_DSN"] = dsn
            driver, dsn = driver_for(values["MAIN_DB_DSN"])
            if values.get("MAIN_DB_DRIVER") not in ("", driver) and not (fresh and "MAIN_DB_DRIVER" not in os.environ):
                raise InstallError("MAIN_DB_DRIVER 与数据库连接字符串的类型不一致。")
            if values.get("MAIN_DB_DRIVER") != driver:
                updates["MAIN_DB_DRIVER"] = driver
            if values["MAIN_DB_DSN"] != dsn:
                updates["MAIN_DB_DSN"] = dsn
            values["MAIN_DB_DRIVER"], values["MAIN_DB_DSN"] = driver, dsn
            if missing_detected_log:
                if args.non_interactive:
                    raise InstallError("检测到独立日志库，但无法自动导入，请设置 LOG_DB_DRIVER 和 LOG_DB_DSN。")
                log_dsn = prompt(tty, "独立日志库连接字符串（需能从站长后台访问，输入不显示）", secret=True)
                log_driver, log_dsn = driver_for(log_dsn)
                values["LOG_DB_DRIVER"] = updates["LOG_DB_DRIVER"] = log_driver
                values["LOG_DB_DSN"] = updates["LOG_DB_DSN"] = log_dsn
            if not values.get("NEWAPI_UPSTREAM"):
                if args.non_interactive:
                    raise InstallError("使用站长账号登录需要 NewAPI 网站地址，请设置 NEWAPI_UPSTREAM。")
                values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = prompt(tty, "NewAPI 网站地址（例如 https://api.example.com）")
            origin = validated_origin(values["NEWAPI_UPSTREAM"])
            if values["NEWAPI_UPSTREAM"] != origin or "NEWAPI_UPSTREAM" in updates:
                updates["NEWAPI_UPSTREAM"] = origin
            values["NEWAPI_UPSTREAM"] = origin
        for prefix in ("LOG_DB", "MANAGEMENT_DB"):
            if bool(values.get(prefix + "_DSN")) != bool(values.get(prefix + "_DRIVER")):
                raise InstallError(prefix + "_DSN 和 " + prefix + "_DRIVER 必须同时配置。")
            if values.get(prefix + "_DRIVER") and values[prefix + "_DRIVER"] not in ("postgres", "mysql"):
                raise InstallError(prefix + "_DRIVER 只能设置为 postgres 或 mysql。")
        bind = (values.get("BIND_ADDRESS") if not fresh or os.environ.get("BIND_ADDRESS")
                else "0.0.0.0") or "0.0.0.0"
        if fresh and tty is not None and not os.environ.get("BIND_ADDRESS"):
            print("\n========== 后台访问设置 ==========", flush=True)
            print("1) 通过服务器 IP 访问（监听所有网卡，默认）", flush=True)
            print("2) 仅本机反向代理访问（127.0.0.1）", flush=True)
            print("3) 自定义监听地址", flush=True)
            access_mode = prompt(tty, "请选择访问方式（1–3）", "1")
            if access_mode == "1":
                bind = "0.0.0.0"
            elif access_mode == "2":
                bind = "127.0.0.1"
            elif access_mode == "3":
                bind = prompt(tty, "输入自定义监听地址", "127.0.0.1")
            else:
                raise InstallError("访问方式无效，请输入 1、2 或 3。")
        try:
            ipaddress.ip_address(bind)
        except ValueError:
            raise InstallError("监听地址 BIND_ADDRESS 必须是有效的 IP 地址。") from None
        updates["BIND_ADDRESS"] = bind
        port = values.get("HOST_PORT") or "18080"
        if fresh and tty is not None and not os.environ.get("HOST_PORT"):
            port = prompt(tty, "站长后台访问端口，直接回车使用默认值", port)
        if not port.isdecimal() or not 1 <= int(port) <= 65535:
            raise InstallError("站长后台端口 HOST_PORT 必须是 1 到 65535 之间的整数。")
        updates["HOST_PORT"] = port
        if values.get("SITEADMIN_COOKIE_SECURE", "auto") not in ("auto", "true", "false"):
            raise InstallError("SITEADMIN_COOKIE_SECURE 只能设置为 auto、true 或 false。")
        for key in ("ADMIN_TOKEN", "FINANCE_PROXY_SECRET", "PROBE_SHARED_SECRET"):
            if not values.get(key):
                updates[key] = secrets.token_hex(32)
        update_env(path, lines, updates)
        os.chmod(path, 0o600)
        if detected_networks:
            keys = ["yunlan_external_" + str(index) for index in range(1, len(detected_networks) + 1)]
            overlay = {
                "services": {"site-admin": {"networks": ["default", *keys]}},
                "networks": {key: {"external": True, "name": name} for key, name in zip(keys, detected_networks)},
            }
            atomic_write(root / ".yunlan.compose.yaml", json.dumps(overlay, ensure_ascii=False, indent=2) + "\n")
            print("[成功] 已生成站长后台的网络连接配置，复用 NewAPI 与数据库的现有网络。", flush=True)
        elif container:
            print("[提示] 未找到可复用的自定义 Docker 网络，将使用已配置的数据库和 NewAPI 地址。", flush=True)
        print("[成功] 配置已保存到 .env；数据库密码和内部密钥均未显示。", flush=True)
        print("[访问] 监听地址：" + bind + "，访问端口：" + port, flush=True)
    finally:
        if tty is not None:
            tty.close()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (InstallError, OSError, ValueError, subprocess.TimeoutExpired) as error:
        message = str(error) if isinstance(error, InstallError) else "准备部署失败，请检查目录权限、连接格式及 Docker 服务状态。"
        print("yunlan：" + message, file=sys.stderr)
        raise SystemExit(1)
