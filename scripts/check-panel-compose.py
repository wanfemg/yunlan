#!/usr/bin/env python3
"""验证面板通过单一编排文件读取 yunlan 项目、容器状态和日志。"""

import argparse
import json
from pathlib import Path
import subprocess
import sys


class PanelComposeError(Exception):
    pass


def run_compose(arguments, description):
    try:
        result = subprocess.run(
            ["docker", "compose", *arguments], capture_output=True, text=True,
            encoding="utf-8", errors="replace", timeout=30, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise PanelComposeError(description + "未能完成。") from None
    if result.returncode:
        raise PanelComposeError(description + "失败。")
    if len(result.stdout) > 1 << 20:
        raise PanelComposeError(description + "返回内容超过大小限制。")
    return result.stdout


def json_objects(raw, description):
    try:
        try:
            value = json.loads(raw)
        except ValueError:
            value = [json.loads(line) for line in raw.splitlines() if line.strip()]
        if isinstance(value, dict):
            value = [value]
        if not isinstance(value, list) or any(not isinstance(item, dict) for item in value):
            raise ValueError
        return value
    except (ValueError, TypeError):
        raise PanelComposeError(description + "返回内容无效。") from None


def check_panel_compose(root, *, report=print):
    candidate = Path(root).expanduser()
    if not candidate.is_absolute() or candidate.is_symlink() or not candidate.is_dir():
        raise PanelComposeError("安装目录必须是有效的绝对目录，且不能是符号链接。")
    root = candidate.resolve()
    runtime = root / ".yunlan.runtime.yaml"
    settings = root / ".env"
    if runtime.is_symlink() or not runtime.is_file():
        raise PanelComposeError("面板使用的单一运行编排文件不存在或不是普通文件。")
    if settings.is_symlink() or not settings.is_file():
        raise PanelComposeError("部署配置 .env 不存在或不是普通文件。")
    projects = json_objects(
        run_compose(["ls", "--all", "--format", "json"], "读取面板编排列表"),
        "面板编排列表",
    )
    matched = [item for item in projects if item.get("Name") == "yunlan"]
    if len(matched) != 1:
        raise PanelComposeError("编排列表中没有唯一的 yunlan 项目。")
    config_files = matched[0].get("ConfigFiles")
    if config_files != str(runtime):
        raise PanelComposeError("yunlan 项目未记录预期的单一编排文件；面板仍会查找错误路径。")
    if not Path(config_files).is_file() or Path(config_files).is_symlink():
        raise PanelComposeError("面板编排列表记录的运行文件不存在。")
    arguments = [
        "--project-name", "yunlan", "--project-directory", str(root),
        "--env-file", str(settings), "-f", str(runtime),
    ]
    containers = json_objects(
        run_compose([*arguments, "ps", "--all", "--format", "json"], "读取面板容器状态"),
        "面板容器状态",
    )
    if not containers:
        raise PanelComposeError("面板通过运行编排文件未找到项目容器。")
    services = {item.get("Service") for item in containers if isinstance(item.get("Service"), str)}
    missing = {"site-admin", "finance"} - services
    if missing:
        raise PanelComposeError("面板容器状态缺少必要服务：" + "、".join(sorted(missing)) + "。")
    logs = run_compose([*arguments, "logs", "--no-color", "--tail", "5"], "读取面板项目日志")
    if not logs.strip():
        raise PanelComposeError("面板通过运行编排文件未读取到项目启动日志。")
    report("面板编排验证通过：单一文件存在，yunlan 容器和启动日志均可读取。")


class ChineseArgumentParser(argparse.ArgumentParser):
    def format_usage(self):
        return super().format_usage().replace("usage:", "用法：", 1)

    def format_help(self):
        return super().format_help().replace("usage:", "用法：", 1)

    def error(self, message):
        self.print_usage(sys.stderr)
        self.exit(2, "yunlan: 面板编排检查参数无效；使用 --help 查看用法。\n")


def main(arguments=None):
    parser = ChineseArgumentParser(description=__doc__, add_help=False)
    parser._optionals.title = "选项"
    parser.add_argument("-h", "--help", action="help", help="显示帮助并退出。")
    parser.add_argument("--root", required=True, type=Path, metavar="安装目录", help="当前安装的绝对路径。")
    options = parser.parse_args(arguments)
    try:
        check_panel_compose(options.root)
    except (PanelComposeError, OSError, ValueError) as error:
        message = str(error) if isinstance(error, PanelComposeError) else "读取部署目录失败。"
        print("yunlan: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
