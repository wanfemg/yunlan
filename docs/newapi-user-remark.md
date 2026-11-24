# NewAPI 管理员用户备注接入

核对日期：2026-11-23。

本页记录官方字段及接口约定，供站长后台的封禁、解封与备注编辑接入使用。本文不修改 NewAPI 源码。

## 官方源码基线

当前官方仓库为 [QuantumNous/new-api](https://github.com/QuantumNous/new-api)。GitHub 仓库 API 返回的 `full_name` 为 `QuantumNous/new-api`，默认分支为 `main`。

本次通过 `git ls-remote` 核对的 `main` 提交为：

```text
51a1ceeaec14ede2130ade924dcc46a6316a49a5
```

下表固定到该提交，不依赖后续 `main` 分支变更。

| 源码 | 核对内容 |
| --- | --- |
| [model/user.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/model/user.go) | 用户字段、管理员查询、普通用户资料查询、用户编辑 |
| [controller/user.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/controller/user.go) | 管理员更新校验、用户资料响应、管理审计 |
| [router/api-router.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/router/api-router.go) | 管理员用户更新路由 |
| [model/user_cache.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/model/user_cache.go) | 用户认证缓存字段 |
| [users-mutate-drawer.tsx](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/web/src/features/users/components/users-mutate-drawer.tsx) | 管理员用户编辑中的备注输入 |

固定原始源码链接示例：

```text
https://raw.githubusercontent.com/QuantumNous/new-api/51a1ceeaec14ede2130ade924dcc46a6316a49a5/model/user.go
```

`Calcium-Ion/new-api` 是历史仓库地址；本次请求该地址的 GitHub 仓库 API，同样返回 `full_name: QuantumNous/new-api` 和 `html_url: https://github.com/QuantumNous/new-api`。固定源码引用使用 `QuantumNous/new-api`，不依赖旧地址重定向。

## 管理员用户备注字段

- 数据库字段为 `users.remark`，Go 字段为 `User.Remark`，JSON 字段为 `remark`。
- 官方模型声明 `varchar(255)` 并校验 `max=255`。站长后台使用相同的 **255 个 Unicode 字符**上限，不能沿用风控处置备注原有的 500 字符上限，也不能静默截断。
- 官方管理员用户编辑界面使用备注文本框，并标注仅管理员可见。
- 管理员用户列表及用户详情从数据库读取该字段。普通用户的 self、登录及刷新响应使用专用资料 DTO，排除管理员备注。
- 风控处置记录中的备注是操作历史；NewAPI 的 `users.remark` 是管理员后台当前用户备注。两者需要分别保留，不能用本地处置记录冒充已同步的管理员用户备注。

## 通用用户编辑接口的风险

官方路由提供 `PUT /api/user/`，由 `controller.UpdateUser` 处理。本次核对未发现独立的用户备注 PATCH 路由。

该接口要求非零用户 ID 和非空用户名，并执行账号角色权限校验。下游 `EditWithTx` 会同时更新用户名、昵称、分组和备注；分组变化还会影响认证版本。

因此，仅提交 `id` 和 `remark` 不符合该接口约定。为满足校验而补传过期用户名、昵称或分组，同样存在覆盖管理员并发修改的风险。备注同步不能调用该接口时漏传其余编辑字段，也不能以空字段补齐请求。

## 单字段 CAS 接入约定

站长后台已有数据库管理写入能力，用户备注采用数据库单字段更新，并使用 compare-and-swap（CAS）检查原值：

1. 读取目标用户当前真实备注及账号状态，不从历史处置记录推断当前备注。
2. 校验新备注长度。封禁追加说明前先与原备注合成，再对合成后的完整内容校验 255 字符上限。
3. 更新条件同时包含用户 ID 和读取到的原备注；更新列仅包含 `remark`，保留原用户名、昵称、分组、余额、权限及认证版本。
4. 兼容数据库中的 `NULL` 和空字符串，明确两者的比较语义。MySQL 使用字节精确比较，避免默认排序规则忽略大小写或尾空格；SQLite 使用二进制比较；PostgreSQL 使用精确文本比较。
5. CAS 未匹配时反馈备注已被其他操作修改，重新读取后再编辑；不能覆盖更新，也不能宣称同步成功。
6. 保存后读取确认实际字段值，并将同步结果写入站长后台操作审计。数据库错误与并发冲突必须返回前端。

备注编辑器加载 NewAPI 当前备注，并携带原值参与并发校验。封禁时默认保留原备注并追加处置说明；解封不能自动清空已有备注。用户明确执行清空时，按同一并发校验流程保存空值，并保留操作审计。

封禁、解封和备注同步可能分别成功或失败。接口及界面需要报告实际结果，不得将“账号状态已改变、备注写入失败”展示为全部成功。

## 缓存与审计边界

官方 `UserBase` 认证缓存不包含 `Remark`。管理员列表与详情直接查询数据库，因此仅更新 `users.remark` 不需要变更 `auth_version`，也不需要清理或刷新认证缓存。

数据库单字段更新不会经过官方 `UpdateUser` 的 `user.update` 审计逻辑。站长后台继续保存自己的操作人、目标用户、旧备注、新备注、处置关联及同步结果；不能宣称数据库更新已生成 NewAPI 官方管理审计。

本次核对未执行功能测试。
