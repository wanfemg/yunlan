# yunlan · 云岚站长后台

独立运行的 NewAPI 站长工作区，集成用户管理、账号密码登录、风控、IP 分析、模型日志、
任务日志、签到分析、令牌、自动分组、兑换码和圈钱中心，不修改 NewAPI 源码。

## Linux 一键安装

安装环境：Docker Engine、Docker Compose 插件、Git、Bash、curl 和 Python 3.8+。
业务数据库支持 PostgreSQL、MySQL。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

脚本克隆或更新 `wanfemg/yunlan`，在完整项目目录内运行部署入口；
识别可用的 NewAPI 容器、数据库和 Docker 网络，缺少参数时交互填写。
已有 `.env`、数据卷和机器密钥保留，源码有本地改动时停止覆盖。
更新同样使用上面的一条命令。

安装完成后再次打开分级管理菜单：

```bash
cd /opt/yunlan && bash install.sh --menu
```

首次启动会在后台下载核心 GeoIP、VPN／数据中心与 Tor 数据，安装命令不等待下载完成；ISP 与匿名 IP 数据库需要单独导入。

新装默认按真实数据部署。网页登录使用现有 **NewAPI 站长账号和密码**，圈钱中心共用登录。
新装默认发布宿主机 `0.0.0.0:18080`，通过服务器 IP 和端口访问；
同机反向代理专用部署在向导中选择 `127.0.0.1`。已有安装保留原监听配置。
默认拉取已发布的多架构镜像，使用 `--build` 才在服务器构建源码。
安装会验证常驻容器、网页、登录入口和正式模式的数据库连接，失败时显示阶段、
全部容器状态与隐藏凭据后的日志，不会打印部署成功。

指定安装目录：

```bash
YUNLAN_INSTALL_DIR=/opt/yunlan bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

仅启动演示数据：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) --demo
```

非交互参数、独立日志库、网络连接、更新和备份见
[一键安装说明](docs/one-click-install.md)。

## 完整功能接入

真实数据连接与机器服务检查入口：导航栏 **系统连接**（`/system`）。
本轮接通内容、IP 维护说明见 [功能落地与连接说明](docs/delivery-integration.md)，
Windows 集成启动、Docker 三服务和探针入口配置见
[集成运行与部署](docs/integrated-deployment.md)。

## 本地全量测试数据

双击 `启动测试环境.cmd`，或在项目目录运行 `.\Start-Demo.ps1`。
脚本编译并启动本地 Go、财务临时 SQLite 和前端，入口为
`http://127.0.0.1:5173/`，演示登录账号为 `root`，密码为 `123456`。
圈钱中心沿用后台入口，不再设置独立登录口令。
正式模式使用已配置 NewAPI 的站长账号密码，并保留其二次验证；
登录配置和会话说明见 [站长账号登录](docs/owner-login.md)。

样本覆盖首页、用户管理、调用／登录／注册 IP、风控、日志分析、
任务日志、令牌管理、自动分组、兑换码和财务各页面。页面的筛选、
分页、详情、导出及已经实现的管理操作使用正常接口，操作仅影响本地
演示状态。财务推送和报告操作只在临时库产生本地记录。
重启脚本恢复初始数据；兑换码样例仅用于检查生成与复制、导出流程。
此入口不会加载真实 NewAPI 连接或财务 `.env`。

`pnpm dev:demo` 需要先启动对应 Go 和财务服务；单独执行
`pnpm dev:ui` 仍是暂停业务取数的界面预览。

## 从已检出的源码部署

从项目根目录运行部署入口：

```bash
bash deploy.sh
```

演示模式使用 `bash deploy.sh --demo`。脚本建立或保留 `.env`，生成缺少的内部机器密钥，
拉取并启动后台与财务容器；修改源码后使用 `bash deploy.sh --build`。
服务器本机访问地址：

```text
http://127.0.0.1:18080
```

脚本保留已有配置。修改本项目 `.env` 后再次运行 `bash deploy.sh`。
生成的 `.yunlan.compose.yaml` 保存已探测的外部网络，安装器将它与基础配置合并为
`.yunlan.runtime.yaml`；实际启动、管理菜单与宝塔只使用这一个运行文件。
已有双文件部署会自动迁移容器标签并短暂重启本项目，配置与数据卷保留。
宝塔「从文件导入」填写安装目录内 `.yunlan.runtime.yaml` 的完整路径。
查看日志和停止服务时使用相同 Compose 项目名 `yunlan`，不要加 `down -v` 删除数据卷。

The standard Compose setup starts the site admin and integrated finance service. The login/registration probe is an optional Compose profile. See the [integrated deployment guide](docs/integrated-deployment.md).

The navigation has two separate log pages. 日志分析 aggregates per-model success, failures, and suspected empty responses from the NewAPI log database (or the main database when no separate log database is configured). 任务日志 reads asynchronous generation records from the main database's `tasks` table. For live mode, give the corresponding database accounts `SELECT` access to `logs` and `tasks`.

## Connect a live instance

Configure `.env` on the host before starting the service:

```dotenv
APP_MODE=live
ADMIN_TOKEN=long-random-secret
MAIN_DB_DRIVER=postgres
MAIN_DB_DSN=postgres://siteadmin:password@db-host:5432/newapi?sslmode=require
NEWAPI_UPSTREAM=https://newapi.example.com
TIMEZONE=Asia/Shanghai
QUOTA_PER_UNIT=500000
```

The main database account is the site-admin data connection and supports `SELECT`, `INSERT`, `UPDATE`, and `DELETE` on NewAPI data managed by this application. The current auto-grouping and token-control actions update user groups and token status. Token-status changes may remain cached in NewAPI until its Redis token-cache entry expires. Writes use `MANAGEMENT_DB_DRIVER` and `MANAGEMENT_DB_DSN` when configured; otherwise they use the main database connection. Log reads can use a separate read-only account through `LOG_DB_DRIVER` and `LOG_DB_DSN`.

Supported drivers are `postgres` and `mysql`. PostgreSQL accepts a URL DSN; MySQL accepts the Go MySQL driver's DSN format, for example `siteadmin:password@tcp(db-host:3306)/newapi?parseTime=true`. The backend requires `ADMIN_TOKEN`, `MAIN_DB_DRIVER`, and `MAIN_DB_DSN` in live mode and rejects incomplete configuration at startup.

Keep `.env` private and out of source control. The provided `.gitignore` excludes it. Compose passes these values to the container at runtime; the Docker image does not bake in database credentials or the admin token. Browser access uses the NewAPI owner username/password and an HttpOnly session cookie. Passwords and upstream access tokens are never stored in browser storage. `ADMIN_TOKEN` remains available for server-side schedulers and explicit programmatic API calls.

The service remains bound to loopback by default. When placing a reverse proxy on the same host, configure `BIND_ADDRESS` to the host interface reachable by that proxy and restrict access at the host firewall. Do not publish the database port to the Internet.

## Redemption code generation

`POST /api/redemptions/generate` uses the existing administrator Bearer token and accepts the same generator parameters as newapitools:

```json
{
  "name": "充值活动",
  "count": 10,
  "key_prefix": "promo-",
  "quota_mode": "fixed",
  "fixed_amount": 1,
  "expire_mode": "days",
  "expire_days": 30
}
```

Each batch contains 1–1000 codes. Names contain 1–20 characters; an optional UTF-8 prefix can contain up to 20 bytes without whitespace or control characters. Codes preserve the complete prefix and contain exactly 32 characters, with a cryptographically random suffix. `quota_mode` defaults to `fixed`; `random` takes `min_amount` and `max_amount` instead of `fixed_amount`. Amounts retain their original JSON decimal value, are converted using `QUOTA_PER_UNIT`, and are rounded to the nearest integer quota with halves rounded up. Both rounded endpoints must be positive and at most NewAPI's wallet limit of `9007199254740991`; random quota is sampled uniformly between them, inclusive.

`expire_mode` defaults to `never` (`expired_time=0`). `days` takes a positive `expire_days`; `date` takes a future `expire_date` in RFC3339 format, local `YYYY-MM-DDTHH:mm[:ss]`, or `YYYY-MM-DD`. Dates without an offset use `TIMEZONE`; a date without a time expires at the start of that day. Stored timestamps use Unix seconds.

Successful responses contain `success`, `message`, and `data` with `keys`, `count`, matching `quotas` in the same order, `expired_time`, `mode`, and `persisted`. In live mode the entire batch is inserted into NewAPI's `redemptions` table in one transaction, with enabled status `1` and unused fields set to `0`. The creator ID remains `1`, matching newapitools. Writes use the optional management connection or the main connection as described above; a failed batch does not return codes as a success. Demo mode only generates a preview, returns `persisted=false`, and does not write to any database. Preview codes cannot be redeemed. The generator UI is available at `/redemptions` and supports copying and TXT export.

## Development

The frontend lockfile uses pnpm lockfile format 9. The container pins pnpm 9.15.9 and uses frozen-lockfile installation. The frontend development server listens on `127.0.0.1:5173` and proxies `/api` and `/finance` to `127.0.0.1:18080`. The backend module targets Go 1.25. The production image builds the frontend and backend separately, serves `frontend/dist` through the Go service at port `18080`, and runs as a non-root user.

## User, IP and risk modules

The three modules have separate implementation files: `backend/user_management.go`,
`backend/ip_analysis.go`, and `backend/risk_control.go`. Their independent frontend
pages are `UserManagement.tsx`, `IPAnalysis.tsx`, and `RiskControl.tsx`, available at
`/users`, `/ip-analysis`, and `/risk`. All use the existing administrator session.

## Responsive UI preview

The desktop layout uses a shared top navigation, statistics, filters and detail
views based on the reference tool's workflow. Phones use a modal navigation drawer,
stacked forms, user/history cards and bounded scrolling dialogs. Inputs use 16px
text and at least 44px touch height on narrow screens. Charts and feature pages
load separately from the application shell.

For the current interface-only stage, run `pnpm dev:ui` in `frontend/`.
This loads `.env.ui-preview`, sets `VITE_UI_PREVIEW_ONLY=true`, and prevents
business reads, mutations, polling and credential validation. Statistics display
unavailable values rather than fabricated zeroes. Navigation, local filter
drafts, tabs and form-layout switches remain available. The backend is not needed
for this preview. `pnpm build:ui` generates the same interface-only static build.

Normal `pnpm dev` and `pnpm build` retain the complete backend-connected behavior
when the preview-only flag is unset. Data access should remain paused until that
stage is requested. The finance page does not create an iframe during the paused
preview and does not add another login screen.

Map boundary assets in `frontend/public/network-world.json` and
`network-china.json` originate from the local reference project's `public/world.json`
and `public/china.json`. Geographic statistics are produced by the independent
network module; the assets do not supply account or request data.

Responsive checks cover the three new modules at 320, 390, 768 and 1440 CSS pixels,
and the other navigation pages at 320 and 1280. Screenshots are saved under
`output/ui-preview/`. Backend tests and both frontend build modes are checked
without connecting to a live NewAPI instance.

## Module endpoints

| Module | Endpoints |
| --- | --- |
| Users | `GET /api/users`, `/api/users/:id`, `/api/users/activity-stats`, `/api/users/export` |
| User actions | `POST /api/users/:id/ban`, `/api/users/:id/unban` |
| Network statistics | `GET /api/ip/stats`, `/api/ip/distribution`, `/api/ip/addresses`, `/api/ip/shared` |
| Network relationships | `GET /api/ip/multi-ip-users`, `/api/ip/multi-ip-tokens`, `/api/ip/users/:id/ips` |
| IP investigation | `GET /api/ip/details/:ip` with per-source counts and paginated users, tokens, events or verified browsers |
| IP enrichment | `GET /api/ip/lookup/:ip`, `POST /api/ip/attributes` |
| Usage import | `POST /api/ip/sync` |
| Risk configuration | `GET/PUT /api/risk/config` |
| Risk review | `GET /api/risk/users/:id/analysis`, `POST /api/risk/assess` |
| Risk jobs and history | `POST /api/risk/scan`, `GET /api/risk/jobs`, `/api/risk/assessments`, `/api/risk/ban-records`, `/api/risk/audit` |
| Risk actions | `POST /api/risk/actions`, `/api/risk/actions/:id/release`, `/api/risk/actions/:id/reconcile` |
| AI connection and schema check | `POST /api/risk/ai/test` |

IP queries require one `source`: `api_call` (default), `login`, or `registration`.
Supported `period` values are `1h`, `3h`, `6h`, `12h`, `24h`, `3d`, `7d`, `30d`,
and `90d`; the default is `7d`. Network relationships are paginated using `page`
and `page_size`. Aggregates process all retained events, including more than
3000 distinct IPs; pagination affects displayed relationships only.

Usage events come only from NewAPI consumption/error logs (`type=2/5`), with
task settlement entries excluded. Source row IDs provide import idempotency;
separate actual requests sharing a request ID are not collapsed. Imports persist
events and a monotonic cursor in one transaction. Old log schemas without
`request_id` remain usable. SQL errors are reported as unavailable data.

Live mode creates `siteadmin_network_events`, `siteadmin_network_meta`,
`siteadmin_ip_enrichment`, `siteadmin_risk_state`, and `siteadmin_risk_leases`
in the management database. Grant `CREATE`/index privileges for initialization
and normal read/write privileges for operation. When no management connection
is supplied, this uses the main connection. NewAPI application tables are not
altered. Demo state is in memory and clearly reports demo mode.

Optional `GEOIP_CITY_PATH`, `GEOIP_ASN_PATH`, `GEOIP_ISP_PATH`, and
`GEOIP_ANONYMOUS_PATH` point to existing local MMDB files. Missing databases or
records produce unknown values and coverage warnings. Hosting is a network
attribute, not proof of proxy usage or abuse. There are no automatic downloads.
Updated database files take effect on service restart. Country, region, city,
ASN, ISP, and proxy attributes remain separate. Unknown, invalid, missing,
private, loopback, link-local and reserved addresses are not classified abroad.
Geographic users are distinct within a region; global users are the union,
not a sum of regional counts.

To install real offline City/ASN, known VPN/non-residential networks and Tor
exit data on Windows, run `.\Install-IPIntelligence.ps1`, then restart the
backend. `-Proxy http://127.0.0.1:7897` is supported when a local download proxy
is required. Downloads use versioned directories under `data/ip-intelligence`
and activate only after validation. `IP_INTELLIGENCE_DIR` overrides the data
root; otherwise the backend uses `DATA_DIR/ip-intelligence`. The installed
generation's City/ASN files are discovered automatically. Compose mounts
`./data/ip-intelligence` read-only at `/app/ip-intelligence`.

Lookup remains local. Known VPN and Tor hits include their source and matching
prefix; absent membership remains unknown. Non-residential lists combine VPN
and datacenter ranges and are not treated as a VPN verdict. There is no
scheduled updater; rerun the installer and restart to refresh the generation.
With real data installed, demo account/call records remain samples but IP
attribute fixtures are disabled. See [provider selection and deployment](docs/analysis/ip-provider-selection.md).

The IP investigation dialog is shared by IP analysis, risk IP monitoring,
user login/device observations and token analysis. Related names open user
or token dialogs and preserve the originating IP query when returning.
The IP page keeps its source, period and filters in the URL. Token egress
counts and egress rankings use the normalized retained call events, while
other token usage metrics retain their existing NewAPI log basis.
See [IP analysis functionality and integration](docs/analysis/ip-analysis.md)
for the feature inventory, query windows, evidence rules and remaining limits.

User activity is based on API requests, including failed identified requests.
`activity` accepts `daily`, `weekly`, `monthly`, `silent`, `dormant`, `never`,
`unknown`, `active_7d`, or `active_30d`. The exclusive buckets are up to 24h,
24h–7d, 7d–30d, 30d–90d and over 90d. `never` requires verified lifetime call
coverage and no upstream request history; otherwise missing history is unknown.
`NETWORK_HISTORY_COMPLETE=false` is the default. Login activity and unverified
attempts against a username are separate fields. CSV export uses the same
filters, classification and statistics timestamp as the list.

User actions require a JSON `reason` and an `Idempotency-Key` header. Configure
`NEWAPI_UPSTREAM`, `NEWAPI_ADMIN_TOKEN`, and `NEWAPI_ADMIN_USER_ID` to use
NewAPI's own management lifecycle. Actions verify resulting status and, when
available, the authentication version. Administrator accounts are excluded.
No independent management password is introduced. Releases restore only a
ban owned by this risk module, reject conflicting later changes, and do not
re-enable unrelated tokens. Uncertain actions retain their snapshots; reconcile
reads current state and records evidence without blindly replaying a mutation.

Risk scans persist jobs, assessments, actions and audit events. They return
`202 Accepted`; background work resumes from a durable cursor. Dry runs perform
real evaluation and store evidence while skipping mutations. Automatic actions
default off, dry-run defaults on, and incomplete historical coverage suppresses
automatic actions. Initial rules combine a configured request burst with token
exit fanout and an increase over the preceding window; a first observed peak
only requests review. Login or registration exits differing from API exits,
shared IPs, and proxy labels do not independently trigger bans.
AI is explicitly configured, called only by assessment/test/scan operations,
receives aggregate evidence, and cannot authorize an action on its own.
`GET /api/risk/users/:id/analysis` does not call AI.

The independent [login probe](login-probe/README.md) supplies only login and
registration events. It does not replace the usage-log IP pipeline. Its
`PROBE_SHARED_SECRET` is a machine transport key, not a human login password.

Maintainer validation commands (the full suite was not run for this publication):
backend `go test ./...`, `go vet ./...`, and `go build .`; repeat
these in `login-probe/`. The optional `NETWORK_TEST_MMDB_DIR` enables tests
against external MaxMind test fixtures. Real-instance SQL permissions, schema
compatibility, NewAPI administrative credentials, and the site's proxy/CSP
settings require instance-specific integration checks.

## License

MIT License，见 [LICENSE](LICENSE)。
参考项目及集成来源见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
