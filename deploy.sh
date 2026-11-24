#!/usr/bin/env bash
set -Eeuo pipefail
set +x

for argument in "$@"; do
  if [[ "$argument" == --help || "$argument" == -h ]]; then
    cat <<'USAGE'
Deploy the current yunlan checkout.

Usage: bash deploy.sh [--live | --demo] [--non-interactive] [--newapi-container NAME]

New installations default to live mode. Existing .env mode is preserved unless
--live or --demo is supplied. Owner login uses NEWAPI_UPSTREAM.
Database connections are entered without echo when automatic discovery is unavailable.
No NewAPI files, existing deployment data or unrelated Docker resources are changed.
USAGE
    exit 0
  fi
done
ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"
fail() { printf 'yunlan: %s\n' "$*" >&2; exit 1; }
for program in docker python3; do
  command -v "$program" >/dev/null 2>&1 || fail "Missing prerequisite: $program."
done
docker info >/dev/null 2>&1 || fail 'Docker is not running or this account cannot use the Docker daemon.'
docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 is required.'
docker compose up --help | grep -F -- '--wait-timeout' >/dev/null ||
  fail 'Update Docker Compose v2 to a version supporting up --wait and --wait-timeout.'

umask 077
for argument in "$@"; do
  case "$argument" in
    --root|--root=*|--read|--read=*) fail 'Use only the public deployment options listed by --help.' ;;
  esac
done
python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" "$@"
# Compose gives process variables priority over --env-file. The helper has
# already persisted supported overrides; the selected --demo/--live mode and
# saved credentials must now be authoritative for all three services.
for variable in APP_MODE MAIN_DB_DRIVER MAIN_DB_DSN LOG_DB_DRIVER LOG_DB_DSN \
  MANAGEMENT_DB_DRIVER MANAGEMENT_DB_DSN NEWAPI_UPSTREAM NEWAPI_ADMIN_TOKEN \
  NEWAPI_ADMIN_USER_ID BIND_ADDRESS HOST_PORT LOGIN_PROBE_ENABLED \
  SITEADMIN_COOKIE_SECURE TIMEZONE ADMIN_TOKEN FINANCE_PROXY_SECRET PROBE_SHARED_SECRET; do
  unset "$variable"
done
[[ ! -L .env ]] || fail '.env must not be a symbolic link.'
[[ ! -L data && ! -L data/ip-intelligence ]] ||
  fail 'The managed data/ip-intelligence directory must not be a symbolic link.'
mkdir -p data/ip-intelligence
compose_args=(--project-name yunlan --project-directory "$ROOT_DIR" --env-file "$ROOT_DIR/.env" -f "$ROOT_DIR/compose.yaml")
if [[ -f "$ROOT_DIR/.yunlan.compose.yaml" ]]; then
  [[ ! -L "$ROOT_DIR/.yunlan.compose.yaml" ]] || fail 'The generated network file must not be a symbolic link.'
  compose_args+=(-f "$ROOT_DIR/.yunlan.compose.yaml")
fi
probe_enabled="$(python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" --read LOGIN_PROBE_ENABLED)"
if [[ "$probe_enabled" == true || "$probe_enabled" == 1 ]]; then compose_args+=(--profile probe); fi

# Rendered Compose config contains secrets, so validate without printing it.
docker compose "${compose_args[@]}" config --quiet
docker compose "${compose_args[@]}" up -d --build --wait --wait-timeout 180
docker compose "${compose_args[@]}" ps
published_address="$(docker compose "${compose_args[@]}" port site-admin 18080)"
mode="$(python3 "$ROOT_DIR/scripts/configure-install.py" --root "$ROOT_DIR" --read APP_MODE)"
printf '\nyunlan started. Published address: %s\n' "$published_address"
if [[ "$mode" == demo ]]; then printf 'Demo login: root / 123456 (sample data only).\n'
else printf 'Sign in with the existing NewAPI owner username and password.\n'; fi
printf 'Finance shares this login. Configure AI and DingTalk in the workspace.\n'
printf 'Configuration: %s/.env; existing configuration and persistent volumes were preserved.\n' "$ROOT_DIR"
printf 'Update: rerun the one-line installer. Stop from this directory: docker compose --project-name yunlan --profile probe down\n'
if [[ "$probe_enabled" != true && "$probe_enabled" != 1 ]]; then
  printf 'Login probe is not enabled. Configure its upstream/routing before enabling the probe profile.\n'
fi
