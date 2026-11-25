# yunlan 一键安装

仓库：`https://github.com/wanfemg/yunlan`

## 安装入口

在已有 Docker Engine、Docker Compose v2、Git、curl、Python 3.8+ 的 Linux 服务器执行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

安装入口下载源码并执行仓库中的 `deploy.sh`，不是只启动一个主后台二进制。
后台／前端、圈钱中心和可选登录注册探针均有对应容器构建。
默认拉取 GHCR 已发布的 `linux/amd64`／`linux/arm64` 镜像，与源码构建分开。
当前源码需要自行构建时传入 `--build`；镜像拉取失败会明确停止，不会悄悄运行旧镜像。
交互式安装完成后进入分级管理菜单，可管理服务、连接配置及 IP 情报；`--non-interactive`
部署不会停留在菜单中。菜单停止服务时保留全部数据卷。

安装后重新打开管理菜单：

```bash
cd /opt/yunlan && bash install.sh --menu
```

菜单包含安装／更新、服务管理、连接配置、IP 情报和帮助；服务管理与 IP 情报有二级操作。
这是服务器安装管理菜单，网页内仍按功能导航。

源码构建模式会安装前端、Go、Python 运行依赖。首次启动会立即在后台初始化核心 IP 情报：
GeoLite City/ASN、VPN 与数据中心 IPv4/IPv6 网段、Tor 出口名单；文件校验后存入
`data/ip-intelligence` 持久目录。安装服务不会等待这些外部数据源下载完成，进度和失败原因
在「IP 分析 → IP 识别数据源」查看，支持手动重试。ISP 与 Anonymous-IP 的 MMDB 是可选独立
来源，需在该页面导入；公共代理和住宅代理没有单独的数据源，未命中保持未知。

root 用户默认安装在 `/opt/yunlan`，普通用户默认安装在 `$HOME/yunlan`。
可明确指定目录：

```bash
YUNLAN_INSTALL_DIR=/opt/yunlan bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

只查看参数，不开始安装：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) --help
```

## 真实数据配置

首次默认使用真实模式。脚本只读检查本机运行的 NewAPI 容器：

1. 按容器名、Compose 服务标签、镜像名顺序选择匹配的 NewAPI，支持明确指定容器。
2. 从容器环境读取 `SQL_DSN`／`LOG_SQL_DSN`，识别 PostgreSQL／MySQL 主库及独立日志库。
3. 检查本机运行容器的 Docker 网络与已发布端口；地址能唯一映射到数据库容器时，改写成可达端点并把 Yunlan 加入对应网络。
4. 生成本项目的 `.env` 和 `.yunlan.compose.yaml`，合并为单一运行文件 `.yunlan.runtime.yaml`，不修改 NewAPI 容器或配置。
5. 校验 Compose，拉取镜像并启动服务，再核对全部必要容器的健康状态。
6. 验证首页 HTML、登录入口与正式模式的主库、日志库、管理库连接，全部通过才报成功。

无需 Compose 的 `--wait`／`--wait-timeout` 参数；服务就绪由安装器独立验证。
没有交互终端时，完整连接信息采用默认监听地址与端口；必需连接缺失仍会明确报错。
部署失败会输出失败阶段、包括已退出容器的状态与隐藏凭据后的最近日志。
`initialize-data` 通过一次性任务准备数据目录，成功后自动移除，不留在常驻编排中；
`site-admin`、`finance` 和已启用的 `login-probe` 必须持续运行且健康。

自动发现适用于本机 Docker 中可明确识别的 PostgreSQL／MySQL 端点。已有 `.env` 或系统连接配置优先保留；外部数据库、无法唯一映射的容器、不可达的 loopback 地址须提供 Yunlan 容器可访问的 DSN，密码在交互提示中不回显。SQLite、ClickHouse 不在自动接入支持范围内。

安装器读取并复用 NewAPI 数据库连接，不会下载、复制或迁移 NewAPI 数据库文件。NewAPI 主库和日志库保持原位置；站长后台的 IP／风控事件表在首次运行相关功能时由后端自动创建，因此数据库账号需要对 `siteadmin_*` 表拥有建表及读写权限。日志分析使用 `LOG_DB_DSN`，未提供时沿用主库；任务日志从主库读取。

明确选择 NewAPI 容器：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) --newapi-container new-api
```

数据库／NewAPI 地址已经通过环境或本项目 `.env` 提供时，可非交互运行：

```bash
bash deploy.sh --non-interactive
```

| 参数／环境变量 | 作用 |
| --- | --- |
| `--live` | 明确采用真实数据模式 |
| `--demo` | 明确采用演示模式 |
| `--build` | 使用当前源码构建；默认拉取已发布镜像 |
| `--non-interactive` | 不提问，缺少所需配置时退出 |
| `--newapi-container NAME` | 选择本机 NewAPI 容器 |
| `YUNLAN_INSTALL_DIR` | 安装目录 |
| `YUNLAN_REF` | 安装／更新的分支，默认 `main` |
| `YUNLAN_REPOSITORY` | GitHub owner/repository，默认 `wanfemg/yunlan` |
| `MAIN_DB_DRIVER`／`MAIN_DB_DSN` | NewAPI 主库 |
| `LOG_DB_DRIVER`／`LOG_DB_DSN` | 独立调用日志库，未配置时沿用主库 |
| `NEWAPI_UPSTREAM` | NewAPI 登录与管理上游地址 |
| `BIND_ADDRESS`／`HOST_PORT` | 宿主机监听地址／端口 |
| `SITEADMIN_COOKIE_SECURE` | `auto`／`true`／`false`；正式 HTTPS 反代使用 `true` |

`NEWAPI_UPSTREAM` 可使用实际站点域名，也支持自动识别的 Docker 内网地址。
安装器验证该地址的登录设置可读取，以便验证码和二次验证沿用原实例配置。

## 访问与登录

新装默认绑定 `0.0.0.0:18080`，通过服务器 IP 和端口 `18080` 访问。
同机反向代理的目标地址为：

```text
http://127.0.0.1:18080
```

首次交互可选择直接访问、仅本机反代或自定义监听地址。已有安装保留原地址；
原地址为 `127.0.0.1` 且需要直接访问时，在本项目 `.env` 设置
`BIND_ADDRESS=0.0.0.0` 后再次运行 `bash deploy.sh`。
正式网页登录使用现有 NewAPI 站长账号密码，保留源站的验证码和二次验证。
圈钱中心共用站长登录，不另设财务口令。

`ADMIN_TOKEN`、`FINANCE_PROXY_SECRET` 和 `PROBE_SHARED_SECRET`
由安装器为缺省项生成随机值；已有值保留，不打印在终端。
它们是程序与机器服务凭据，不是站长账号密码。

只看演示：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh) --demo
```

演示账号为 `root`，密码为 `123456`。安装失败不会自动切换成演示模式。
已有安装沿用实际保存的模式。系统连接页写入数据卷的模式优先于 `.env`；
它与显式 `--live`／`--demo` 冲突时，安装器在写配置前退出。
先在系统连接页把模式调整为目标值，再重新运行部署入口，不会忽略已保存连接或意外切换数据源。

## 更新与维护

实际运行的编排文件为安装目录中的 `.yunlan.runtime.yaml`，root 默认完整路径：

```text
/opt/yunlan/.yunlan.runtime.yaml
```

宝塔「从文件导入」使用这一个文件路径，不填写逗号分隔的文件列表。
旧版部署使用两个 `-f`，Docker 将路径记为逗号串；部分宝塔版本会把整串当成一个文件，
报“文件不存在”，因而无法读取容器列表、日志及运行状态。安装器会将旧 Yunlan 容器
迁移到单文件标签，短暂重启本项目并保留 `.env`、后台配置、数据卷和外部网络。
迁移后刷新宝塔编排列表并重新打开详情。单凭旧面板的“已停止／暂无容器”不能判断 Docker 实际状态。
部分宝塔版本会将“正常退出的初始化容器 + 正在运行的服务”显示成已停止；
安装器会自动清理旧初始化容器，后续初始化任务执行完即移除。

更新时重新执行同一条安装命令。引导脚本核对仓库、分支和工作树，
仅作快进更新；有源码改动或提交分叉时退出，保留本地内容。
本地源码目录运行 `bash install.sh --update` 会先安全快进更新再部署；`bash deploy.sh` 只部署当前检出内容。

从安装目录查看日志：

```bash
docker compose --project-name yunlan -f .yunlan.runtime.yaml logs -f
```

停止当前项目：

```bash
docker compose --project-name yunlan -f .yunlan.runtime.yaml down
```

更新和停止均不删除持久数据卷。备份需要同时保存 `.env`、IP 情报目录，
以及 `yunlan_site-admin-data`、`yunlan_finance-data`、`yunlan_probe-data` 数据卷。
系统连接页保存的连接配置位于主服务数据卷内。

探针在 `LOGIN_PROBE_ENABLED=true` 时启动；登录控制台和认证入口需配置相应反代路由，
模型调用仍转发原 NewAPI。运行状态与真实来源、AI、钉钉投递是否接通分开核对，
具体见 [集成运行与部署](integrated-deployment.md)。

发布前在 Linux Docker 中启动全部服务，验证网页登录、首页和财务代理，
并用 Compose 2.16 重建容器核对兼容性。镜像发布后还会从空目录运行远程一键安装，
核对下载源码与三个已运行镜像的提交一致，重测网页登录和财务代理。
同时复现旧双文件标签，验证迁移后 Compose 列表只返回单个存在的文件路径，
使用相同文件读取容器和日志；覆盖初始化容器、旧探针、外部网络与 Compose 2.16。
这些验证不代表已经在目标服务器部署。
