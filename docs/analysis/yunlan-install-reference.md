# yunlan 一键安装入口参考

调研日期：2026-11-25。以下结论来自 `james-6-23/new_api_tools` 的公开 `main` 源码；该分支会更新，未在真实服务器执行其脚本。

## 上游采用的安装链

README 提供远程 Bash 入口：下载 `install.sh` 后执行。安装脚本检查 Git、Docker、Compose，识别 NewAPI 目录及已有工具目录；新安装克隆项目，随后在完整检出目录运行 `deploy.sh`。[1][2]

已有安装默认进入更新流程：保留 `.env`，更新源码、拉取镜像，再重建容器。其普通克隆更新分支还包含 `git reset --hard origin/main`；`yunlan` 应保留配置与数据，并在工作树有改动时停止覆盖，不能直接复制该强制重置行为。[2]

数据库地址与 Docker 网络需要配套处理。上游按 NewAPI 网络选择 bridge 或 host overlay，日志分库可以另接日志数据库网络；host 模式及宿主机回环数据库还有专用 overlay。源码里的 `NEWAPI_NETWORK`、`HOST_DB_PROXY_PORT` 等是上游部署协议，不应直接当作本项目已有能力。[2][3]

## 本项目发布前的差异

检查时，本项目 `install.sh` 根据 `BASH_SOURCE[0]` 定位仓库，并直接读取旁边的 `.env.example`、`compose.yaml` 和 Docker 构建上下文。它只能从完整检出目录执行；直接将其作为远程下载入口运行，无法获得这些配套文件。

`yunlan` 应拆分为两层：

1. 远程 `install.sh`：选择安装目录、克隆或更新 `wanfemg/yunlan`，再调用检出目录中的部署入口。
2. 本地 `deploy.sh`：保留 `.env`，补齐尚未配置的随机机器密钥，验证 Compose 并构建、启动服务。

现有架构包含 Go/React 主后台、Python 财务和可选登录探针。财务与探针共享主后台网络空间，内部连接继续使用环回地址和机器认证；NewAPI 保持独立运行，网页登录复用其站长账号。源代码构建应包含三个服务及全部依赖文件，不能只发布主 Go 二进制。[4]

安装目录更新不得删除数据卷或替换已有连接参数。入口需要明确处理更新失败、工作树改动、安装路径冲突及缺少 Docker/Compose；远程脚本本身不能宣称数据库、登录探针或钉钉已实际接通。

## 验证范围

本次只阅读源代码和本地发布前脚本，未克隆执行上游安装器，未启动服务器容器，未修改任何 NewAPI 配置，也未创建 GitHub 仓库。最终安装地址须在 `yunlan` 仓库发布后检查可访问性。

## 来源

1. [上游 README 的快速部署与日志分库说明](https://github.com/james-6-23/new_api_tools/blob/main/README.md)
2. [上游 install.sh：依赖检查、克隆、配置检测、更新和 deploy 调用](https://github.com/james-6-23/new_api_tools/blob/main/install.sh)
3. [上游 deploy.sh：数据库连接与 Docker 网络配置](https://github.com/james-6-23/new_api_tools/blob/main/deploy.sh)
4. 本地发布前检查：项目根目录 `install.sh`、`compose.yaml`、`Dockerfile`、`finance/Dockerfile`、`login-probe/Dockerfile`。
