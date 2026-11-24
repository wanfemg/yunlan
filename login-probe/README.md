# NewAPI 登录注册探针

独立 Go 模块，源代码不写入 NewAPI。网关观察注册与认证接口，在控制台 HTML
响应中插入同源浏览器脚本。API 调用 IP 继续从 NewAPI 日志导入。

## 配置

| 环境变量 | 作用 |
| --- | --- |
| `NEWAPI_UPSTREAM` | NewAPI HTTP(S) 源站，例如 `http://127.0.0.1:3000` |
| `SITEADMIN_ORIGIN` | 站长后台源站，例如 `http://127.0.0.1:18080` |
| `PROBE_SHARED_SECRET` | 与站长后台相同的 32–128 位 URL 安全机器密钥 |
| `PROBE_LISTEN` | 默认 `127.0.0.1:18081` |
| `PROBE_DATA_DIR` | 事件待发送目录，默认 `./data/login-probe` |
| `PROBE_TRUSTED_PROXIES` | 实际入口代理的 CIDR，逗号分隔，默认不信任任何转发头 |
| `SITEADMIN_CONFIG_FILE` | 可选站长后台 `connections.json`，只继承其中的 `NEWAPI_UPSTREAM`，重启生效 |

配置后在此目录执行 `go run .`。本次开发没有修改任何在线反向代理或 NewAPI 配置。
后续接入时，控制台与认证路径经此网关；模型调用路径保留原转发。
需要同时正确保留主机名、HTTPS 协议及 NewAPI 自身的可信代理设置。

完整 Docker 编排与 Windows 启停见
[集成运行与部署](../docs/integrated-deployment.md)。Compose 使用 `probe` profile，
Windows 使用 `Start-Integrated.ps1 -EnableProbe`；上游未配置时不会假装探针已接入。
同机 Nginx 路由模板见 [nginx-locations.conf.example](nginx-locations.conf.example)，
控制台和认证入口转发到 `18081`，模型路径保留 `3000` 原入口。入口代理位于宿主机时，
探针需信任实际可见的代理来源地址；容器网络下通常不是 `127.0.0.1`，先核对来源再配置 CIDR。

## 运行状态

`GET /_siteadmin_probe/healthz` 是独立于 NewAPI 上游的存活检查。
`GET /_siteadmin_probe/readyz` 要求 `X-Siteadmin-Probe` 等于共享机器密钥，
检查上游公共状态、采集端存活和本地队列，只返回就绪布尔值、待发送及隔离事件数量。
接口不展示地址凭据和登录事件内容，不生成登录事件。入口代理仍需实际转发控制台和
认证路径到网关，服务存活不等同于登录流量已接通。

## 已实现的采集

- 标准密码注册、密码登录、二次验证、登录验证、Passkey 完成接口。
- 返回受支持 JSON 登录结果的 OAuth 回调。无法核验最终身份的结果不计作登录成功。
- 登录、注册出口 IP，网络对端、IP 来源可信状态、时间、认证方式。
- 服务端签名的第一方浏览器 ID；UA、平台、语言、时区、屏幕等辅助特征。
- 登录成功结果中的账号 ID；失败尝试仅保留尝试用户名。
- 注册未返回账号 ID 时，由采集端在事件新鲜且创建时间可核验时做明确标注的数据库关联。
  其他情况保留注册时点的用户名，不冒充已验证的永久账号 ID。

只处理明确的认证路由，不将 `/api/user/self`、令牌刷新、普通 API 或
Passkey challenge begin 当成新登录。HTTP 200 和 `success=true` 不足以证明认证完成；
二次验证挑战与未知成功结构记为 `pending`。

浏览器数据仅辅助登录与注册关联，不参与 API 调用归因。清理 Cookie、无痕窗口、
浏览器保护等均可能改变或缺失标识；它不是硬件唯一身份。关闭页面不代表退出登录。
OAuth 自动注册的创建事件只有在源站明确提供证据时才能独立识别，本模块不会把
一次 OAuth 登录擅自标记为注册。
OAuth 共用回调也可能用于绑定账号；无法核验用途的成功或失败回调不计入登录事件，
避免将绑定操作、刷新页面或无效回调伪造成登录失败。

## 数据与失败处理

认证结果只抽取白名单字段，不保存密码、验证码、访问令牌、刷新令牌或完整 Cookie。
会话关联标识采用带密钥摘要。浏览器不能上报用户 ID、登录结果或来源 IP。

事件先原子写入本地待发送目录，再使用时间戳和 HMAC-SHA256 上报到
`/internal/probe/events`。收集端按来源与事件 ID 幂等，确认后删除本地待发送副本。
网络及服务故障保留重试；不可重试的事件校验错误移入 `quarantine/`，避免阻塞后续事件。
机器密钥或收集地址配置错误仍需修正配置，待发送文件保持保留。

严格 CSP 会被保留：允许同源脚本或有匹配 nonce 时才插入；不满足时返回
`X-Siteadmin-Probe-Device: blocked-by-csp`，认证事件仍可采集。
HTML 支持 gzip 解码并更新缓存校验头，普通 API、SSE、WebSocket 不做响应体解析。
浏览器数据采集失败不影响认证，待发送目录写入失败会明确记录错误。
网关自身属于网络入口，需要实际接入时单独验证可用性。

## 验证

`go test ./...`、`go vet ./...`、`go build .`；浏览器脚本用 `node --check probe.js`。
测试覆盖认证中间态、旧版结果兼容、凭据排除、可信代理边界、HTTPS 终止、
CSP/压缩、签名重试、坏事件隔离和 SSE 首块透传。
