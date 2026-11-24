# 圈钱中心

完整 Windows 启停与三服务 Docker 编排见
[集成运行与部署](../docs/integrated-deployment.md)。`finance/run.py` 跟随站长后台运行模式，
演示模式采用原临时 SQLite，真实模式采用原订单轮询与钉钉通知。

这里保留了原财务后台 `wanfemg/newapi-order-notifier` 1.1.0（提交 `e698bda4`）的页面和业务代码。集成到站长后台后，圈钱中心页面、API 和 CSV 导出统一验证站长后台登录会话，不再设置独立财务口令；服务间使用 `FINANCE_PROXY_SECRET` 验证内部代理请求。浏览器使用 NewAPI 站长账号密码登录，`ADMIN_TOKEN` 仅保留为程序调用凭据。

## 本地预览

在站长后台根目录打开两个 PowerShell 终端。两个终端的 `FINANCE_PROXY_SECRET` 必须相同；以下固定示例值仅用于本机预览。第一个终端启动演示数据服务，需要 Python 3.9 或更新版本。

```powershell
$env:FINANCE_PROXY_SECRET = 'siteadmin-local-preview-connector-v1'
python finance/demo.py --port 8765
```

第二个终端安装前端依赖、构建页面并启动站长后台。使用 Node.js 24、pnpm 9 和 Go 1.25 或更新版本。

```powershell
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend build
Set-Location backend
$env:APP_MODE = 'demo'
$env:LISTEN_HOST = '127.0.0.1'
$env:PORT = '18080'
$env:ADMIN_TOKEN = 'siteadmin-preview'
$env:FINANCE_UPSTREAM = 'http://127.0.0.1:8765'
$env:FINANCE_PROXY_SECRET = 'siteadmin-local-preview-connector-v1'
go run .
```

先在站长后台使用演示账号 `root`、密码 `123456` 登录，再打开圈钱中心。
在站长后台根目录运行 `Start-Demo.ps1` 可启动完整本地演示。

演示默认生成 365 天、60 位用户、约 2.7 万笔订单，账户 ID 与主后台的 101–160 一致。配置保存、测试通知、日报、周报、补发与清空记录均可操作，但只写入自动创建的临时 SQLite，不发送外部消息。退出后临时数据删除，重启恢复样本。`demo.py` 不读取 `.env`，也不采用 `MAIN_DB_DSN`、`SQL_DSN`、`SQLITE_PATH` 或已有 `notifier.db`。

## 集成数据源与钉钉通知

实际财务服务入口为 `finance/app/main.py`。集成时设置 `UI_BASE_PATH=/finance`、`UI_BIND=127.0.0.1`，让 `UI_PORT` 匹配站长后台 `FINANCE_UPSTREAM` 的端口，并为 Go/Python 进程设置相同的 `FINANCE_PROXY_SECRET`。这是服务间机器密钥，浏览器不接收该值，管理员无需输入第二套口令。

订单、排行、画像、自动订单通知和日/周报均继承站长后台的 `MAIN_DB_DRIVER` 与 `MAIN_DB_DSN`。财务进程通过仅限环回地址、机器密钥鉴权的 `/internal/finance/source` 获取配置，不再单独填写数据库连接，也不会采用财务旧配置中的连接值。日志库与管理写库不作为财务订单来源。PostgreSQL URI、关键字连接串和常见 Go MySQL DSN 均受支持。

默认内部地址为 `http://127.0.0.1:18080`；共享 `PORT` 环境变量可随站长后台端口变化。独立启动两个进程且端口不同于默认值时，将财务的 `SITEADMIN_INTERNAL_URL` 设为站长后台的环回 HTTP 地址。财务先启动不会崩溃：来源未就绪时显示提示并重试，连接恢复后自动继续。

集成设置页只保留钉钉 Webhook、加签密钥、关键词、@手机号、通知规则、日/周报与补发重试。数据库连接、轮询间隔、扫描窗口和首次补推由后台统一管理，不在财务页面重复设置；浏览器配置接口不返回连接字段、数据库凭据或路径。嵌入页面跟随站长后台主题，并隐藏重复品牌、独立退出和主题顶栏。

真实运行仍复用原钉钉发送实现，支持签名、关键词及 @手机号。自动订单通知关闭后只登记状态；手动测试、补发和报表仍可发送。失败通知不登记成功去重，后续轮询继续重试；日/周报保持独立成功标记与调度重试。通知参数、推送历史与去重信息保存在财务 `DATA_DIR/notifier.db` 中。独立运行原财务服务时仍支持原配置模式。

`/finance/api/readiness` 继承现有接口认证，实际查询源连接，只返回来源就绪、
轮询线程、报表线程和钉钉配置布尔状态。`/finance/healthz` 保留无凭据存活检查。
就绪检查不发送通知；Webhook 已保存与消息已投递分别表示。
生产入口支持 SIGTERM，停止 HTTP 服务、订单轮询与报表线程。

短验证使用本机 HTTP 模拟接收端，不发送真实钉钉消息：

```powershell
python finance/tests/test_integration.py
```
