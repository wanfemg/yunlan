"""Start the integrated finance service using the same saved mode as site-admin."""

import json
import os
from pathlib import Path
import runpy
import sys


def startup_mode():
    mode = os.environ.get("APP_MODE", "demo").strip().lower()
    settings = os.environ.get("SITEADMIN_CONFIG_FILE", "").strip()
    if settings:
        try:
            path = Path(settings)
            with path.open("r", encoding="utf-8-sig") as stream:
                document = stream.read(49153)
            if len(document.encode("utf-8")) > 49152:
                raise ValueError
            values = json.loads(document)["values"]
            if not isinstance(values, dict):
                raise ValueError
            if "APP_MODE" in values:
                mode = str(values["APP_MODE"]).strip().lower()
        except FileNotFoundError:
            pass
        except (OSError, ValueError, KeyError, TypeError):
            raise SystemExit("无法读取站长后台的运行模式；未启动财务服务") from None
    if mode not in ("demo", "live"):
        raise SystemExit("APP_MODE 必须是 demo 或 live")
    return mode


def main():
    directory = Path(__file__).resolve().parent
    if not os.environ.get("FINANCE_PROXY_SECRET", "").strip():
        raise SystemExit("集成入口需要 FINANCE_PROXY_SECRET；不创建独立管理口令")
    if startup_mode() == "demo":
        sys.argv = [str(directory / "demo.py"), "--port", os.environ.get("UI_PORT", "8765")]
        runpy.run_path(str(directory / "demo.py"), run_name="__main__")
    else:
        sys.argv = [str(directory / "app" / "main.py")]
        runpy.run_path(str(directory / "app" / "main.py"), run_name="__main__")


if __name__ == "__main__":
    main()
