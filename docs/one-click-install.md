# yunlan 一键安装

仓库：`https://github.com/wanfemg/yunlan`

## 安装入口

在已有 Docker Engine、Docker Compose v2、Git、curl、Python 3 的 Linux 服务器执行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/wanfemg/yunlan/main/install.sh)
```

安装入口下载源码并执行仓库中的 `deploy.sh`，不是只启动一个主后台二进制。
后台／前端、圈钱中心和可选登录注册探针均有对应容器构建。
首次安装从源码构建镜像；不依赖预先发布的 GHCR 镜像。

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

1. 只有一个匹配容器时自动选择；多个时由终端选择。
2. 从该容器读取 PostgreSQL／MySQL 的 `SQL_DSN` 和独立 `LOG_SQL_DSN`。
3. 将 yunlan 主服务加入对应的用户自定义 Docker 网络。
4. 生成本项目的 `.env` 和 `.yunlan.compose.yaml`，不修改 NewAPI 容器或配置。
5. 校验 Compose，构建镜像并启动服务。

发现 SQLite、ClickHouse 等当前不支持的数据库时不会冒充已接通。
host 网络、默认 bridge 网络或回环数据库地址不能直接套用到独立容器；
无法自动导入时交互填写从 yunlan 容器可达的连接，密码输入不回显。

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

`NEWAPI_UPSTREAM` 可使用实际站点域名。首次交互会显示识别结果供确认，
以便已有验证码、HTTPS 和登录配置继续按原实例工作。

## 访问与登录

默认绑定 `127.0.0.1:18080`，同机反向代理的目标地址为：

```text
http://127.0.0.1:18080
```

直接通过公网端口访问时，在首次交互中选择 `0.0.0.0`，或在本项目 `.env` 设置
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

更新时重新执行同一条安装命令。引导脚本核对仓库、分支和工作树，
仅作快进更新；有源码改动或提交分叉时退出，保留本地内容。
本地源码目录运行 `bash install.sh` 或 `bash deploy.sh` 只部署当前检出内容。

从安装目录查看日志：

```bash
docker compose --project-name yunlan --profile probe logs -f
```

停止当前项目：

```bash
docker compose --project-name yunlan --profile probe down
```

更新和停止均不删除持久数据卷。备份需要同时保存 `.env`、IP 情报目录，
以及 `yunlan_site-admin-data`、`yunlan_finance-data`、`yunlan_probe-data` 数据卷。
系统连接页保存的连接配置位于主服务数据卷内。

探针在 `LOGIN_PROBE_ENABLED=true` 时启动；登录控制台和认证入口需配置相应反代路由，
模型调用仍转发原 NewAPI。运行状态与真实来源、AI、钉钉投递是否接通分开核对，
具体见 [集成运行与部署](integrated-deployment.md)。

发布时只进行构建、脚本语法及安装配置检查，不代表已经在实际生产服务器部署。
