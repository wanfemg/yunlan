#!/usr/bin/env bash
set -Eeuo pipefail
set +x

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"

usage() {
  cat <<'USAGE'
yunlan 安装管理菜单

用法：
  bash scripts/manage-install.sh
  bash scripts/manage-install.sh --help

管理操作保留安装配置和 Docker 持久卷，不提供删除数据的入口。
USAGE
}

for argument in "$@"; do
  case "$argument" in
    --help|-h) usage; exit 0 ;;
    *) printf 'yunlan: 未知参数：%s\n' "$argument" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ! -t 0 || ! -t 1 ]]; then
  printf 'yunlan: 管理菜单需要交互式终端（TTY）。请在终端中运行：bash scripts/manage-install.sh\n' >&2
  exit 2
fi

pause() {
  printf '\n按回车键返回菜单…'
  IFS= read -r _ || true
}

run_command() {
  local label="$1"
  shift
  printf '\n>>> %s\n' "$label"
  if "$@"; then
    printf '\n操作完成。\n'
  else
    local result=$?
    printf '\nyunlan: 操作失败，退出码 %s。\n' "$result" >&2
  fi
  pause
}

require_checkout() {
  if [[ ! -f "$ROOT_DIR/compose.yaml" || ! -f "$ROOT_DIR/deploy.sh" || ! -f "$ROOT_DIR/install.sh" ]]; then
    printf 'yunlan: 当前目录不是完整安装目录：%s\n' "$ROOT_DIR" >&2
    return 1
  fi
}

require_docker_compose() {
  if ! command -v docker >/dev/null 2>&1; then
    printf 'yunlan: 未找到 Docker 命令。请安装 Docker Engine 与 Docker Compose v2 后重试。\n' >&2
    return 1
  fi
  if ! docker compose version >/dev/null 2>&1; then
    printf 'yunlan: 当前 Docker 未提供 Compose v2。请启用 docker compose 插件后重试。\n' >&2
    return 1
  fi
  if ! docker info >/dev/null 2>&1; then
    printf 'yunlan: Docker 服务未运行，或当前账号无权访问 Docker。\n' >&2
    return 1
  fi
}

clear_compose_environment() {
  # Match deploy.sh: persisted .env remains authoritative over the shell environment.
  unset APP_MODE MAIN_DB_DRIVER MAIN_DB_DSN LOG_DB_DRIVER LOG_DB_DSN \
    MANAGEMENT_DB_DRIVER MANAGEMENT_DB_DSN NEWAPI_UPSTREAM NEWAPI_ADMIN_TOKEN \
    NEWAPI_ADMIN_USER_ID BIND_ADDRESS HOST_PORT LOGIN_PROBE_ENABLED \
    PROBE_BIND_ADDRESS PROBE_HOST_PORT PROBE_TRUSTED_PROXIES \
    SITEADMIN_COOKIE_SECURE TIMEZONE ADMIN_TOKEN FINANCE_PROXY_SECRET PROBE_SHARED_SECRET \
    GEOIP_CITY_PATH GEOIP_ASN_PATH GEOIP_ISP_PATH GEOIP_ANONYMOUS_PATH \
    IP_INTELLIGENCE_DIR NETWORK_HISTORY_COMPLETE DATA_DIR SITEADMIN_CONFIG_FILE \
    QUOTA_PER_UNIT YUNLAN_IMAGE YUNLAN_FINANCE_IMAGE YUNLAN_PROBE_IMAGE
}

build_compose_args() {
  local probe_enabled
  [[ -f "$ROOT_DIR/.env" ]] || {
    printf 'yunlan: 尚未生成 .env 配置。请先打开“连接配置”运行部署向导。\n' >&2
    return 1
  }
  [[ ! -L "$ROOT_DIR/.env" && ! -L "$ROOT_DIR/compose.yaml" ]] || {
    printf 'yunlan: .env 或 compose.yaml 是符号链接，已停止管理操作。\n' >&2
    return 1
  }
  if [[ -e "$ROOT_DIR/.yunlan.compose.yaml" && ( ! -f "$ROOT_DIR/.yunlan.compose.yaml" || -L "$ROOT_DIR/.yunlan.compose.yaml" ) ]]; then
    printf 'yunlan: 生成的网络配置不是普通文件，已停止管理操作。\n' >&2
    return 1
  fi
  command -v python3 >/dev/null 2>&1 || {
    printf 'yunlan: 读取登录探针配置需要 Python 3。\n' >&2
    return 1
  }
  probe_enabled="$(python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" --read LOGIN_PROBE_ENABLED)" || {
    printf 'yunlan: 无法读取登录探针配置，已停止管理操作。\n' >&2
    return 1
  }

  COMPOSE_ARGS=(docker compose --project-name yunlan --project-directory "$ROOT_DIR" --env-file "$ROOT_DIR/.env" -f "$ROOT_DIR/compose.yaml")
  if [[ -f "$ROOT_DIR/.yunlan.compose.yaml" ]]; then
    COMPOSE_ARGS+=(-f "$ROOT_DIR/.yunlan.compose.yaml")
  fi
  if [[ "$probe_enabled" == true || "$probe_enabled" == 1 ]]; then
    COMPOSE_ARGS+=(--profile probe)
  fi
  clear_compose_environment
}

service_menu() {
  require_checkout || { pause; return; }
  if ! require_docker_compose; then pause; return; fi
  if ! build_compose_args; then pause; return; fi

  while true; do
    printf '\n========== 服务管理 ==========\n'
    printf '1) 启动服务\n2) 停止服务（保留数据）\n3) 重启服务\n4) 查看服务状态\n5) 查看实时日志\n0) 返回上级菜单\n'
    IFS= read -r -p '请选择：' choice || return
    case "$choice" in
      1) run_command '启动 yunlan 服务' "${COMPOSE_ARGS[@]}" up -d --wait --wait-timeout 180 ;;
      2) run_command '停止 yunlan 服务并保留数据卷' "${COMPOSE_ARGS[@]}" stop ;;
      3) run_command '重启 yunlan 服务' "${COMPOSE_ARGS[@]}" restart ;;
      4) run_command '查看 yunlan 服务状态' "${COMPOSE_ARGS[@]}" ps ;;
      5)
        printf '\n实时日志（按 Ctrl+C 返回本菜单）\n'
        "${COMPOSE_ARGS[@]}" logs --follow --tail 200 || printf '\nyunlan: 日志读取结束或发生错误。\n' >&2
        ;;
      0) return ;;
      *) printf '无效选项，请输入 0–5。\n' ;;
    esac
  done
}

connection_menu() {
  while true; do
    printf '\n========== 连接配置 ==========\n'
    printf '1) 检查已有连接并补全缺失配置，再部署\n0) 返回上级菜单\n'
    printf '修改已保存的数据库连接：进入后台“系统连接”；此处保留已有连接。\n'
    IFS= read -r -p '请选择：' choice || return
    case "$choice" in
      1)
        require_checkout || { pause; continue; }
        if ! require_docker_compose; then pause; continue; fi
        run_command '检查已有连接并补全缺失配置，再部署' env YUNLAN_MANAGE_MENU=1 bash "$ROOT_DIR/deploy.sh"
        ;;
      0) return ;;
      *) printf '无效选项，请输入 0 或 1。\n' ;;
    esac
  done
}

ip_menu() {
  while true; do
    printf '\n========== IP 情报 ==========\n'
    printf '1) 查看 IP 情报数据状态\n2) 立即更新 IP 情报\n0) 返回上级菜单\n'
    IFS= read -r -p '请选择：' choice || return
    case "$choice" in
      1)
        require_checkout || { pause; continue; }
        command -v python3 >/dev/null 2>&1 || { printf 'yunlan: IP 情报管理需要 Python 3。\n' >&2; pause; continue; }
        [[ -f "$ROOT_DIR/scripts/ip-maintenance-cli.py" ]] || { printf 'yunlan: 未找到 IP 情报管理命令。\n' >&2; pause; continue; }
        run_command '查看 IP 情报数据状态' python3 "$ROOT_DIR/scripts/ip-maintenance-cli.py" status
        ;;
      2)
        require_checkout || { pause; continue; }
        command -v python3 >/dev/null 2>&1 || { printf 'yunlan: IP 情报管理需要 Python 3。\n' >&2; pause; continue; }
        [[ -f "$ROOT_DIR/scripts/ip-maintenance-cli.py" ]] || { printf 'yunlan: 未找到 IP 情报管理命令。\n' >&2; pause; continue; }
        run_command '立即更新 IP 情报' python3 "$ROOT_DIR/scripts/ip-maintenance-cli.py" update
        ;;
      0) return ;;
      *) printf '无效选项，请输入 0–2。\n' ;;
    esac
  done
}

help_menu() {
  printf '\n========== 帮助 ==========\n'
  cat <<'HELP'
安装 / 更新：调用仓库根目录的 install.sh --update，仅快进更新源码并保留配置与数据。
服务管理：使用当前 .env、NewAPI 网络 overlay 和已启用的登录探针 profile 管理容器。
连接配置：调用 deploy.sh 检查已有连接、补全缺失配置并部署；已有连接在后台系统连接页修改。
IP 情报：查看情报状态或提交后台更新任务；下载的数据保存在 data/ip-intelligence。

所有停止、重启和更新操作均保留 Docker 持久卷；此菜单没有删除数据的功能。
HELP
  pause
}

main_menu() {
  require_checkout || exit 1
  while true; do
    printf '\n========== yunlan 安装管理 ==========\n'
    printf '1) 安装 / 更新\n2) 服务管理\n3) 连接配置\n4) IP 情报\n5) 帮助\n0) 退出\n'
    IFS= read -r -p '请选择：' choice || break
    case "$choice" in
      1)
        if ! require_docker_compose; then pause; continue; fi
        run_command '更新 yunlan 源码并重新部署' env YUNLAN_MANAGE_MENU=1 bash "$ROOT_DIR/install.sh" --update
        ;;
      2) service_menu ;;
      3) connection_menu ;;
      4) ip_menu ;;
      5) help_menu ;;
      0) break ;;
      *) printf '无效选项，请输入 0–5。\n' ;;
    esac
  done
  printf '已退出 yunlan 安装管理菜单。\n'
}

declare -a COMPOSE_ARGS=()
main_menu
