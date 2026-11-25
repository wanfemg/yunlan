#!/usr/bin/env bash
set -Eeuo pipefail
set +x

for argument in "$@"; do
  if [[ "$argument" == --help || "$argument" == -h ]]; then
    cat <<'USAGE'
云澜 · 部署当前安装目录

用法：bash deploy.sh [--live | --demo] [--build] [--non-interactive] [--newapi-container 名称]

默认拉取已发布镜像；--build 使用当前目录的源码构建镜像。
首次默认连接现有 NewAPI，已有模式和连接配置优先保留。
启动后检查全部常驻容器、网页、登录入口和正式模式的数据库连接。
USAGE
    exit 0
  fi
done
show_management_menu=true
build_images=false
configuration_args=()
for argument in "$@"; do
  if [[ "$argument" == --non-interactive ]]; then show_management_menu=false; fi
  if [[ "$argument" == --build ]]; then build_images=true
  else configuration_args+=("$argument"); fi
done
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"
deployment_stage='运行环境检查'
compose_args=()
deployment_failed() {
  local result="$1"
  trap - ERR
  printf '\n[部署失败] %s，退出码 %s；当前未通过部署验证。\n' "$deployment_stage" "$result" >&2
  if [[ "${#compose_args[@]}" -gt 0 ]]; then
    python3 "$ROOT_DIR/scripts/deploy-diagnostics.py" --root "$ROOT_DIR" \
      --stage "$deployment_stage" --exit-code "$result" -- "${compose_args[@]}" || true
  fi
  exit "$result"
}
trap 'deployment_failed "$?"' ERR
fail() { printf '[失败] %s\n' "$*" >&2; deployment_failed 1; }
for program in docker python3; do
  command -v "$program" >/dev/null 2>&1 || fail "缺少必要命令：$program。"
done
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' ||
  fail '安装配置需要 Python 3.8 或更新版本。'
docker info >/dev/null 2>&1 || fail 'Docker 服务未运行，或当前账号无权访问 Docker。'
docker compose version >/dev/null 2>&1 || fail '未检测到 Docker Compose v2 插件。'

umask 077
for argument in "$@"; do
  case "$argument" in
    --root|--root=*|--read|--read=*) fail '请使用 --help 中列出的部署参数。' ;;
  esac
done
deployment_stage='连接配置'
printf '\n[1/5] 识别 NewAPI、数据库和网络，保存部署配置…\n'
python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" "${configuration_args[@]}"
# Compose gives process variables priority over --env-file. The helper has
# already persisted supported overrides; the selected --demo/--live mode and
# saved credentials must now be authoritative for all three services.
for variable in APP_MODE MAIN_DB_DRIVER MAIN_DB_DSN LOG_DB_DRIVER LOG_DB_DSN \
  MANAGEMENT_DB_DRIVER MANAGEMENT_DB_DSN NEWAPI_UPSTREAM NEWAPI_ADMIN_TOKEN \
  NEWAPI_ADMIN_USER_ID BIND_ADDRESS HOST_PORT LOGIN_PROBE_ENABLED \
  PROBE_BIND_ADDRESS PROBE_HOST_PORT PROBE_TRUSTED_PROXIES \
  SITEADMIN_COOKIE_SECURE TIMEZONE ADMIN_TOKEN FINANCE_PROXY_SECRET PROBE_SHARED_SECRET \
  GEOIP_CITY_PATH GEOIP_ASN_PATH GEOIP_ISP_PATH GEOIP_ANONYMOUS_PATH \
  IP_INTELLIGENCE_DIR NETWORK_HISTORY_COMPLETE DATA_DIR SITEADMIN_CONFIG_FILE \
  QUOTA_PER_UNIT YUNLAN_IMAGE YUNLAN_FINANCE_IMAGE YUNLAN_PROBE_IMAGE; do
  unset "$variable"
done
[[ ! -L .env ]] || fail '.env 不能是符号链接。'
[[ ! -L data && ! -L data/ip-intelligence ]] ||
  fail '受管理的数据目录不能是符号链接。'
mkdir -p data/ip-intelligence
probe_enabled="$(python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" --read LOGIN_PROBE_ENABLED)"
prepare_args=(--root "$ROOT_DIR")
if [[ "$probe_enabled" == true || "$probe_enabled" == 1 ]]; then prepare_args+=(--probe); fi

# Rendered Compose config contains secrets, so validate without printing it.
deployment_stage='容器编排校验'
printf '\n[2/5] 生成并校验宝塔可读取的单文件编排…\n'
runtime_compose="$(python3 "$ROOT_DIR/scripts/prepare-compose.py" "${prepare_args[@]}")"
[[ "$runtime_compose" == "$ROOT_DIR/.yunlan.runtime.yaml" ]] || fail '生成的运行编排路径无效。'
compose_args=(--project-name yunlan --project-directory "$ROOT_DIR" --env-file "$ROOT_DIR/.env" -f "$runtime_compose")
docker compose "${compose_args[@]}" config --quiet
if [[ "$build_images" == true ]]; then
  deployment_stage='源码镜像构建'
  printf '\n[3/5] 从当前源码构建镜像，下面为 Docker 构建进度…\n'
  docker compose "${compose_args[@]}" pull initialize-data
  docker compose "${compose_args[@]}" build
else
  deployment_stage='已发布镜像拉取'
  printf '\n[3/5] 拉取已发布镜像，下面为 Docker 下载进度…\n'
  docker compose "${compose_args[@]}" pull
fi
deployment_stage='容器创建和启动'
printf '\n[4/5] 创建并启动后台、财务与已启用的探针…\n'
python3 "$ROOT_DIR/scripts/migrate-compose.py" --root "$ROOT_DIR"
docker compose "${compose_args[@]}" up -d --no-build --remove-orphans
mode="$(python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" --read APP_MODE)"
verify_args=(--project-name yunlan --mode "$mode" --compose-file "$runtime_compose")
if [[ "$probe_enabled" == true || "$probe_enabled" == 1 ]]; then verify_args+=(--probe); fi
deployment_stage='服务健康和业务连接验证'
printf '\n[5/5] 等待常驻容器健康，验证网页、登录入口与业务连接…\n'
python3 "$ROOT_DIR/scripts/verify-deployment.py" "${verify_args[@]}"
python3 "$ROOT_DIR/scripts/check-panel-compose.py" --root "$ROOT_DIR"
docker compose "${compose_args[@]}" ps --all
published_address="$(docker compose "${compose_args[@]}" port site-admin 18080)"
printf '\n[部署成功] 全部必要服务与连接验证通过。\n'
printf '[监听] %s\n' "$published_address"
case "$published_address" in
  0.0.0.0:*) printf '[访问] 使用服务器 IP，端口 %s；同机访问 http://127.0.0.1:%s\n' "${published_address##*:}" "${published_address##*:}" ;;
  \[::\]:*) printf '[访问] 使用服务器 IPv6 地址，端口 %s。\n' "${published_address##*:}" ;;
  *) printf '[访问] http://%s\n' "$published_address" ;;
esac
if [[ "$mode" == demo ]]; then printf '[登录] 演示账号 root，密码 123456。\n'
else printf '[登录] 使用现有 NewAPI 站长账号和密码，圈钱中心共用登录。\n'; fi
printf '[配置] %s/.env；已有连接、机器密钥和持久数据保留。\n' "$ROOT_DIR"
printf '[宝塔编排文件] %s\n' "$runtime_compose"
printf '[IP 情报] 首次后台初始化，进度在 IP 分析页面查看。\n'
if [[ "$probe_enabled" != true && "$probe_enabled" != 1 ]]; then
  printf '[探针] 当前未启用；配置上游与路由后可启用独立探针。\n'
fi
trap - ERR
if [[ "$show_management_menu" == true && -t 0 && -t 1 && "${YUNLAN_MANAGE_MENU:-}" != 1 ]]; then
  exec bash "$ROOT_DIR/scripts/manage-install.sh"
fi
