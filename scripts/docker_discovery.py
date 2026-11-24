"""Resolve database endpoints from Docker metadata without exposing secrets."""
from __future__ import annotations

from dataclasses import dataclass
import json
import re
import socket
import subprocess
from urllib.parse import urlsplit, urlunsplit


class DiscoveryError(Exception):
    pass


@dataclass(frozen=True)
class DatabaseEndpoint:
    driver: str
    host: str
    port: int


LOOPBACKS = {"localhost", "127.0.0.1", "::1"}
BUILTIN_NETWORKS = {"bridge", "host", "none"}
DEFAULT_PORTS = {"postgres": 5432, "mysql": 3306}


def _authority(host: str, port: int) -> str:
    rendered = f"[{host}]" if ":" in host and not host.startswith("[") else host
    return f"{rendered}:{port}"


def _pg_conninfo_fields(dsn: str) -> list[dict[str, object]] | None:
    """Tokenize libpq keyword DSNs while preserving original source spans."""
    fields: list[dict[str, object]] = []
    index, length = 0, len(dsn)
    while index < length:
        while index < length and dsn[index].isspace():
            index += 1
        if index == length:
            break
        key_start = index
        while index < length and not dsn[index].isspace() and dsn[index] != "=":
            index += 1
        key = dsn[key_start:index].lower()
        while index < length and dsn[index].isspace():
            index += 1
        if not key or index >= length or dsn[index] != "=":
            return None
        index += 1
        while index < length and dsn[index].isspace():
            index += 1
        value_start = index
        decoded: list[str] = []
        if index < length and dsn[index] == "'":
            index += 1
            closed = False
            while index < length:
                char = dsn[index]
                if char == "\\":
                    index += 1
                    if index >= length:
                        return None
                    decoded.append(dsn[index])
                    index += 1
                elif char == "'":
                    index += 1
                    closed = True
                    break
                else:
                    decoded.append(char)
                    index += 1
            if not closed or (index < length and not dsn[index].isspace()):
                return None
            value_end = index
        else:
            while index < length and not dsn[index].isspace():
                if dsn[index] == "\\":
                    index += 1
                    if index >= length:
                        return None
                decoded.append(dsn[index])
                index += 1
            value_end = index
        fields.append({
            "key": key,
            "value": "".join(decoded),
            "start": value_start,
            "end": value_end,
        })
    return fields


def _validate_port(port: int) -> int | None:
    return port if 1 <= port <= 65535 else None


def parse_database_endpoint(dsn: str, driver: str) -> DatabaseEndpoint | None:
    """Return the host/port portion of a supported NewAPI DSN."""
    if driver not in DEFAULT_PORTS:
        return None
    if driver == "postgres" and dsn.startswith(("postgres://", "postgresql://")):
        try:
            parsed = urlsplit(dsn)
            if not parsed.hostname:
                return None
            port = parsed.port or DEFAULT_PORTS[driver]
        except ValueError:
            return None
        valid_port = _validate_port(port)
        return DatabaseEndpoint(driver, parsed.hostname, valid_port) if valid_port else None
    if driver == "mysql" and dsn.startswith("mysql://"):
        try:
            parsed = urlsplit(dsn)
            if not parsed.hostname:
                return None
            port = parsed.port or DEFAULT_PORTS[driver]
        except ValueError:
            return None
        valid_port = _validate_port(port)
        return DatabaseEndpoint(driver, parsed.hostname, valid_port) if valid_port else None
    if driver == "mysql":
        matches = list(re.finditer(r"@tcp\((\[[^\]]+\]|[^):]+)(?::(\d+))?\)", dsn))
        if len(matches) != 1:
            return None
        match = matches[0]
        port = _validate_port(int(match.group(2) or 3306))
        return DatabaseEndpoint(driver, match.group(1).strip("[]"), port) if port else None
    fields = _pg_conninfo_fields(dsn)
    if fields is None:
        return None
    selected = {str(field["key"]): str(field["value"]) for field in fields
                if field["key"] in {"host", "hostaddr", "port"}}
    if sum(field["key"] == "host" for field in fields) != 1 or sum(field["key"] == "port" for field in fields) > 1:
        return None
    # A host list and hostaddr override require a user-supplied, explicit route.
    host = selected.get("host")
    if not host or "hostaddr" in selected or "," in host:
        return None
    try:
        port = int(selected.get("port", DEFAULT_PORTS[driver]))
    except ValueError:
        return None
    valid_port = _validate_port(port)
    return DatabaseEndpoint(driver, host, valid_port) if valid_port else None


def replace_database_endpoint(dsn: str, endpoint: DatabaseEndpoint, host: str, port: int) -> str:
    """Replace only the endpoint, retaining credentials and DSN parameters."""
    if not 1 <= port <= 65535:
        raise DiscoveryError("Cannot safely rewrite an invalid database port.")
    if endpoint.driver == "postgres" and dsn.startswith(("postgres://", "postgresql://")):
        parsed = urlsplit(dsn)
        userinfo, separator, _old = parsed.netloc.rpartition("@")
        netloc = (userinfo + separator if separator else "") + _authority(host, port)
        return urlunsplit((parsed.scheme, netloc, parsed.path, parsed.query, parsed.fragment))
    if endpoint.driver == "mysql" and dsn.startswith("mysql://"):
        parsed = urlsplit(dsn)
        userinfo, separator, _old = parsed.netloc.rpartition("@")
        netloc = (userinfo + separator if separator else "") + _authority(host, port)
        return urlunsplit((parsed.scheme, netloc, parsed.path, parsed.query, parsed.fragment))
    if endpoint.driver == "mysql":
        pattern = r"(@tcp\()([^)]*)(\))"
        if len(list(re.finditer(pattern, dsn))) != 1:
            raise DiscoveryError("Cannot safely rewrite the MySQL TCP endpoint.")
        return re.sub(pattern, lambda m: m.group(1) + _authority(host, port) + m.group(3), dsn, count=1)
    fields = _pg_conninfo_fields(dsn)
    if fields is None:
        raise DiscoveryError("Cannot safely rewrite the PostgreSQL host parameter.")
    hosts = [field for field in fields if field["key"] == "host"]
    ports = [field for field in fields if field["key"] == "port"]
    if len(hosts) != 1 or len(ports) > 1 or any(field["key"] == "hostaddr" for field in fields):
        raise DiscoveryError("Cannot safely rewrite ambiguous PostgreSQL host parameters.")
    if not 1 <= port <= 65535:
        raise DiscoveryError("Cannot safely rewrite an invalid database port.")
    host_field = hosts[0]
    edits = [(int(host_field["start"]), int(host_field["end"]), host)]
    if ports:
        port_field = ports[0]
        edits.append((int(port_field["start"]), int(port_field["end"]), str(port)))
    else:
        return dsn.rstrip() + " port=" + str(port)
    rewritten = dsn
    for start, end, value in sorted(edits, reverse=True):
        rewritten = rewritten[:start] + value + rewritten[end:]
    return rewritten


def _container_networks(container: dict) -> dict[str, dict]:
    networks = container.get("NetworkSettings", {}).get("Networks", {})
    return networks if isinstance(networks, dict) else {}


def _container_ports(container: dict) -> set[int]:
    keys = set(container.get("Config", {}).get("ExposedPorts", {}) or {})
    keys.update(container.get("NetworkSettings", {}).get("Ports", {}) or {})
    return {int(match.group(1)) for key in keys if (match := re.match(r"(\d+)/tcp$", key))}


def _matching_networks(container: dict, endpoint: DatabaseEndpoint) -> list[str]:
    host = endpoint.host.strip("[]").lower()
    exposed = _container_ports(container)
    if exposed and endpoint.port not in exposed:
        return []
    container_name = str(container.get("Name", "")).lstrip("/").lower()
    matches = []
    for name, details in _usable_networks(container):
        aliases = {str(alias).lower() for alias in details.get("Aliases", []) if alias}
        address = str(details.get("IPAddress", "")).lower()
        if host in aliases or host == address or host == container_name:
            matches.append(name)
    return matches


def _usable_networks(container: dict) -> list[tuple[str, dict]]:
    result = []
    for name, details in _container_networks(container).items():
        address = str(details.get("IPAddress", "")).strip()
        if name not in BUILTIN_NETWORKS and address:
            result.append((name, details))
    return result


def _published_port_matches(container: dict, host_port: int) -> list[tuple[str, dict, int]]:
    matches = []
    ports = container.get("NetworkSettings", {}).get("Ports", {}) or {}
    for port_spec, bindings in ports.items():
        match = re.fullmatch(r"(\d+)/tcp", str(port_spec))
        if not match:
            continue
        internal_port = int(match.group(1))
        for binding in bindings or []:
            try:
                host_ip = str(binding.get("HostIp", "")).lower()
                if host_ip in {"", "0.0.0.0", "::", "127.0.0.1", "::1"} and int(binding.get("HostPort", "0")) == host_port:
                    matches.extend((name, details, internal_port) for name, details in _usable_networks(container))
            except (TypeError, ValueError):
                continue
    return matches


def _unique_network_alias(container: dict, network: str, containers: list[dict]) -> str | None:
    endpoint = _container_networks(container).get(network, {})
    candidates = {str(container.get("Name", "")).lstrip("/").lower()}
    candidates.update(str(alias).lower() for alias in endpoint.get("Aliases", []) if alias)
    candidates.discard("")
    for candidate in sorted(candidates):
        owners = []
        for item in containers:
            target = _container_networks(item).get(network)
            if target is None:
                continue
            item_name = str(item.get("Name", "")).lstrip("/").lower()
            aliases = {str(alias).lower() for alias in target.get("Aliases", []) if alias}
            if candidate == item_name or candidate in aliases:
                owners.append(item)
        if len(owners) == 1:
            return candidate
    return None


def container_network_names(container: dict) -> list[str]:
    """Return networks yunlan can attach to; host/none are not attachable."""
    return [name for name, details in _container_networks(container).items()
            if name not in BUILTIN_NETWORKS and details.get("IPAddress")]


def container_host_network_mode(container: dict) -> bool:
    mode = str(container.get("HostConfig", {}).get("NetworkMode", "")).lower()
    return mode == "host" or "host" in _container_networks(container)


def container_environment(container: dict) -> dict[str, str]:
    values = {}
    for entry in container.get("Config", {}).get("Env", []) or []:
        if isinstance(entry, str) and "=" in entry:
            key, value = entry.split("=", 1)
            values[key] = value
    return values


def container_http_port(container: dict, environment: dict[str, str]) -> int:
    """Use the internal HTTP port; host mode can use a known published port."""
    configured = environment.get("PORT", "3000")
    try:
        internal = int(configured)
        if not 1 <= internal <= 65535:
            internal = 3000
    except (TypeError, ValueError):
        internal = 3000
    if container_host_network_mode(container):
        ports = container.get("NetworkSettings", {}).get("Ports", {}) or {}
        bindings = ports.get(f"{internal}/tcp") or []
        published = {int(item["HostPort"]) for item in bindings
                     if isinstance(item, dict) and str(item.get("HostPort", "")).isdecimal()
                     and 1 <= int(item["HostPort"]) <= 65535}
        if len(published) == 1:
            return next(iter(published))
    return internal


def resolve_database_dsn(
    dsn: str,
    driver: str,
    containers: list[dict],
    *,
    newapi_host_mode: bool,
) -> tuple[str, list[str], str | None]:
    """Return usable DSN, required networks, and a non-secret status message."""
    endpoint = parse_database_endpoint(dsn, driver)
    if endpoint is None:
        return dsn, [], "The supported database DSN has no unambiguous TCP host; enter an endpoint reachable from yunlan."
    host = endpoint.host.strip("[]").lower()
    if host in LOOPBACKS:
        if not newapi_host_mode:
            return dsn, [], "The NewAPI database endpoint is loopback; enter an address reachable from yunlan."
        matches = []
        for container in containers:
            matches.extend((container, network, details, internal_port)
                           for network, details, internal_port in _published_port_matches(container, endpoint.port))
        # One database attached to multiple networks is unambiguous. More than
        # one matching container or internal TCP port must use manual input.
        unique = {(str(item[0].get("Id") or item[0].get("Name", "")), item[3]) for item in matches}
        if len(unique) != 1:
            return dsn, [], "No unambiguous database container matches the published loopback port; enter a reachable DSN manually."
        container, network, details, internal_port = sorted(matches, key=lambda item: (item[1] == "bridge", item[1]))[0]
        address = _unique_network_alias(container, network, containers)
        address = address or str(details.get("IPAddress", "")).strip()
        if not address:
            return dsn, [], "The matched database container has no usable Docker network address; enter a reachable DSN manually."
        try:
            return replace_database_endpoint(dsn, endpoint, address, internal_port), [network], None
        except DiscoveryError:
            return dsn, [], "The database DSN cannot be rewritten without changing its parameters; enter a reachable DSN manually."

    matches = [(container, _matching_networks(container, endpoint)) for container in containers]
    matches = [(container, networks) for container, networks in matches if networks]
    if len(matches) > 1:
        return dsn, [], "The database hostname matches multiple Docker containers; enter an unambiguous DSN manually."
    if not matches:
        # Do not treat an endpoint that resolves to a local container on a
        # built-in/non-attachable network as reachable from Compose's private
        # project bridge. A reachable host-published endpoint must be entered.
        host = endpoint.host.strip("[]").lower()
        for container in containers:
            container_name = str(container.get("Name", "")).lstrip("/").lower()
            networks = _container_networks(container)
            aliases = {
                str(alias).lower()
                for details in networks.values()
                for alias in details.get("Aliases", [])
                if alias
            }
            if host == container_name or host in aliases:
                if any(name in BUILTIN_NETWORKS for name in networks):
                    return dsn, [], "The database is on a non-attachable Docker network; enter a host-published DSN reachable from yunlan."
        return dsn, [], None
    _container, networks = matches[0]
    # The endpoint name/IP was confirmed on these exact user-defined networks;
    # never attach unrelated networks from the same container.
    return dsn, networks, None


def discover_host_gateway_origin(port: int) -> str | None:
    """Use a literal gateway only when Docker's host gateway accepts TCP."""
    try:
        result = subprocess.run(
            ["docker", "network", "inspect", "bridge"], stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace",
            timeout=20, check=False,
        )
        if result.returncode:
            return None
        networks = json.loads(result.stdout)
        gateway = networks[0].get("IPAM", {}).get("Config", [{}])[0].get("Gateway")
        if not gateway:
            return None
        with socket.create_connection((gateway, port), timeout=1.5):
            return "http://" + _authority(gateway, port)
    except (OSError, ValueError, TypeError, IndexError, KeyError, subprocess.TimeoutExpired):
        return None


def inspect_running_containers() -> list[dict]:
    """Inspect all running containers; captured environment values stay private."""
    listed = subprocess.run(
        ["docker", "ps", "-q"], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=25, check=False,
    )
    if listed.returncode:
        raise DiscoveryError("Cannot inspect running Docker containers.")
    identifiers = listed.stdout.split()
    if not identifiers:
        return []
    inspected = subprocess.run(
        ["docker", "inspect", *identifiers], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", timeout=25, check=False,
    )
    if inspected.returncode:
        raise DiscoveryError("Docker container inspection failed.")
    try:
        result = json.loads(inspected.stdout)
    except ValueError:
        raise DiscoveryError("Docker returned invalid container inspection data.") from None
    if not isinstance(result, list) or any(not isinstance(item, dict) for item in result):
        raise DiscoveryError("Docker returned an invalid container inspection result.")
    return [item for item in result if item.get("State", {}).get("Running")]
