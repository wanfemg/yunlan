# 风控 AI 模型列表接入

核对日期：2026-11-23。

## 官方接口

本次通过官方源码核对，HTTP 源码读取返回 200：

- [router/relay-router.go](https://github.com/QuantumNous/new-api/blob/main/router/relay-router.go)：`GET /v1/models` 使用 `TokenAuth`；未携带 Anthropic、Gemini 专用认证头时采用 OpenAI 兼容模型列表。
- [controller/model.go](https://github.com/QuantumNous/new-api/blob/main/controller/model.go)：OpenAI 兼容响应为 `{"success":true,"object":"list","data":[...]}`。模型列表依据令牌所属分组、模型限制及计费配置生成；它表示密钥可见模型，不代表所有模型都支持风控使用的聊天补全接口。

## 站长后台行为

- `POST /api/risk/ai/models` 接收当前表单的 API 地址、可选密钥及超时设置，由后端发送 `GET /v1/models`。只有用户点击拉取才发送请求。
- 根地址和 `/v1` 地址规范化为同一 API 前缀；模型列表和连接验证使用同一规范化规则。
- 空密钥仅在该 API 前缀与已保存地址相同时复用已保存密钥；更换地址需要新密钥。配置保存遵守同一约定。
- 模型按 ID 去重排序；读取上限为 2 MiB、5000 项。不把上游返回的错误正文或密钥回显给浏览器，不跟随重定向。真正的空列表保持为空；异常或分页片段不伪装成完整成功列表。
- `POST /api/risk/ai/test` 可验证未保存草稿。它调用选定模型并校验结构化审核响应，不保存配置、不发起账号扫描。没有请求体时保留验证已保存配置的兼容行为。

模型列表仅证明读取成功。选定模型是否能执行结构化审核，由单独的连接验证确认。

本次仅核对官方源码，未运行测试，未向用户配置的模型服务发送实际请求。
