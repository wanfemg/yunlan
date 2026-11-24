#!/usr/bin/env bash
set -Eeuo pipefail

set +x

fail() { printf 'yunlan: %s\n' "$*" >&2; exit 1; }
for argument in "$@"; do
  if [[ "$argument" == --help || "$argument" == -h ]]; then
    cat <<'USAGE'
yunlan one-line installer

Usage:
  bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) [options]

Options:
  --live                    Configure the existing NewAPI instance (new-install default).
  --demo                    Start sample data; local demo login is root / 123456.
  --non-interactive         Read supplied settings; fail instead of prompting.
  --newapi-container NAME   Inspect only this local NewAPI container.
  --help                    Show this help without cloning or starting Docker.

Environment:
  YUNLAN_INSTALL_DIR         Default /opt/yunlan as root; $HOME/yunlan otherwise.
  YUNLAN_REPOSITORY          GitHub owner/repository; default wanfemg/yunlan.
  YUNLAN_REF                 Branch name; default main.
  MAIN_DB_DRIVER/MAIN_DB_DSN and NEWAPI_UPSTREAM may be supplied explicitly.

Prerequisites: Bash, Git, Python 3, Docker Engine and Docker Compose v2.
Rerunning preserves .env, data and volumes. Modified/diverged source is never reset.
USAGE
    exit 0
  fi
done
repository="${YUNLAN_REPOSITORY:-wanfemg/yunlan}"
ref="${YUNLAN_REF:-main}"
[[ "$repository" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$ && "$repository" != *..* ]] ||
  fail 'YUNLAN_REPOSITORY must be a GitHub owner/repository name.'
[[ "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ && "$ref" != *..* && "$ref" != *//* ]] ||
  fail 'YUNLAN_REF must name a branch, for example main.'

# A checkout invocation deploys that checkout. /dev/fd process substitutions
# take the download/update path instead.
script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [[ -n "$script_directory" && -f "$script_directory/deploy.sh" &&
      -f "$script_directory/compose.yaml" && -f "$script_directory/scripts/configure-install.py" ]]; then
  exec bash "$script_directory/deploy.sh" "$@"
fi
for program in git python3; do
  command -v "$program" >/dev/null 2>&1 || fail "Missing prerequisite: $program."
done
git check-ref-format "refs/heads/$ref" >/dev/null 2>&1 || fail 'Invalid branch name.'

if [[ -n "${YUNLAN_INSTALL_DIR:-}" ]]; then destination="$YUNLAN_INSTALL_DIR"
elif [[ "$EUID" -eq 0 ]]; then destination='/opt/yunlan'
else destination="${HOME:?Set HOME or YUNLAN_INSTALL_DIR}/yunlan"; fi
destination="$(python3 -c 'import os,sys; print(os.path.abspath(os.path.expanduser(sys.argv[1])))' "$destination")"
[[ "$destination" != / && "$destination" != "$HOME" ]] || fail 'Use a dedicated yunlan installation directory.'
[[ ! -L "$destination" ]] || fail 'The installation directory must not be a symbolic link.'
repo_url="https://github.com/${repository}.git"

normalize_remote() {
  local value="$1"
  value="${value%/}"; value="${value%.git}"
  value="${value#https://github.com/}"; value="${value#git@github.com:}"
  printf '%s' "$value"
}
if [[ -d "$destination/.git" ]]; then
  current_remote="$(git -C "$destination" remote get-url origin 2>/dev/null || true)"
  [[ "$(normalize_remote "$current_remote")" == "$repository" ]] ||
    fail "The existing directory belongs to a different repository: $destination"
  [[ -z "$(git -C "$destination" status --porcelain --untracked-files=normal)" ]] ||
    fail "Local source changes exist in $destination; preserve or commit them before updating."
  current_branch="$(git -C "$destination" symbolic-ref --quiet --short HEAD || true)"
  [[ "$current_branch" == "$ref" ]] ||
    fail "The checkout is on ${current_branch:-a detached revision}; use the same YUNLAN_REF or another directory."
  printf 'Updating yunlan in %s\n' "$destination"
  git -C "$destination" fetch --no-tags origin "refs/heads/$ref"
  git -C "$destination" merge-base --is-ancestor HEAD FETCH_HEAD ||
    fail 'Local and published commits diverge; the installer will not reset this checkout.'
  git -C "$destination" merge --ff-only FETCH_HEAD
elif [[ -e "$destination" ]]; then
  fail "The installation directory already exists and is not a yunlan checkout: $destination"
else
  parent_directory="$(dirname -- "$destination")"
  mkdir -p -- "$parent_directory"
  parent_directory="$(cd -- "$parent_directory" && pwd)"
  temporary_checkout="$(mktemp -d "$parent_directory/.yunlan.clone.XXXXXX")"
  cleanup_checkout() {
    case "${temporary_checkout:-}" in
      "$parent_directory"/.yunlan.clone.*)
        [[ ! -L "$temporary_checkout" ]] && rm -rf -- "$temporary_checkout" ;;
    esac
  }
  trap cleanup_checkout EXIT
  printf 'Downloading %s (%s) into %s\n' "$repository" "$ref" "$destination"
  git clone --single-branch --branch "$ref" --origin origin "$repo_url" "$temporary_checkout"
  mv -T -- "$temporary_checkout" "$destination"
  temporary_checkout=''
  trap - EXIT
fi
exec bash "$destination/deploy.sh" "$@"
