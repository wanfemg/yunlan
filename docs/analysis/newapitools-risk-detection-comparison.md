# newapitools 风险检测逻辑对比分析

日期：2026-11-24

本文比较两个本地代码库的实际行为：

- 参考项目：`newapitools二开/new_api_tools`，源码基线 `cafb9eead3d2c1cc12ab7490f59a5dda95aea3d9`。
- 当前项目：`站长后台`。

结论只描述源码当前实现，不代表界面文案或接口名称所暗示的能力。

## 结论

newapitools 的风控主体是“调用日志画像 + 独立风险标签”。它能统计请求量、失败率、调用出口 IP、地理标签、令牌轮换、邀请关系和共享调用 IP，但这些证据大多分散在不同接口中，没有合并为一个账号风险判定，更没有登录/注册探针或设备指纹链路。

`站长后台` 的基础更完整：登录探针把登录、注册、调用 IP 分成不同来源，并记录 BrowserID；风险评估有统一评分、证据 ID、AI 结构化审核、扫描任务、封禁动作、审计和备注同步。真正用于自动封禁的规则目前只有 `token_fanout_with_burst`，其余信号仍以展示和人工复核为主。

因此，适合吸收 newapitools 的调用画像和标签设计，但不应照搬其自动封禁思路；一人多账号识别应以站长后台的探针数据为主、调用日志为辅。

## 数据源对比

| 维度 | newapitools | 站长后台 |
|---|---|---|
| 请求行为 | `logs` 中的调用记录 | `api_call` 网络事件 |
| 登录/注册 | 无专用数据源 | `login`、`registration` 事件 |
| IP 语义 | 基本都来自调用日志 IP | 调用 IP、登录 IP、注册 IP 分离 |
| 设备标识 | 无 | BrowserID 和粗粒度设备字段 |
| 历史完整性 | 未声明 | 自动封禁前检查 `NetworkHistoryComplete` |

依据：newapitools `backend/internal/service/risk_monitoring.go:199` 的用户分析主要读取调用日志；IP 和地理标签来自 `risk_ip_geo.go:267-313`。站长后台 `backend/risk_control.go:907-984` 分别累积调用 IP、登录 IP、注册 IP；`risk_control.go:1197-1204` 在历史不完整时禁用自动执行。

## 一人多账号能力

newapitools 有三个相关入口，但都不是同一个人判定：

1. **令牌轮换**：默认 24 小时、令牌数 ≥5、平均每令牌请求 ≤10。它只统计数量与均值，不验证令牌是否按时间轮换。见 `backend/internal/service/risk_monitoring.go:536-584`。
2. **关联账号**：按 `inviter_id` 聚合，同一邀请人带来多个账号。这只能说明邀请关系，不能证明同一操作者。见 `risk_monitoring.go:587-618`。
3. **同 IP 注册**：实现是调用日志 `GROUP BY user_id, ip`，没有注册事件，也没有注册时 IP；名称与实现不符。见 `risk_monitoring.go:621-650`。

三者是独立 GET 接口，未并入用户评估，也没有被 Go 版 AI 自动封禁流程调用。newapitools 的 IP 监控同样只按调用 IP 统计共享令牌、多令牌和多账号情况，缺少设备、登录认证关系和代理识别，见 `backend/internal/service/ip_monitoring.go:112`。

站长后台目前也没有完整的“一人多账号最终判定”，但证据结构已经具备：`risk_ip_monitor.go:220-392` 同时聚合 IP、用户、令牌和 BrowserID；`user_observations.go:147-159` 只在非调用来源且登录身份已验证时建立设备归属。BrowserID 是签名 Cookie 标识，不是硬件指纹，只能作为组合证据之一。

## 异常账号能力

newapitools 用户分析可输出：

- `HIGH_RPM`：窗口平均 RPM >5，不是峰值 RPM，也不比较个人历史。见 `risk_monitoring.go:321-363`。
- `HIGH_FAILURE_RATE`：该规则存在明显缺陷。`failure_rate` 计算为 0-1，但用户分析判断条件是 `>50.0`，正常情况下不会触发。见 `risk_monitoring.go:279-285`、`:365-366`。
- `CHECKIN_ANOMALY`：查询使用 `SUM(quota)`，而当前表结构使用 `quota_awarded`，规则可能静默失效。见 `risk_monitoring.go:371-389`、`:711-718`。
- 地理标签：`MANY_IPS`、`MANY_CITIES`、`GEO_JUMP`、`CROSS_BORDER`、`IP_RAPID_SWITCH`、`IP_HOPPING`。这些全部基于调用出口，不是账号本人位置。见 `risk_ip_geo.go:267-313`。

newapitools 的令牌分析另有规则，失败率阈值是正确的 `>0.5`，且在无 IP 白名单且独立 IP ≥5 时标记 `LEAK_SUSPECT`，但这套规则与用户分析不一致。见 `backend/internal/service/token_analysis.go:149-161`。

站长后台的账号异常目前更保守：

- 登录失败 ≥10：30 分。
- 未验证但声称该账号的登录尝试 ≥10：30 分，仅复核。
- 登录失败 ≥10 且出现历史中未见过的 BrowserID 成功登录：25 分。
- 30-69 为 medium，70 以上为 high；高分只建议复核，不自动封禁。

依据：`backend/risk_control.go:1005-1034`。探针对“已知浏览器”的判断依赖可用历史，缺失历史不能证明这是首次设备，代码也在 `risk_control.go:901-905` 明示了该限制。

## 调用异常能力

newapitools 擅长展示调用画像：窗口请求、失败率、额度、IP、模型、分钟趋势、令牌维度和排行榜。其 IP 快速切换处理有一些值得借鉴的细节：IPv4/IPv6 在 60 秒内变化被视为双栈变化并从“真实切换”中扣除；其他 ≤60 秒的变化算快速切换。见 `risk_monitoring.go:733-860`。

但它有两类语义风险：

- “城市键”缺市时会回退到省/国家，`unique_cities` 不一定是实际城市。
- 平均 IP 驻留时间只统计已经离开的 IP 段，最后仍在使用的出口不计入。

站长后台自动封禁规则 `token_fanout_with_burst` 更严格：

1. 当前窗口调用数 ≥配置的 `MinRequests`（默认 100）。
2. 某个自然分钟达到 `MaxRequestsPerMinute`（默认 600）。
3. 同一令牌在该分钟使用 ≥`ConcurrentTokenIPs` 个公网出口（默认 3）。
4. 上一等长窗口达到 `MinRequests`，可形成基线。
5. 当前/上一窗口调用比 ≥`MinBurstRatio`（默认 3）。

依据：`backend/risk_control.go:76-82`、`:907-1004`。

这个规则适合识别共享令牌加爆发调用，但覆盖面窄，不能代表所有调用异常。失败率、空回复率、额度突增、模型集中异常等目前更偏展示或待扩展。

## AI 审核与自动封禁

newapitools Go 版 `AIAutoBanService` 不是完整 AI 自动封禁链路：

- `ManualAssess` 返回固定 `risk_level=unknown` 和“AI 评估功能需要配置 API”。见 `ai_auto_ban.go:254-265`。
- `RunScan` 返回 `scanned=0`、`assessed=0`、`banned=0` 和占位提示。见 `ai_auto_ban.go:267-277`。
- `TestConnection` 只检查 Base URL 是否填写，不实际请求。见 `ai_auto_ban.go:279-297`。
- `FetchModels` 和 `TestModel` 是真实网络请求，但它们只是模型连接能力，不构成扫描或处置链路。见 `ai_auto_ban.go:305-573`。
- `GetSuspiciousUsers` 从调用日志中筛选请求总数 ≥10 的用户，按失败次数优先排序，并排除面板白名单；它没有登录、设备或多账号证据。见 `ai_auto_ban.go:195-251`。
- `ListBanRecords` 是固定空列表。见 `risk_monitoring.go:668-676`。

站长后台的 AI 流程是真实调用且有结构化约束：

- AI 输入是结构化特征、发现项和数据说明，移除了原始 IP 列表，只提供计数。见 `risk_control.go:1064-1086`。
- 系统提示要求 AI 只返回 JSON，并明确“API 出口不是登录位置、代理本身不是滥用、AI 不授权动作”。见 `risk_control.go:1084`。
- 返回值校验风险等级、建议、置信度、证据 ID，且证据 ID 必须来自已提供发现。见 `risk_control.go:1119-1143`。
- AI 失败或返回 unknown 时，该次评估禁用自动执行。见 `risk_control.go:1209-1218`。
- AI 仅有建议权，不能单独授权封禁；自动执行仍要求确定性规则命中。见 `risk_control.go:1332-1345`。

一个待修正的边界：AI 返回 low、observe 或 review 时，目前不会否决已经满足自动条件的确定性封禁。这符合“AI 不授权”的保守解释，但如果期望 AI 也具备拦截权，需要单独设计一致性策略。

## 封禁与审计

newapitools 的封禁记录读取是占位空列表，AI 扫描也是占位逻辑，Go 版没有可验证的自动封禁落地链路。见 `risk_monitoring.go:668-676`、`ai_auto_ban.go:267-277`。

站长后台的动作生命周期更完整：

- 自动封禁要求 `AutoExecute=true`、`DryRun=false`、用户不在白名单、当前配置版本与证据版本一致、网络历史完整。见 `risk_control.go:687-703`。
- 动作有幂等键、用户锁、前后状态、prepared/verified/uncertain/released 等状态和审计事件。见 `risk_control.go:630-806`。
- 封禁备注写入 NewAPI 用户备注，保留前后值并记录同步状态。见 `backend/risk_remark.go:14-125`。
- 路由提供评估记录、候选用户、封禁记录、审计、任务、IP 监控和排行榜。见 `risk_control.go:1452-1475`。

这套链路解决了 newapitools 占位实现的主要缺口，但规则覆盖仍不足。

## 针对三类目标的建议

### 一人多账号

不要用单一 IP 作为封禁依据。应以“账号归属可信的登录/注册事件”为主证据，组合 BrowserID、登录成功 IP、注册 IP、时间间隔、设备属性、代理特征和邀请关系；调用共享 IP 只能作为辅助信号。重点输出“账号组”而不是孤立用户列表，并给每个组一个证据矩阵：同 BrowserID、同登录出口、注册出口相近、邀请关系、共享令牌、共享调用出口。

自动封禁应只用于证据极强的组，例如同一已验证 BrowserID 多次成功登录多个账号，叠加异常注册节奏或共享令牌滥用；普通家庭出口、公司出口、校园网和移动网络重合必须降权。

### 异常账号

保留站长后台的验证/未验证登录区分。暴力尝试、撞库失败、验证失败后换新 BrowserID 成功登录等规则可以进入复核队列；高风险分不直接封禁。补齐 newapitools 的调用失败率修复版（0-1 与阈值一致）、签到字段适配和空回复率，但先把它们作为证据，不直接升级为自动处置。

### 调用异常

保留 `token_fanout_with_burst` 作为唯一自动封禁规则是合理的保守起点。后续可增加“同令牌分钟级出口扩散 + 基线倍数”的宽阈值观察档，避免只有 600 RPM 这种单一峰值才触发；同时加入失败率、空回复率、额度消耗突增、模型集中失败等复核信号。调用 IP 的地理标签应继续标注为 API 出口画像，不作为用户所在地。

## 最小改造清单

1. 把 newapitools 的令牌轮换、邀请关系、共享调用 IP 吸收为账号组证据，而不是独立列表。
2. 修复参考逻辑中的失败率阈值、签到字段和“同 IP 注册”命名，避免直接迁移缺陷。
3. 建立账号组评估对象：组 ID、成员、证据矩阵、置信度、建议处置和复核原因。
4. 一人多账号规则默认只复核；自动封禁只允许高置信度组合证据。
5. 为 AI 增加明确的一致性策略：AI 异常时阻止自动执行；AI 反对时是否阻止，必须由配置声明。
6. 在所有 IP 展示中标注来源：调用出口、登录出口、注册出口；代理仅作为属性，不作为滥用结论。
