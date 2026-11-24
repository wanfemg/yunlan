# 日志分析的模型状态口径

核对日期：2026-11-23。功能目标是按模型聚合调用状态，主表不重复展示 NewAPI 原始日志。

## 与 newapitools 保持一致的公式

参考本地 `newapitools二开/new_api_tools/backend/internal/service/log_analytics.go` 的 `GetModelStatistics`：

| 指标 | 统计范围 |
| --- | --- |
| 总请求 | 当前时间与渠道范围内 `type IN (2, 5)` 的日志记录数 |
| 成功数 | `type = 2` 的消费记录数，包含下述疑似空回复 |
| 失败数 | `type = 5` 的错误记录数 |
| 空回复数 | `type = 2 AND completion_tokens = 0` 的记录数 |
| 成功率 | 成功数 ÷ 总请求 × 100% |
| 空回复率 | 空回复数 ÷ 成功数 × 100% |

空回复是成功记录的子集，不是成功、失败之外的第三种互斥结果；趋势图不能把三者堆叠后称为总请求。无分母时比例显示“—”。模型列表分页只影响展示，汇总指标按完整筛选范围聚合。

兼容旧版数据库时，`completion_tokens` 字段缺失、NULL 或无效值属于未知，不能用 `COALESCE(..., 0)` 伪装为空回复。部分成功记录缺少有效输出 Token 数据时，应同时报告已知空计数与未知计数，并把完整范围的空回复率标为不可判定。

## “空回复”的含义和局限

本页沿用参考界面的“空回复数”列名，但它实际表示**成功记录的输出 Token 为零这一信号**，不能证明响应正文为空：

- Gemini 图像生成明确将 `CompletionTokens` 设为零，仍能返回图片。
- MiniMax TTS 和阿里 rerank 也存在输出 Token 为零的正常处理路径。
- 计费逻辑在缺少 usage 时可回退为估算输入 Token 与零输出 Token。
- 流式接口可能使用上游 usage，也可能根据已收集的输出估算；部分兼容网关把 usage 放在倒数第二个事件，官方已有回退处理。
- 缓存 Token 是独立计费字段。思考内容、工具调用与可见文本也不等价；仅凭输出 Token 的正负都无法确认用户看到了什么内容。

因此，状态颜色仅表示日志指标是否越过参考阈值，不能据此直接判定模型真实可用性，更不能直接封禁用户或渠道。

## 记录数与用户请求数

NewAPI 的同一请求可能经过多次渠道重试。官方重试循环会记录失败渠道的错误日志，随后成功路径仍可记录消费日志，并使用同一上下文 `request_id`。本页按日志记录计数，包含这些尝试；成功率不是对用户请求去重后的最终成功率。

记录只覆盖 NewAPI 实际保存的日志。消费日志和错误日志均有保存开关，未记录的事件无法由模型聚合补回。本页不读取或展示原始请求正文、令牌密钥或任意日志 metadata。

## 官方源码基线

本次 `git ls-remote` 确认官方 HEAD 为 `51a1ceeaec14ede2130ade924dcc46a6316a49a5`；以下固定提交的原始源码均成功读取，HTTP 200：

- [model/log.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/model/log.go)：类型常量 2／5、消费与错误记录、输出 Token 和 request_id 字段、消费日志开关。
- [controller/relay.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/controller/relay.go)：渠道重试循环及逐次错误处理。
- [service/relay_error.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/service/relay_error.go)：错误记录条件及错误日志开关。
- [relay/channel/gemini/relay-gemini.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/relay/channel/gemini/relay-gemini.go)：图像生成使用零输出 Token。
- [relay/channel/minimax/tts.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/relay/channel/minimax/tts.go)：TTS 的 Token 处理。
- [relay/channel/ali/rerank.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/relay/channel/ali/rerank.go)：rerank 的 Token 处理。
- [relay/channel/openai/relay-openai.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/relay/channel/openai/relay-openai.go)：流式 usage 回退与输出估算。
- [service/text_quota.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/service/text_quota.go)：usage 缺失回退及缓存字段分开统计。
- [service/billing_usage.go](https://github.com/QuantumNous/new-api/blob/51a1ceeaec14ede2130ade924dcc46a6316a49a5/service/billing_usage.go)：日志计费使用规范化后的 usage。

本次只读核对源码，未运行测试，未请求用户配置的模型服务。
