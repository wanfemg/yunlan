"""Configuration-bound notification receipts, without exposing destinations."""

import hashlib
import json


def configuration_fingerprint(config):
    if not str(config.get("dingtalk_webhook", "")).strip():
        return ""
    # Keyword checks and mention targets also affect acceptance/delivery.
    # Keep the exact effective values used by the sender, including unsaved
    # form overrides; never use a masked API response to build this digest.
    values = [
        str(config.get(key) or "")
        for key in ("dingtalk_webhook", "dingtalk_secret",
                    "dingtalk_keyword", "dingtalk_at_mobiles")
    ]
    encoded = json.dumps(["dingtalk-delivery-v1", *values],
                         ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def record_delivery(store, config, ok):
    fingerprint = configuration_fingerprint(config)
    if fingerprint:
        store.record_notification_delivery(fingerprint, bool(ok))


def delivery_readiness(store, config):
    state = store.notification_delivery_state(configuration_fingerprint(config))
    return {
        "delivery_verified": state["last_ok"] and state["last_success"] > 0,
        "last_success": state["last_success"],
        "last_error_at": state["last_error_at"],
    }
