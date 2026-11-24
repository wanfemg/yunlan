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
                raise InstallError("Invalid quoted value in .env.")
            decoded = "".join(result)
            return decoded.replace("$$", "$") if quote == '"' else decoded
        result.append(char)
        index += 1
    raise InstallError("Unterminated quoted value in .env.")


def read_env(path: Path):
    if path.is_symlink():
        raise InstallError(".env must not be a symbolic link.")
    if not path.is_file() or path.stat().st_size > 256 * 1024:
        raise InstallError("The deployment .env is missing or exceeds its size limit.")
    return parse_env_text(path.read_text(encoding="utf-8-sig"))


def parse_env_text(content):
    lines, values = content.splitlines(), {}
    for line in lines:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, raw = line.partition("=")
        key = key.strip()
        if not separator or not KEY.fullmatch(key):
            raise InstallError("The deployment .env contains an invalid setting line.")
        values[key] = env_value(raw)
    return lines, values


def atomic_write(path: Path, content: str):
    if path.is_symlink():
        raise InstallError("A managed configuration path must not be a symbolic link.")
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
        raise InstallError("Deployment values must use a single line.")
    # Match Compose's double-quote escape/interpolation rules, including a
    # literal trailing backslash and passwords containing dollar signs.
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace("$", "$$") + '"'


def docker_json(*arguments):
    result = subprocess.run(
        ["docker", *arguments], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=25, check=False,
    )
    if result.returncode:
        raise InstallError("Docker inspection failed; NewAPI was not changed.")
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise InstallError("Docker returned an invalid inspection response.") from None


def docker_text(*arguments):
    result = subprocess.run(
        ["docker", *arguments], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=60, check=False,
    )
    if result.returncode:
        raise InstallError("Cannot read the existing yunlan connection configuration; deployment was stopped without changing it.")
    return result.stdout


def saved_workspace_connections():
    """Read the running workspace or its exact data volume, never print JSON."""
    identifiers = docker_text(
        "ps", "-a", "--filter", "label=com.docker.compose.project=yunlan",
        "--filter", "label=com.docker.compose.service=site-admin", "--format", "{{.ID}}",
    ).split()
    if len(identifiers) > 1:
        raise InstallError("Multiple yunlan site-admin containers exist; cannot determine the authoritative saved mode.")
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
            raise InstallError("Cannot identify the existing yunlan workspace.")
        container = inspected[0]
        if container.get("State", {}).get("Running"):
            raw = docker_text("exec", identifiers[0], "/bin/sh", "-c", read_command)
        else:
            for mount in container.get("Mounts", []):
                if mount.get("Destination") == "/app/data":
                    if mount.get("Type") != "volume" or not mount.get("Name"):
                        raise InstallError("The stopped yunlan workspace uses a custom data mount; start it before changing deployment mode.")
                    volume = str(mount["Name"])
                    break
            if volume is None:
                raise InstallError("Cannot determine the existing yunlan data volume.")
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
            raise InstallError("The existing yunlan data-volume name is invalid.")
        raw = docker_text(
            "run", "--rm", "--network", "none", "--read-only", "--entrypoint", "/bin/sh",
            "--mount", "type=volume,src=" + volume + ",dst=/app/data,readonly",
            "alpine:3.22", "-c", read_command,
        )
    if not raw.strip():
        return {}
    if len(raw.encode("utf-8")) > 49152:
        raise InstallError("The saved yunlan connection configuration exceeds its supported size.")
    try:
        document = json.loads(raw)
        stored = document["values"]
    except (ValueError, TypeError, KeyError):
        raise InstallError("The saved yunlan connection configuration is invalid; recover it before deployment.") from None
    if not isinstance(stored, dict) or any(not isinstance(key, str) or not isinstance(value, str) for key, value in stored.items()):
        raise InstallError("The saved yunlan connection configuration has an invalid format.")
    selected = {key: value for key, value in stored.items() if key in SAVED_CONNECTION_KEYS}
    if selected.get("APP_MODE") not in (None, "", "live", "demo"):
        raise InstallError("The saved yunlan workspace has an invalid APP_MODE.")
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
        raise InstallError("Configuration needs a terminal; provide the required environment values or use --non-interactive.")
    suffix = " [Enter keeps the existing value]" if secret and default else (" [" + default + "]" if default else "")
    if secret:
        value = getpass.getpass(label + suffix + ": ", stream=tty)
    else:
        tty.write(label + suffix + ": ")
        tty.flush()
        value = tty.readline()
        if value == "":
            raise InstallError("Configuration input was closed.")
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
            raise InstallError("Cannot enumerate running Docker containers.")
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
                raise InstallError("Cannot inspect Docker Compose service labels.")
            selected = next(iter(labeled.stdout.split()), "")
        if not selected:
            selected = next((identifier for identifier, _name, image in running
                             if re.search(r"(^|/)new-api([-_:]|$)", image, re.IGNORECASE)), "")
        if not selected:
            return None
        inspected = docker_json("inspect", selected)
    if not isinstance(inspected, list) or len(inspected) != 1:
        raise InstallError("The selected NewAPI container was not found.")
    return inspected[0]


def driver_for(dsn):
    if dsn.startswith(("postgres://", "postgresql://")) or re.search(r"(?:^|\s)(?:host|dbname)\s*=", dsn):
        return "postgres", dsn
    if dsn.startswith("mysql://"):
        parsed = urlsplit(dsn)
        if not parsed.hostname or not parsed.path.strip("/"):
            raise InstallError("The MySQL URI is incomplete.")
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
    raise InstallError("PostgreSQL/MySQL are required; SQLite and unsupported DSNs need an explicit supported connection.")

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
        raise InstallError("NEWAPI_UPSTREAM must be an HTTP(S) origin without credentials, path or query.") from None
    return value.rstrip("/")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--demo", action="store_true")
    mode.add_argument("--live", action="store_true")
    parser.add_argument("--non-interactive", action="store_true")
    parser.add_argument("--newapi-container")
    parser.add_argument("--read", choices=sorted(SAFE_READ_KEYS))
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
        raise InstallError("Run deployment from a valid yunlan checkout.")
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
    )
    for key in allowed:
        if os.environ.get(key):
            values[key] = updates[key] = os.environ[key]
    explicit_mode = "demo" if args.demo else "live" if args.live else os.environ.get("APP_MODE")
    stored_connections = saved_workspace_connections()
    stored_mode = stored_connections.get("APP_MODE")
    if explicit_mode and stored_mode and explicit_mode != stored_mode:
        raise InstallError(
            "The requested mode conflicts with the workspace's saved APP_MODE. "
            "Change the mode in System Connections first, then rerun this installer; no saved settings were changed."
        )
    selected_mode = explicit_mode or stored_mode or values.get("APP_MODE")
    if fresh and not args.demo and not args.live and "APP_MODE" not in os.environ:
        selected_mode = stored_mode or "live"
    if selected_mode not in ("live", "demo"):
        raise InstallError("APP_MODE must be live or demo.")
    values["APP_MODE"] = updates["APP_MODE"] = selected_mode
    # Backend/finance also give these saved fields priority. Reuse them only
    # for effective configuration validation; never copy them back into .env.
    values.update(stored_connections)
    values["APP_MODE"] = selected_mode
    tty = None if args.non_interactive else open_terminal()
    try:
        container, detected_networks, missing_detected_log = None, [], False
        newapi_networks = []
        if args.newapi_container or os.environ.get("YUNLAN_NEWAPI_CONTAINER") or selected_mode == "live" and (not values.get("MAIN_DB_DSN") or not values.get("NEWAPI_UPSTREAM")):
            container = select_container(args, tty)
        if container:
            try:
                running_containers = inspect_running_containers()
            except DiscoveryError as error:
                raise InstallError(str(error)) from None
            environment = container_environment(container)
            newapi_networks = container_network_names(container)
            detected_networks = list(newapi_networks)
            host_mode = container_host_network_mode(container)
            print("Detected NewAPI container: " + str(container.get("Name", "")).lstrip("/"))
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
                    print("Detected an unsupported separate NewAPI log database; provide a supported reachable log DSN.")
                    continue
                dsn, database_networks, resolution_message = resolve_database_dsn(
                    dsn, driver, running_containers, newapi_host_mode=host_mode,
                )
                if resolution_message:
                    if saved_workspace_dsn:
                        raise InstallError("A saved database endpoint is unreachable from yunlan; update that connection in System Connections. The saved configuration was preserved.")
                    print(prefix.replace("_", " ") + ": " + resolution_message)
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
                    raise InstallError("A saved database endpoint needs network-address rewriting; update that connection in System Connections. The saved configuration was preserved.")
                detected_networks.extend(database_networks)
                values[prefix + "_DRIVER"] = driver
                values[upstream_key] = dsn
                if not saved_workspace_dsn and saved.get(upstream_key) != dsn:
                    updates[prefix + "_DRIVER"] = driver
                    updates[upstream_key] = dsn
                print(("Detected the main" if prefix == "MAIN_DB" else "Detected a separate log") + " database connection; its value is not displayed.")
            detected_networks = list(dict.fromkeys(name for name in detected_networks if name and len(name) <= 200))[:16]
            if not values.get("NEWAPI_UPSTREAM"):
                port = container_http_port(container, environment)
                if host_mode:
                    origin = discover_host_gateway_origin(port)
                    if origin:
                        values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = origin
                        print("Detected a reachable NewAPI host-gateway address.")
                    else:
                        print("NewAPI uses host networking; enter its externally reachable origin because no reachable Docker host gateway was detected.")
                elif newapi_networks:
                    name = str(container.get("Name", "")).lstrip("/")
                    values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = "http://" + name + ":" + str(port)
                else:
                    print("NewAPI has no attachable Docker network; enter an origin reachable from yunlan.")
        if selected_mode == "live":
            if not values.get("MAIN_DB_DSN"):
                if args.non_interactive:
                    raise InstallError("MAIN_DB_DSN is required; provide it or select a compatible NewAPI container.")
                driver = prompt(tty, "Database driver (postgres/mysql)", values.get("MAIN_DB_DRIVER") or "postgres")
                dsn = prompt(tty, "NewAPI main database DSN", secret=True)
                values["MAIN_DB_DRIVER"] = updates["MAIN_DB_DRIVER"] = driver
                values["MAIN_DB_DSN"] = updates["MAIN_DB_DSN"] = dsn
            driver, dsn = driver_for(values["MAIN_DB_DSN"])
            if values.get("MAIN_DB_DRIVER") not in ("", driver) and not (fresh and "MAIN_DB_DRIVER" not in os.environ):
                raise InstallError("MAIN_DB_DRIVER does not match the supplied connection string.")
            if values.get("MAIN_DB_DRIVER") != driver:
                updates["MAIN_DB_DRIVER"] = driver
            if values["MAIN_DB_DSN"] != dsn:
                updates["MAIN_DB_DSN"] = dsn
            values["MAIN_DB_DRIVER"], values["MAIN_DB_DSN"] = driver, dsn
            if missing_detected_log:
                if args.non_interactive:
                    raise InstallError("A separate NewAPI log database was detected but cannot be imported automatically; provide LOG_DB_DRIVER and LOG_DB_DSN.")
                log_dsn = prompt(tty, "Separate NewAPI log database DSN reachable from yunlan", secret=True)
                log_driver, log_dsn = driver_for(log_dsn)
                values["LOG_DB_DRIVER"] = updates["LOG_DB_DRIVER"] = log_driver
                values["LOG_DB_DSN"] = updates["LOG_DB_DSN"] = log_dsn
            if not values.get("NEWAPI_UPSTREAM"):
                if args.non_interactive:
                    raise InstallError("NEWAPI_UPSTREAM is required for owner-account login.")
                values["NEWAPI_UPSTREAM"] = updates["NEWAPI_UPSTREAM"] = prompt(tty, "NewAPI site origin")
            origin = validated_origin(values["NEWAPI_UPSTREAM"])
            if fresh and not args.non_interactive:
                origin = validated_origin(prompt(tty, "NewAPI login origin", origin))
            if values["NEWAPI_UPSTREAM"] != origin or "NEWAPI_UPSTREAM" in updates:
                updates["NEWAPI_UPSTREAM"] = origin
            values["NEWAPI_UPSTREAM"] = origin
        for prefix in ("LOG_DB", "MANAGEMENT_DB"):
            if bool(values.get(prefix + "_DSN")) != bool(values.get(prefix + "_DRIVER")):
                raise InstallError(prefix + "_DSN and " + prefix + "_DRIVER must be configured together.")
            if values.get(prefix + "_DRIVER") and values[prefix + "_DRIVER"] not in ("postgres", "mysql"):
                raise InstallError(prefix + "_DRIVER must be postgres or mysql.")
        bind = values.get("BIND_ADDRESS") or "127.0.0.1"
        if fresh and not args.non_interactive:
            bind = prompt(tty, "Listen address (127.0.0.1 for reverse proxy; 0.0.0.0 for direct access)", bind)
        try:
            ipaddress.ip_address(bind)
        except ValueError:
            raise InstallError("BIND_ADDRESS must be an IP address.") from None
        updates["BIND_ADDRESS"] = bind
        port = values.get("HOST_PORT") or "18080"
        if not port.isdecimal() or not 1 <= int(port) <= 65535:
            raise InstallError("HOST_PORT must be between 1 and 65535.")
        updates["HOST_PORT"] = port
        if values.get("SITEADMIN_COOKIE_SECURE", "auto") not in ("auto", "true", "false"):
            raise InstallError("SITEADMIN_COOKIE_SECURE must be auto, true or false.")
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
            print("Attached yunlan to the selected NewAPI networks; NewAPI is unchanged.")
        elif container:
            print("No user-defined NewAPI bridge network found. Use a database/upstream address reachable from yunlan.")
        print("Saved deployment configuration to .env (credentials were not displayed).")
    finally:
        if tty is not None:
            tty.close()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (InstallError, OSError, ValueError, subprocess.TimeoutExpired) as error:
        message = str(error) if isinstance(error, InstallError) else "Cannot prepare deployment; check directory permissions and Docker availability."
        print("yunlan: " + message, file=sys.stderr)
        raise SystemExit(1)
