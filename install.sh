#!/usr/bin/env bash
set -Eeuo pipefail

set +x

fail() { printf '\n[失败] %s\n' "$*" >&2; exit 1; }
info() { printf '[提示] %s\n' "$*"; }
success() { printf '[完成] %s\n' "$*"; }
check_docker_requirements() {
  info '正在检查 Docker Engine、Docker daemon 与 Compose v2…'
  command -v docker >/dev/null 2>&1 ||
    fail '未找到 Docker 命令。请先在服务器安装 Docker Engine 和 Docker Compose v2，再重新运行一键安装命令。'
  docker compose version >/dev/null 2>&1 ||
    fail '未检测到 Docker Compose v2 插件。请确认 `docker compose version` 可正常运行，再重新执行安装。'
  docker info >/dev/null 2>&1 ||
    fail 'Docker daemon 当前不可用或当前账号无权访问 Docker。请启动 Docker 服务，并使用有 Docker 权限的账号重新执行。'
  success 'Docker Engine、daemon 与 Compose v2 检查通过。'
}
for argument in "$@"; do
  if [[ "$argument" == --help || "$argument" == -h ]]; then
    cat <<'USAGE'
云澜 · 站长后台一键安装

用法：
  bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) [参数]

常用参数：
  --live                    连接现有 NewAPI，首次安装默认使用此模式。
  --demo                    使用演示数据，演示账号 root，密码 123456。
  --build                   从源码构建镜像；默认直接拉取已发布镜像。
  --update                  更新当前安装目录的源码，再重新部署。
  --menu                    直接打开已有安装的分级管理菜单。
  --non-interactive         无人值守安装；缺少配置时报告原因并退出。
  --newapi-container 名称   指定本机的 NewAPI 容器，跳过自动识别。
  --help                    显示中文帮助，不下载源码、不启动服务。

环境变量：
  YUNLAN_INSTALL_DIR         安装目录；root 默认 /opt/yunlan，普通用户默认 $HOME/yunlan。
  YUNLAN_REPOSITORY          GitHub 仓库，默认 wanfemg/yunlan。
  YUNLAN_REF                 安装或更新的分支，默认 main。
  MAIN_DB_DRIVER / MAIN_DB_DSN / NEWAPI_UPSTREAM 支持提前指定数据库和 NewAPI 地址。

运行环境：Bash、Git、Python 3、Docker Engine 和 Docker Compose v2。
在 NewAPI 所在服务器执行即可自动识别容器与连接信息，缺失项由中文向导补全。
重复执行同一条命令即可更新，已有配置、数据和持久卷保留。
检测到本地源码修改或分支分叉时停止更新，不覆盖本地改动。
USAGE
    exit 0
  fi
done
repository="${YUNLAN_REPOSITORY:-wanfemg/yunlan}"
ref="${YUNLAN_REF:-main}"
[[ "$repository" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ && "$repository" != *..* ]] ||
  fail 'YUNLAN_REPOSITORY 格式错误，应为 GitHub 用户名/仓库名，例如 wanfemg/yunlan。'
[[ "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ && "$ref" != *..* && "$ref" != *//* ]] ||
  fail 'YUNLAN_REF 必须是有效分支名，例如 main。'

update_install=false
menu_only=false
declare -a deploy_args=()
for argument in "$@"; do
  if [[ "$argument" == --update ]]; then update_install=true
  elif [[ "$argument" == --menu ]]; then menu_only=true
  else deploy_args+=("$argument"); fi
done
if [[ "$menu_only" == true && ( "$update_install" == true || "${#deploy_args[@]}" -gt 0 ) ]]; then
  fail '--menu 用于直接进入管理菜单，不能与其他参数同时使用。'
fi

printf '\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n'
printf '             云澜 · 站长后台一键安装\n'
printf '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n'
printf '  自动识别 NewAPI · 自动配置连接 · 保留配置和数据\n'
printf '  安装仓库：%s  |  分支：%s\n' "$repository" "$ref"
printf '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n'
if [[ "$menu_only" == true ]]; then
  info '正在检查打开管理菜单所需的 Git 与 Python 3…'
else
  info '准备 1/3：检查 Git、Python 3、Docker Engine 与 Compose v2…'
fi
for program in git python3; do
  command -v "$program" >/dev/null 2>&1 || fail "缺少必要命令：$program。请先安装该依赖后重新执行安装命令。"
done
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)' ||
  fail '安装配置需要 Python 3.8 或更新版本。'
git check-ref-format "refs/heads/$ref" >/dev/null 2>&1 || fail '分支名无效，请检查 YUNLAN_REF。'
success 'Git 与 Python 3 检查通过。'
if [[ "$menu_only" != true ]]; then check_docker_requirements; fi

normalize_remote() {
  local value="$1"
  value="${value%/}"; value="${value%.git}"
  value="${value#https://github.com/}"; value="${value#git@github.com:}"
  printf '%s' "$value"
}

# A checkout invocation deploys that checkout. /dev/fd process substitutions
# take the download/update path instead.
script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [[ -n "$script_directory" && -f "$script_directory/deploy.sh" &&
      -f "$script_directory/compose.yaml" && -f "$script_directory/scripts/configure-install.py" ]]; then
  if [[ "$menu_only" == true ]]; then
    [[ -f "$script_directory/scripts/manage-install.sh" ]] || fail '当前版本尚无管理菜单，请先更新当前安装目录。'
    info '已找到本地安装，正在打开管理菜单…'
    exec bash "$script_directory/scripts/manage-install.sh"
  fi
  if [[ "$update_install" == true ]]; then
    current_remote="$(git -C "$script_directory" remote get-url origin 2>/dev/null || true)"
    [[ "$(normalize_remote "$current_remote")" == "$repository" ]] ||
      fail "当前目录属于其他仓库，已停止更新：$script_directory"
    current_branch="$(git -C "$script_directory" symbolic-ref --quiet --short HEAD || true)"
    [[ "$current_branch" == "$ref" ]] ||
      fail "当前分支为 ${current_branch:-分离提交状态}，与指定分支 $ref 不一致；请保持 YUNLAN_REF 与当前分支一致。"
    [[ -z "$(git -C "$script_directory" status --porcelain --untracked-files=normal)" ]] ||
      fail "安装目录内存在本地源码改动，已保留并停止更新：$script_directory；请保存或提交改动后重新运行。"
    info "准备 2/3：正在更新已有安装：$script_directory"
    info '正在连接 GitHub 获取最新源码，下面为 Git 原始进度…'
    git -C "$script_directory" fetch --no-tags origin "refs/heads/$ref" ||
      fail '获取最新源码失败，请检查服务器到 GitHub 的网络连接后重新执行。'
    git -C "$script_directory" merge-base --is-ancestor HEAD FETCH_HEAD ||
      fail '本地提交与远端分支已分叉，已保留本地源码并停止更新，请先处理分支差异。'
    git -C "$script_directory" merge --ff-only FETCH_HEAD || fail '源码快进更新失败，已有配置和数据保留。'
    success '已有安装的源码已更新。'
  else
    info "准备 2/3：使用当前安装目录：$script_directory"
  fi
  info '准备 3/3：进入中文部署向导，检查 NewAPI、数据库和服务配置…'
  exec bash "$script_directory/deploy.sh" "${deploy_args[@]}"
fi
if [[ -n "${YUNLAN_INSTALL_DIR:-}" ]]; then destination="$YUNLAN_INSTALL_DIR"
elif [[ "$EUID" -eq 0 ]]; then destination='/opt/yunlan'
else destination="${HOME:?请设置 HOME 或 YUNLAN_INSTALL_DIR 安装目录}/yunlan"; fi
destination="$(python3 -c 'import os,sys; print(os.path.abspath(os.path.expanduser(sys.argv[1])))' "$destination")" ||
  fail '无法解析安装目录，请检查 YUNLAN_INSTALL_DIR。'
[[ "$destination" != / && "$destination" != "${HOME:-}" ]] || fail '请使用独立安装目录，不能直接安装到根目录或用户主目录。'
[[ ! -L "$destination" ]] || fail '安装目录是符号链接，已停止安装，请使用独立的实际目录。'
repo_url="https://github.com/${repository}.git"

if [[ -d "$destination/.git" ]]; then
  current_remote="$(git -C "$destination" remote get-url origin 2>/dev/null || true)"
  [[ "$(normalize_remote "$current_remote")" == "$repository" ]] ||
    fail "安装目录属于其他仓库，已停止更新：$destination"
  if [[ "$menu_only" == true ]]; then
    [[ -f "$destination/scripts/manage-install.sh" ]] || fail '当前版本尚无管理菜单，请去掉 --menu 重新执行安装命令完成更新。'
    info '已找到已有安装，正在打开管理菜单…'
    exec bash "$destination/scripts/manage-install.sh"
  fi
  [[ -z "$(git -C "$destination" status --porcelain --untracked-files=normal)" ]] ||
    fail "安装目录内存在本地源码改动，已保留并停止更新：$destination；请保存或提交改动后重新运行。"
  current_branch="$(git -C "$destination" symbolic-ref --quiet --short HEAD || true)"
  [[ "$current_branch" == "$ref" ]] ||
    fail "当前分支为 ${current_branch:-分离提交状态}，与指定分支 $ref 不一致；请保持 YUNLAN_REF 与当前分支一致。"
  info "准备 2/3：检测到已有安装，正在更新：$destination"
  info '正在连接 GitHub 获取最新源码，下面为 Git 原始进度…'
  git -C "$destination" fetch --no-tags origin "refs/heads/$ref" ||
    fail '获取最新源码失败，请检查服务器到 GitHub 的网络连接后重新执行。'
  git -C "$destination" merge-base --is-ancestor HEAD FETCH_HEAD ||
    fail '本地提交与远端分支已分叉，已保留本地源码并停止更新，请先处理分支差异。'
  git -C "$destination" merge --ff-only FETCH_HEAD || fail '源码快进更新失败，已有配置和数据保留。'
  success '源码更新完成，继续检查已有配置。'
elif [[ -e "$destination" ]]; then
  fail "安装目录已经存在，但不是云澜项目源码目录：$destination；请使用独立安装目录。"
else
  [[ "$menu_only" != true ]] || fail '尚未发现云澜安装，请去掉 --menu 重新执行安装命令。'
  info "准备 2/3：首次安装，源码将保存到：$destination"
  parent_directory="$(dirname -- "$destination")"
  mkdir -p -- "$parent_directory" || fail "无法创建安装目录的上级目录：$parent_directory；请检查目录权限。"
  parent_directory="$(cd -- "$parent_directory" && pwd)" || fail '无法进入安装目录的上级目录。'
  temporary_checkout="$(mktemp -d "$parent_directory/.yunlan.clone.XXXXXX")" || fail '无法创建临时下载目录，请检查磁盘空间和目录权限。'
  cleanup_checkout() {
    case "${temporary_checkout:-}" in
      "$parent_directory"/.yunlan.clone.*)
        [[ ! -L "$temporary_checkout" ]] && rm -rf -- "$temporary_checkout" ;;
    esac
  }
  trap cleanup_checkout EXIT
  info "正在下载 $repository（$ref），下面为 Git 原始进度…"
  git clone --single-branch --branch "$ref" --origin origin "$repo_url" "$temporary_checkout" ||
    fail '源码下载失败，请检查服务器到 GitHub 的网络连接后重新执行安装命令。'
  mv -T -- "$temporary_checkout" "$destination" || fail "无法保存源码到安装目录：$destination；请检查目录权限或路径冲突。"
  temporary_checkout=''
  trap - EXIT
  success '源码下载完成。'
fi
info '准备 3/3：进入中文部署向导，检查 NewAPI、数据库和服务配置…'
exec bash "$destination/deploy.sh" "${deploy_args[@]}"
