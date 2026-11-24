# IP 识别选型与本地部署

日期：2026-11-24。部署位置：站长后台本地开发服务，不包含远程生产服务器。

## 选型结论

本轮采用 **GeoLite2 City／ASN + X4BNet VPN／非住宅出口名单 + Tor 官方出口名单**。
查询全部在 Go 后端本地执行，不逐条向第三方发送用户 IP，不依赖新注册的 API 账号或付费密钥。
现有 IP 分析、用户详情、风控 IP 监控、令牌来源详情共用同一个解析模块。

| 方案 | 适合用途 | 本轮处理 |
| --- | --- | --- |
| GeoLite2 City／ASN | 地域与自治系统归属，适配现有 MMDB 读取模块 | 已安装，自动发现 City／ASN 文件 |
| X4BNet/lists_vpn | 已知 VPN 网段，以及 VPN 与机房等非住宅出口的混合集合 | 已安装 IPv4、IPv6 四份名单 |
| Tor Project 出口表 | 当前官方导出的 Tor 出口地址 | 已安装 |
| howismyip | 多数据源聚合与结果对比 | 作为思路参考，未部署；核心结果依赖外部服务 |
| IP2Proxy Go | 将官方 BIN 数据库接到 Go 后端 | 保留后续选项；SDK 开源不等于高级分类数据免费 |
| ipapi.is | 在线归属、VPN 等判断 | 未接入；完整检测字段依赖密钥与配额，不能照旧 README 假定免密钥可用 |
| MyIP | 交互完整的网络诊断工具箱 | 作为界面与诊断流程参考，不另起一套管理后台 |
| ip2region | 离线地域补充 | 本轮已有 MMDB，未增加重复地域引擎；不承担 VPN／代理识别 |

## newapitools 实际使用情况

核对了本地 `newapitools二开/new_api_tools/backend/internal/service/ip_geo.go`，
以及官方仓库当前 `main` 分支的同名文件：

- 使用 `github.com/oschwald/geoip2-golang` 读取 `GeoLite2-City.mmdb`。
- 数据从 `adysec/IP_database` 及其镜像下载，更新间隔 24 小时。
- 当前该模块实际查询国家、省州、城市。结构虽然预留 ISP／Org／ASN 字段，
  但 `QuerySingle` 没有用 ASN／ISP 数据库填充这些字段。
- `deploy.sh` 有 City 和 ASN 两份库的下载逻辑；文件下载并不代表查询模块已经读取 ASN。
- 该模块没有完整的 VPN、住宅代理或公共代理识别数据。

来源：

- https://github.com/james-6-23/new_api_tools
- https://raw.githubusercontent.com/james-6-23/new_api_tools/main/backend/internal/service/ip_geo.go
- https://raw.githubusercontent.com/james-6-23/new_api_tools/main/deploy.sh

## 2026-11-24 实际加载结果

本地版本：`20261124T112632Z`。下表记录当前服务实际读取的条数，不是供应商覆盖率。

| 数据 | 已加载条数 |
| --- | ---: |
| 已知 VPN IPv4 网段 | 11,195 |
| 已知 VPN IPv6 网段 | 714 |
| 非住宅出口 IPv4 网段 | 45,985 |
| 非住宅出口 IPv6 网段 | 8,673 |
| Tor 出口地址 | 1,253 |

City 与 ASN 数据库均已打开。GeoLite2 City／ASN 实际版本使用 MMDB 的 BuildEpoch，
在 IP 详情中可以查看，不能用文件下载时间当作数据库生成时间。
独立 ISP 库、MaxMind Anonymous IP 库未配置。

活动版本保存在 `data/ip-intelligence/active.json`，具体数据放在
`data/ip-intelligence/versions/<版本>/`。保留来源说明、上游 README、文件大小、
SHA-256 和获取时间。数据不加入源码仓库。

本地已确认：从真实下载名单取出的 VPN 网段可匹配到 VPN 与非住宅证据，
Tor 出口可匹配 Tor 证据。`1.1.1.1` 的 ASN 来自真实 ASN 库，
其没有的地域字段仍保持未知；没有继续使用“演示杭州”等人工样例画像。
账号和调用事件仍是演示数据，与真实 IP 属性查询分别处理。

## 判断规则

1. 只对公网 IP 查询网络名单。IPv4 映射 IPv6 先规范化。
2. VPN 命中专用 VPN 集合才写 `vpn=yes`；Tor 命中官方表才写 `tor=yes`。
3. `datacenter` 集合包含 VPN 与机房网段，统一写 `network_type=non_residential`，
   不把它直接写成 `hosting`、`VPN` 或住宅代理。
4. 正例名单未命中保持 `unknown`，不写 `no`。住宅网络归属也不等于住宅代理。
5. 明确的 MMDB 判断与管理员补录保留优先级；每项结果保留来源与匹配前缀。
6. 本轮没有改变自动封禁阈值。网络属性用于关联与复核，不单独触发封禁。

## 安装与更新

Windows 在项目根目录运行：

```powershell
.\Install-IPIntelligence.ps1
.\Start-Demo.ps1 -BackendOnly
```

下载需通过已有代理时，脚本支持显式参数：

```powershell
.\Install-IPIntelligence.ps1 -Proxy http://127.0.0.1:7897
```

脚本仅下载和解析数据，不运行上游代码。先写入新版本目录，全部成功后切换
`active.json`；失败时保留原活动版本。服务在启动时读取活动版本，数据更新后重启生效。
本轮没有建立定时任务；名单不会自动过期，需要按需运行更新。

Docker Compose 已添加只读挂载 `./data/ip-intelligence:/app/ip-intelligence`，
并设置 `IP_INTELLIGENCE_DIR`。迁移时带上整个数据目录与活动版本文件；
后端会自动发现版本目录中的 City／ASN 数据库。单独的远程 Docker 部署本轮未执行。

## 限制与来源

名单识别不保证完整覆盖 VPN，也不识别全部动态住宅代理、移动代理和个人自建出口。
公共代理、住宅代理两个字段当前没有独立识别数据，因此保持未知。
ISP 未安装单独库时不拿 ASN 组织名冒充运营商。

- X4BNet 官方说明区分 VPN 与 datacenter 混合集合：
  https://github.com/X4BNet/lists_vpn
- X4BNet 生成名单的 MIT 许可全文已保存在下载目录 README；
  上游 IPLocate ASN 数据的 CC BY-SA 4.0 来源信息同时保留。
- Tor 官方出口导出：
  https://check.torproject.org/api/bulk
- MaxMind GeoLite2 数据经 P3TERX 镜像下载：
  https://github.com/P3TERX/GeoLite.mmdb
- 数据库内容许可与读取代码许可分别处理，上游来源和条款见下载目录 `DATA-SOURCES.txt`。
- IP2Proxy LITE 仅公共代理与商业分类的边界：
  https://lite.ip2location.com/ip2proxy-lite
- ipapi.is 当前免密钥与带密钥能力：
  https://ipapi.is/developers.html

完成了 Go、TypeScript 和前端构建，以及本地加载与页面显示确认。
按当前开发约定未运行完整业务测试，未连接真实 NewAPI 用户日志。
