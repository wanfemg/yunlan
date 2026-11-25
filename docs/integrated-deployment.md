# 集成运行与部署

站长后台、圈钱中心、登录注册探针现已提供完整服务编排。NewAPI 继续独立运行，
无需修改其源码。缺少数据库、上游或通知凭据时，服务就绪状态会保留未配置提示。

## Windows 本地运行

现有演示入口不变：

```powershell
.\Start-Demo.ps1
```

真实模式先在 `.env` 或系统连接页 `/system` 配置主数据库。系统连接页保存
`DATA_DIR/connections.json`（可由 `SITEADMIN_CONFIG_FILE` 指定），保存后的连接需重启生效；其中的值优先于环境变量。
`.env` 使用 `.env.example` 的键，启动器只解析文本，不执行其中的命令。
Windows 演示入口在项目 `data/connections.json` 保存待接入配置，但预览始终保持演示模式；
切换真实服务时先停止占用相同端口的演示进程，再使用下面的集成启动器。

```powershell
.\Start-Integrated.ps1
```

启动器会安装财务 SQL 驱动、构建 Go 后台并启动真实财务服务、Vite。
页面仍为 `http://127.0.0.1:5173/`。浏览器使用 NewAPI 站长账号密码登录；
生成的内部 API 令牌和机器密钥写入
`output/integrated-runtime/machine-secrets.json`，文件仅当前 Windows 用户可读写，
不会输出到终端。已有环境变量或 `.env` 中的密钥保持优先。
财务 SQLite 状态保存在 `DATA_DIR/finance/notifier.db`。
登录需要配置 `NEWAPI_UPSTREAM`。独立的 `NEWAPI_ADMIN_TOKEN`／ID 用于管理桥，
不是网页登录的必填凭据。HTTPS 反代部署设置 `SITEADMIN_COOKIE_SECURE=true`。
完整说明见 [站长账号登录](owner-login.md)。

已配置 `NEWAPI_UPSTREAM` 时启用探针：

```powershell
.\Start-Integrated.ps1 -EnableProbe
```

探针监听 `127.0.0.1:18081`；主后台采集地址固定为
`http://127.0.0.1:18080`。`PROBE_TRUSTED_PROXIES` 只填写实际位于探针前面的代理 CIDR。

使用构建后的前端，由 Go 提供静态页面：

```powershell
.\Start-Integrated.ps1 -WithoutFrontend
```

此时访问 `http://127.0.0.1:18080/`。`-SkipBuild` 复用已有构建，
`-SkipDependencies` 跳过财务 Python 依赖安装。
启动前发现端口被占用会明确退出，保留已有演示及其他进程。

停止本启动器创建的进程：

```powershell
.\Stop-Integrated.ps1
```

停止器核对进程 ID、启动时间、可执行文件与入口命令，避免停止已被复用的进程 ID。
日志与启动记录位于 `output/integrated-runtime/`。
`Start-Demo.ps1` 的演示进程属于另一套启动记录，集成停止器不停止演示进程。

## Docker Compose

Linux 主机使用远程安装入口：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

已经检出源码时，在本目录执行 `bash deploy.sh`。
安装器创建 `.env` 并为 `ADMIN_TOKEN`、`FINANCE_PROXY_SECRET`、
`PROBE_SHARED_SECRET` 生成独立随机值，已有值保留。新装默认真实模式，演示需传入
`--demo`；已有保存模式保持。新装默认发布地址为
`0.0.0.0:18080`，通过服务器 IP 访问；同机反代可选 `127.0.0.1`，
已有安装保留原绑定。默认拉取已发布镜像，`--build` 使用当前源码构建。
密钥只保存在权限为 `600` 的 `.env`，没有额外财务登录口令。
安装器在全部常驻容器健康、网页、登录入口及正式数据库检查通过后才报告成功。
运行编排统一为安装目录中的 `.yunlan.runtime.yaml`，宝塔从文件导入时填写此路径。
旧双文件部署会迁移旧容器标签并重启本项目，保留数据卷；
初始化任务成功后自动移除，常驻编排只保留运行服务，避免宝塔将其误判为已停止。

三项服务如下：

| 服务 | 地址 | 持久数据 |
| --- | --- | --- |
| `site-admin` | 宿主机 `0.0.0.0:18080`，已有绑定保留 | `site-admin-data` |
| `finance` | 共享网络空间内 `127.0.0.1:8765` | `finance-data` |
| `login-probe` | 宿主机 `127.0.0.1:18081`，需启用 | `probe-data` |

财务与探针共享主后台网络空间，使机器认证保持在环回地址。前端继续通过 `/money`
嵌入财务；`/finance/` 及其 API 均经主后台代理。财务继承后台主数据库，后台配置文件
只读挂载到辅助服务以同步运行模式与探针上游，不在财务页面重复输入数据库密码。

开启真实模式时填写 `.env` 的 `APP_MODE=live`、主库连接及所需 NewAPI 管理桥。
启用探针再填写 `NEWAPI_UPSTREAM` 和 `LOGIN_PROBE_ENABLED=true`，重新执行安装器。
在系统连接页保存配置后，同步重启相关进程：

```bash
docker compose -f .yunlan.runtime.yaml restart
```

启用探针前，必须已配置 NewAPI 上游。探针能启动不等于入口流量已接通：
控制台 HTML、`/_siteadmin_probe/`、注册和认证路径需由实际入口反代到探针。
模型调用路径继续转发 NewAPI，调用 IP 仍以请求日志为准。
本编排不修改现有线上反向代理或 NewAPI 部署。

新镜像统一以 UID/GID `10001` 访问数据卷。一次性 `initialize-data` 服务只为本项目
三项数据卷和明确的 `data/ip-intelligence` 目录设置所有者，不删除已有内容。
City/ASN 及代理情报从 `data/ip-intelligence` 挂载，不打包入镜像；后台需要写权限
才能完成版本更新、MMDB 导入和 active 指针切换。安装器拒绝该目录为符号链接。
保留 `.env`、三项数据卷与 IP 情报目录即可恢复配置、审计、财务去重和探针队列。

## 就绪与通知核对

`/api/health`、`/finance/healthz`、
`/_siteadmin_probe/healthz` 只检查对应 HTTP 服务存活，不代表真实业务来源已接通。

财务 `/finance/api/readiness` 沿用圈钱中心现有认证。返回：

- `source_ready`：实际执行只读 `SELECT 1` 的结果。
- `poller.running`、`reporter_running`：真实后台线程是否运行。
- `notifications.configured`、`signature_configured`：Webhook、加签密钥是否已配置。
- `notifications.external_delivery`：真实模式才允许外发。
- `notifications.delivery_verified`：当前通知配置对应的真实发送回执；新配置或最近失败不视为已验证。
- `notifications.last_success`、`last_error_at`：当前配置最近成功／失败的实际时间；就绪检查本身不发消息。

独立检查财务时，必须提供 `X-Siteadmin-Finance` 机器认证头；浏览器经主后台代理时
由后台自动添加该头。接口不返回数据库连接或通知密钥。

探针 `/_siteadmin_probe/readyz` 要求 `X-Siteadmin-Probe` 机器认证头，值为
`PROBE_SHARED_SECRET`。它只读取 NewAPI 公共状态、主后台健康状态和本地队列，
返回 `upstream_ready`、`collector_ready`、`spool_ready`、
`pending_events`、`quarantined_events`，不伪造登录事件或展示事件内容。

圈钱中心设置页保存钉钉 Webhook、签名密钥、关键词和通知开关后，再使用原有
“测试通知”核验实际投递。首次订单轮询默认只登记历史状态，不自动补推历史消息。
失败投递按现有去重机制重试，日报周报按配置调度，演示模式不发送外部消息。
