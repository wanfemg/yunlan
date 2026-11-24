# 第三方项目说明

`yunlan` 是独立的 NewAPI 站长后台，保留以下项目的参考与集成来源：

- **new_api_tools**：`https://github.com/james-6-23/new_api_tools`。
  自动分组、兑换码生成以及部分 UI／交互参考和适配自该项目。
  上游 README 将项目声明为 MIT License；发布检查时未提供单独的 LICENSE 文件。
  上游作者保留其原有贡献的权利。本项目的 MIT 授权不改变这些贡献原有的权利归属。
- **newapi-order-notifier**：`https://github.com/wanfemg/newapi-order-notifier`。
  圈钱中心基于该财务后台集成，相关来源说明保留在 `finance/README.md`。
- **NewAPI**：`https://github.com/QuantumNous/new-api`。
  作为独立的上游服务运行。本项目通过其管理接口和数据库连接集成，
  不包含完整 NewAPI 源码或部署。

运行依赖包括 Go、Gin、pgx、go-sql-driver/mysql、maxminddb-golang、React、
Radix UI、Tailwind CSS、ECharts、Lucide 及 Python 数据库驱动。
依赖的具体版本分别记录在 `backend/go.mod`、`frontend/package.json`、
`frontend/pnpm-lock.yaml` 和 `finance/requirements.txt`；
各依赖仍遵循其自身许可证。

IP 数据库和名单不随源码仓库发布。下载后的版本目录保留来源、校验摘要及上游说明，
其授权和归属遵循相应数据供应者；详情见 `docs/analysis/ip-provider-selection.md`。
