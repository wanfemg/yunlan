#!/usr/bin/env python3
"""验证编排容器、网页、登录入口和正式模式的数据库连接。"""

import argparse
import json
import re
import subprocess
import sys
import time


PROJECT_LABEL = "com.docker.compose.project"
SERVICE_LABEL = "com.docker.compose.service"
ONEOFF_LABEL = "com.docker.compose.oneoff"
INSPECT_FORMAT = (
    '{"Id":{{json .Id}},"Config":{"Labels":{{json .Config.Labels}}},'
    '"State":{{json .State}}}'
)
HEALTH_COMMAND = "wget -q -T 8 -O - http://127.0.0.1:18080/api/health"
FRONTEND_COMMAND = "wget -q -T 8 -O - http://127.0.0.1:18080/"
SESSION_COMMAND = "wget -q -T 10 -O - http://127.0.0.1:18080/api/auth/session"
CONNECTIONS_COMMAND = (
    '[ -n "${ADMIN_TOKEN:-}" ] || exit 1\n'
    'wget -q -T 20 -O - --header="Authorization: Bearer ${ADMIN_TOKEN}" '
    '--header="Content-Type: application/json" --post-data=\'{}\' '
    "http://127.0.0.1:18080/api/system/connections/check"
)


class VerificationError(Exception):
    pass


def run_docker(arguments, description, *, timeout=25):
    try:
        result = subprocess.run(
            ["docker", *arguments], capture_output=True, text=True,
            encoding="utf-8", errors="replace", timeout=timeout, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise VerificationError(description + "未能完成。") from None
    if result.returncode != 0:
        # Docker errors and API bodies can include credentials; never echo either.
        raise VerificationError(description + "失败。")
    if len(result.stdout) > 1 << 20:
        raise VerificationError(description + "返回内容超出大小限制。")
    return result.stdout


def compose_containers(project_name):
    identifiers = run_docker([
        "ps", "-a", "--filter", "label=" + PROJECT_LABEL + "=" + project_name,
        "--filter", "label=" + ONEOFF_LABEL + "=False", "--format", "{{.ID}}",
    ], "读取编排容器").split()
    if not identifiers:
        return []
    if len(identifiers) > 128 or any(not re.fullmatch(r"[a-f0-9]{12,64}", item) for item in identifiers):
        raise VerificationError("Docker 返回的容器标识无效。")
    raw = run_docker(
        ["inspect", "--format", INSPECT_FORMAT, *identifiers],
        "检查编排容器状态",
    )
    try:
        containers = [json.loads(line) for line in raw.splitlines() if line.strip()]
        if not all(isinstance(item, dict) for item in containers):
            raise ValueError
        return containers
    except (ValueError, TypeError):
        raise VerificationError("Docker 返回的容器状态无效。") from None


def wait_for_services(project_name, timeout, *, probe=False, report=print):
    expected = ("site-admin", "finance", "login-probe") if probe else ("site-admin", "finance")
    deadline = time.monotonic() + timeout
    previous_pending = None
    while True:
        matched = {}
        for item in compose_containers(project_name):
            labels = (item.get("Config") or {}).get("Labels") or {}
            service = labels.get(SERVICE_LABEL)
            if (labels.get(PROJECT_LABEL) != project_name or service not in expected
                    or str(labels.get(ONEOFF_LABEL, "")).lower() == "true"):
                continue
            if service in matched:
                raise VerificationError("服务 " + service + " 匹配到多个容器。")
            matched[service] = item
        pending = []
        ready = {}
        for service in expected:
            item = matched.get(service)
            if item is None:
                pending.append(service)
                continue
            state = item.get("State") or {}
            health = (state.get("Health") or {}).get("Status")
            if state.get("Status") in ("exited", "dead"):
                raise VerificationError("服务 " + service + " 在健康检查通过前已退出。")
            if health == "unhealthy":
                raise VerificationError("服务 " + service + " 的健康检查未通过。")
            if state.get("Running") is True and state.get("Status") == "running" and health == "healthy":
                identifier = item.get("Id", "")
                if not isinstance(identifier, str) or not re.fullmatch(r"[a-f0-9]{12,64}", identifier):
                    raise VerificationError("Docker 返回的服务标识无效。")
                ready[service] = identifier
            else:
                pending.append(service)
        if not pending:
            return ready
        if time.monotonic() >= deadline:
            raise VerificationError("等待服务健康超时：" + "、".join(pending) + "。")
        if pending != previous_pending:
            report("正在等待服务健康：" + "、".join(pending) + "。")
            previous_pending = pending
        time.sleep(min(2, max(0, deadline - time.monotonic())))


def service_request(identifier, script, description):
    return run_docker(["exec", identifier, "/bin/sh", "-ec", script], description)


def response_json(raw, description):
    try:
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError
        return value
    except (ValueError, TypeError):
        raise VerificationError(description + "返回内容无效。") from None


def verify_application(identifier, mode, *, report=print):
    health = response_json(service_request(identifier, HEALTH_COMMAND, "应用健康检查"), "应用健康检查")
    if health.get("status") != "ok":
        raise VerificationError("应用健康检查未通过。")
    frontend = service_request(identifier, FRONTEND_COMMAND, "检查前端页面")
    if not re.search(r"<!doctype\s+html\b|<html\b", frontend, re.IGNORECASE):
        raise VerificationError("前端未返回 HTML 页面。")
    session = response_json(service_request(identifier, SESSION_COMMAND, "检查登录入口"), "检查登录入口")
    data = session.get("data")
    if session.get("success") is not True or not isinstance(data, dict):
        raise VerificationError("登录接口未返回有效的会话信息。")
    if data.get("mode") != mode:
        raise VerificationError("当前运行模式与部署要求不一致。")
    if data.get("login_configured") is not True or data.get("login_ready") is not True:
        raise VerificationError("NewAPI 登录入口尚未就绪。")
    if mode == "demo":
        return
    result = response_json(
        service_request(identifier, CONNECTIONS_COMMAND, "检查正式环境连接"),
        "检查正式环境连接",
    )
    data = result.get("data")
    if result.get("success") is not True or not isinstance(data, dict) or not isinstance(data.get("checks"), list):
        raise VerificationError("正式环境连接检查未返回有效结果。")
    checks = {}
    for item in data["checks"]:
        if not isinstance(item, dict) or not isinstance(item.get("id"), str) or item["id"] in checks:
            raise VerificationError("正式环境连接检查返回了无效结果。")
        checks[item["id"]] = item.get("state")
    for name in ("main", "logs", "management"):
        if checks.get(name) != "connected":
            label = {"main": "主库", "logs": "日志库", "management": "管理库"}[name]
            raise VerificationError(label + "（" + name + "）连接检查未通过。")
    if checks.get("newapi") == "missing":
        report("NewAPI 管理接口尚未配置；后续在后台“系统连接”中填写管理员 ID 和令牌。")
    elif checks.get("newapi") != "connected":
        raise VerificationError("NewAPI 管理接口连接检查未通过。")


def verify_deployment(project_name="yunlan", timeout=180, *, mode="live", probe=False, report=print):
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project_name):
        raise VerificationError("Compose 项目名称无效。")
    if timeout <= 0:
        raise VerificationError("验收超时时间必须大于 0 秒。")
    if mode not in ("live", "demo"):
        raise VerificationError("验收模式必须为 live 或 demo。")
    services = wait_for_services(project_name, timeout, probe=probe, report=report)
    verify_application(services["site-admin"], mode, report=report)
    report("容器健康、网页和登录入口已通过验证" + ("。" if mode == "demo" else "，正式数据库连接已通过验证。"))


class ChineseArgumentParser(argparse.ArgumentParser):
    def format_usage(self):
        return super().format_usage().replace("usage:", "用法：", 1)

    def format_help(self):
        return super().format_help().replace("usage:", "用法：", 1)

    def error(self, message):
        self.print_usage(sys.stderr)
        self.exit(2, "yunlan: 部署验收参数无效；使用 --help 查看用法。\n")


def main():
    parser = ChineseArgumentParser(description=__doc__, add_help=False)
    parser._optionals.title = "选项"
    parser.add_argument("-h", "--help", action="help", help="显示帮助并退出。")
    parser.add_argument("--project-name", default="yunlan", metavar="项目名", help="Compose 项目名称，默认 yunlan。")
    parser.add_argument("--timeout", type=int, default=180, metavar="秒数", help="等待容器健康的超时时间，默认 180 秒。")
    parser.add_argument("--mode", choices=("live", "demo"), default="live", help="验收运行模式，默认 live。")
    parser.add_argument("--probe", action="store_true", help="同时验证登录探针容器。")
    arguments = parser.parse_args()
    try:
        verify_deployment(arguments.project_name, arguments.timeout, mode=arguments.mode, probe=arguments.probe)
    except VerificationError as error:
        print("yunlan: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
