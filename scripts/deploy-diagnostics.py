#!/usr/bin/env python3
"""Show bounded, credential-redacted Compose diagnostics after deployment fails."""
from __future__ import annotations

import argparse
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote, urlsplit

from docker_discovery import _pg_conninfo_fields


REDACTED = "<已隐藏>"
MAX_LINES = 160
MAX_BYTES = 16 * 1024


class DiagnosticParser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("诊断参数无效。")


def read_settings(root):
    helper = Path(__file__).resolve().with_name("configure-install.py")
    specification = importlib.util.spec_from_file_location("yunlan_diagnostic_config", helper)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    _lines, values = module.read_env(root / ".env")
    return values


def secret_values(values):
    secrets = set()
    for key, value in values.items():
        if not value or not key.endswith(("_DSN", "TOKEN", "SECRET")):
            continue
        secrets.add(value)
        if key.endswith("_DSN"):
            if "://" in value:
                try:
                    password = urlsplit(value).password
                    if password:
                        secrets.update((password, unquote(password)))
                except ValueError:
                    pass
            mysql = re.match(r"[^:]*:(.*)@(?:tcp[46]?|unix)\(", value)
            if mysql and mysql.group(1):
                secrets.add(mysql.group(1))
            fields = _pg_conninfo_fields(value)
            if fields:
                secrets.update(str(field["value"]) for field in fields
                               if field["key"] == "password" and field["value"])
    secrets.update(json.dumps(value, ensure_ascii=False)[1:-1] for value in tuple(secrets))
    return sorted(secrets, key=len, reverse=True)


def redact(text, secrets=()):
    for secret in secrets:
        text = text.replace(secret, REDACTED)
    text = re.sub(
        r"""(?ix)(["'](?:[\w-]*(?:token|secret|password|dsn)|passwd|pwd|
            (?:proxy[-_])?authorization|api[_-]?key)["']\s*:\s*)
            (?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^,\s}\]]+)""",
        lambda match: match.group(1) + '"' + REDACTED + '"',
        text,
    )
    text = re.sub(
        r"""(?i)(\b(?:proxy-)?authorization\s*[:=]\s*)
            (?:bearer|basic)\s+[^\s"',;}\]]+""",
        lambda match: match.group(1) + REDACTED,
        text,
        flags=re.VERBOSE,
    )
    text = re.sub(
        r"""(?im)\b([\w-]*dsn)\s*[:=]\s*
            (?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\r\n]+)""",
        lambda match: match.group(1) + "=" + REDACTED,
        text,
        flags=re.VERBOSE,
    )
    text = re.sub(
        r"(?i)\b([a-z][a-z0-9+.-]*://)[^\s/]*@",
        lambda match: match.group(1) + REDACTED + "@",
        text,
    )
    text = re.sub(
        r"(?<![\w])[^:\s/@]+:[^\r\n]*?@(?=(?:tcp[46]?|unix)\()",
        REDACTED + "@",
        text,
    )
    text = re.sub(
        r"""(?ix)\b([\w-]*(?:password|passwd|pwd|token|secret)|authorization|api[_-]?key)
            \s*[:=]\s*
            (?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|[^\s,;]+)""",
        lambda match: match.group(1) + "=" + REDACTED,
        text,
    )
    return text


def run_compose(arguments, operation):
    result = subprocess.run(
        ["docker", "compose", *arguments, *operation],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=20,
        check=False,
    )
    return result.returncode, result.stdout, result.stderr


def has_containers(output):
    return any(line.strip() and not re.match(r"^\s*NAME\s+", line)
               for line in output.splitlines())


def bounded_output(text):
    lines = text.splitlines()
    if len(lines) > MAX_LINES:
        lines = [lines[0], "[诊断输出已截断]", *lines[-(MAX_LINES - 2):]]
    text = "\n".join(lines) + "\n"
    encoded = text.encode("utf-8")
    if len(encoded) > MAX_BYTES:
        heading = lines[0].encode("utf-8")[:1024].decode("utf-8", errors="ignore")
        heading = (heading + "\n[诊断输出已截断]\n").encode("utf-8")
        remaining = MAX_BYTES - len(heading)
        text = heading.decode("utf-8") + encoded[-remaining:].decode("utf-8", errors="ignore")
    return text


def main(arguments=None):
    output = []
    secrets = []
    config_readable = False
    try:
        arguments = list(sys.argv[1:] if arguments is None else arguments)
        separator = arguments.index("--")
        compose_arguments = arguments[separator + 1:]
        parser = DiagnosticParser(add_help=False)
        parser.add_argument("--root", required=True, type=Path)
        parser.add_argument("--stage", required=True)
        parser.add_argument("--exit-code", required=True, type=int)
        options = parser.parse_args(arguments[:separator])
        output.append(f"[失败] 部署阶段：{options.stage}；退出码：{options.exit_code}。")
        try:
            secrets = secret_values(read_settings(options.root))
            config_readable = True
        except Exception:
            output.append("[诊断] 配置读取失败，已隐藏原始状态和日志以避免输出凭据。")
        for label, operation in (
            ("容器状态（包括已退出）", ["ps", "--all"]),
            ("最近的启动日志", ["logs", "--no-color", "--tail", "60"]),
        ):
            output.append("[" + label + "]")
            if operation[0] == "logs" and not config_readable:
                output.append("[诊断] 配置不可读取，已跳过日志采集。")
                continue
            try:
                result, stdout, stderr = run_compose(compose_arguments, operation)
                if result:
                    output.append(f"[诊断] 读取失败，退出码：{result}。")
                elif operation[0] == "ps" and not has_containers(stdout):
                    output.append("当前编排没有容器；请查看部署失败阶段和启动日志。")
                elif operation[0] == "ps" and not config_readable:
                    output.append("已检测到现有容器；已隐藏详细输出。")
                if config_readable and stdout.strip():
                    output.append(stdout.rstrip())
                if config_readable and stderr.strip():
                    output.append(stderr.rstrip())
            except FileNotFoundError:
                output.append("[诊断] Docker 命令不可用，无法读取。")
            except subprocess.TimeoutExpired:
                output.append("[诊断] 读取超时，已结束本次诊断。")
            except Exception:
                output.append("[诊断] 读取失败；原部署错误码保持不变。")
    except Exception:
        output.append("[诊断] 未能生成完整诊断；原部署错误码保持不变。")
    try:
        print(bounded_output(redact("\n".join(output), secrets)), end="", file=sys.stderr)
    except Exception:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
