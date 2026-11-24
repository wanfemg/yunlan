"""入口：加载配置 → 启动轮询线程 → 启动配置界面。"""

import logging
import os
import signal
import sys
import threading

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import config as config_module
import store as store_module
import webui
from integration import InheritedSource
from poller import Poller
from report import DailyReporter


def main():
    logging.basicConfig(
        level=logging.INFO,
        format="[%(asctime)s] %(levelname)s %(name)s: %(message)s",
    )
    log = logging.getLogger("main")
    proxy_secret = webui.proxy_secret_from_env(config_module.UI_BIND, config_module.UI_BASE_PATH)

    data_dir = config_module.DATA_DIR
    os.makedirs(data_dir, exist_ok=True)
    store = store_module.Store(os.path.join(data_dir, "notifier.db"))
    if proxy_secret:
        try:
            store.inherited_source = InheritedSource(proxy_secret)
        except ValueError as exc:
            raise SystemExit(str(exc)) from exc

    credential = None if proxy_secret else webui.ensure_admin(store)
    if proxy_secret:
        log.info("配置界面使用站长后台认证")
    if credential:
        password, generated = credential
        if generated:
            log.warning("已生成管理界面口令：%s （登录后请及时更换：设置 UI_PASSWORD 环境变量）", password)
        else:
            log.info("管理界面口令来自 UI_PASSWORD 环境变量")

    poller = Poller(store)
    try:
        poller.run_once()
    except Exception as exc:
        log.warning("首次轮询未完成：%s", exc)

    reporter = DailyReporter(store)

    server = webui.build_server(
        store, poller, reporter, config_module.UI_BIND, config_module.UI_PORT,
        base_path=config_module.UI_BASE_PATH, secure_cookie=config_module.UI_SECURE_COOKIE,
        proxy_secret=proxy_secret,
    )
    server.poller_thread = threading.Thread(target=poller.loop, name="poller", daemon=True)
    server.reporter_thread = threading.Thread(target=reporter.loop, name="daily-reporter", daemon=True)
    server.poller_thread.start()
    server.reporter_thread.start()

    def stop(_signum=None, _frame=None):
        poller.stop()
        reporter.stop()
        # HTTPServer.shutdown must run outside the serve_forever thread.
        threading.Thread(target=server.shutdown, name="http-shutdown", daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    log.info("订单通知服务已启动，配置界面 http://%s:%s%s/",
             config_module.UI_BIND, config_module.UI_PORT, config_module.UI_BASE_PATH)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("收到退出信号，正在停止")
    finally:
        poller.stop()
        reporter.stop()
        server.server_close()
        server.poller_thread.join(timeout=5)
        server.reporter_thread.join(timeout=5)


if __name__ == "__main__":
    main()
