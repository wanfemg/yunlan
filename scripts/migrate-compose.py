#!/usr/bin/env python3
"""将当前安装的旧编排容器迁移为单一运行文件，保留所有数据卷。"""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys


PROJECT_LABEL = "com.docker.compose.project"
WORKING_DIRECTORY_LABEL = "com.docker.compose.project.working_dir"
CONFIG_FILES_LABEL = "com.docker.compose.project.config_files"
INSPECT_FORMAT = '{"Id":{{json .Id}},"Labels":{{json .Config.Labels}}}'
CONTAINER_IDENTIFIER = re.compile(r"[a-f0-9]{12,64}")


class MigrationError(Exception):
    pass


def run_docker(arguments, description, *, timeout=30):
    try:
        result = subprocess.run(
            ["docker", *arguments], capture_output=True, text=True,
            encoding="utf-8", errors="replace", timeout=timeout, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise MigrationError(description + "未能完成。") from None
    if result.returncode:
        raise MigrationError(description + "失败；未完成旧编排迁移。")
    if len(result.stdout) > 1 << 20:
        raise MigrationError(description + "返回内容超过大小限制。")
    return result.stdout


def installation_root(root):
    candidate = Path(root).expanduser()
    if not candidate.is_absolute() or candidate.is_symlink() or not candidate.is_dir():
        raise MigrationError("安装目录必须是有效的绝对目录，且不能是符号链接。")
    resolved = candidate.resolve()
    if any(character in str(resolved) for character in (",", "\n", "\r")):
        raise MigrationError("安装目录不能包含逗号或换行。")
    for name in (".env", ".yunlan.runtime.yaml"):
        path = resolved / name
        if path.is_symlink() or not path.is_file():
            raise MigrationError("缺少有效的部署文件 " + name + "；已停止旧编排迁移。")
    return resolved


def existing_containers(project_name):
    identifiers = run_docker([
        "ps", "-a", "--filter", "label=" + PROJECT_LABEL + "=" + project_name,
        "--format", "{{.ID}}",
    ], "读取当前项目容器").split()
    if not identifiers:
        return []
    if (len(identifiers) > 128 or len(set(identifiers)) != len(identifiers)
            or any(not CONTAINER_IDENTIFIER.fullmatch(item) for item in identifiers)):
        raise MigrationError("Docker 返回的容器标识无效；已停止旧编排迁移。")
    response = run_docker(
        ["inspect", "--format", INSPECT_FORMAT, *identifiers], "检查当前项目容器标签",
    )
    try:
        containers = [json.loads(line) for line in response.splitlines() if line.strip()]
        if len(containers) != len(identifiers):
            raise ValueError
        inspected = set()
        for item in containers:
            if not isinstance(item, dict) or not isinstance(item.get("Id"), str):
                raise ValueError
            identifier = item["Id"]
            if not CONTAINER_IDENTIFIER.fullmatch(identifier) or identifier in inspected:
                raise ValueError
            if sum(identifier.startswith(listed) for listed in identifiers) != 1:
                raise ValueError
            inspected.add(identifier)
        return containers
    except (ValueError, TypeError):
        raise MigrationError("Docker 返回的容器标签无效；已停止旧编排迁移。") from None


def belongs_to_installation(item, root, project_name):
    labels = item.get("Labels")
    if not isinstance(labels, dict) or labels.get(PROJECT_LABEL) != project_name:
        raise MigrationError("容器项目标签不一致；已停止旧编排迁移。")
    directory = labels.get(WORKING_DIRECTORY_LABEL)
    if not isinstance(directory, str) or not directory:
        raise MigrationError("容器缺少安装目录标签；已停止旧编排迁移。")
    path = Path(directory)
    if not path.is_absolute() or path.resolve() != root:
        raise MigrationError("同名编排属于其他安装目录；已停止旧编排迁移。")
    config_files = labels.get(CONFIG_FILES_LABEL)
    if not isinstance(config_files, str) or not config_files:
        raise MigrationError("容器缺少编排文件标签；已停止旧编排迁移。")
    allowed = {root / name for name in ("compose.yaml", ".yunlan.compose.yaml", ".yunlan.runtime.yaml")}
    for filename in config_files.split(","):
        path = Path(filename)
        if not filename or not path.is_absolute() or path.is_symlink() or path.resolve() not in allowed:
            raise MigrationError("容器编排文件不属于当前安装；已停止旧编排迁移。")
    return config_files == str(root / ".yunlan.runtime.yaml")


def migrate(root, project_name="yunlan", *, report=print):
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project_name):
        raise MigrationError("Compose 项目名称无效。")
    root = installation_root(root)
    containers = existing_containers(project_name)
    current = [belongs_to_installation(item, root, project_name) for item in containers]
    if not containers or all(current):
        return False
    report("[编排迁移] 检测到旧编排标签，正在执行一次项目重启准备；已有数据卷保留。")
    run_docker([
        "compose", "--project-name", project_name, "--project-directory", str(root),
        "--env-file", str(root / ".env"), "-f", str(root / ".yunlan.runtime.yaml"),
        "--profile", "probe", "down", "--remove-orphans",
    ], "迁移当前项目的旧编排容器", timeout=180)
    report("[编排迁移] 旧项目容器已移除，数据卷保留；即将按单一运行文件重建。")
    return True


class ChineseArgumentParser(argparse.ArgumentParser):
    def format_usage(self):
        return super().format_usage().replace("usage:", "用法：", 1)

    def format_help(self):
        return super().format_help().replace("usage:", "用法：", 1)

    def error(self, message):
        self.print_usage(sys.stderr)
        self.exit(2, "yunlan: 编排迁移参数无效；使用 --help 查看用法。\n")


def main(arguments=None):
    parser = ChineseArgumentParser(description=__doc__, add_help=False)
    parser._optionals.title = "选项"
    parser.add_argument("-h", "--help", action="help", help="显示帮助并退出。")
    parser.add_argument("--root", required=True, type=Path, metavar="安装目录", help="当前安装的绝对路径。")
    parser.add_argument("--project-name", default="yunlan", metavar="项目名", help="Compose 项目名称，默认 yunlan。")
    options = parser.parse_args(arguments)
    try:
        migrate(options.root, options.project_name)
    except (MigrationError, OSError, ValueError) as error:
        message = str(error) if isinstance(error, MigrationError) else "检查部署目录失败；已停止旧编排迁移。"
        print("yunlan: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
