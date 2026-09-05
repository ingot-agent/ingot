# app.backend

`app.backend` 是 ingot 浏览器 UI 使用的轻量 HTTP/SSE 应用边界。它是一个包含两个组件的复合插件：

- `host` 持有进程内的 `EventHub`、支持作用域的 `interaction.Channel` 和 `observation.Observer`。该组件没有依赖，因此 Agent 可以使用这些能力而不会在组件图中形成环。
- `app` 持有 HTTP 服务器、Controller、运行中的 Turn，以及保留的 Operation 结果。它依赖 `agent.History`、`session.Store`、`session.Manager` 和 `session.Query`。相互独立且可选的 `agent.Runtime` 与 `agent.StreamingRuntime` 至少需要提供一个。`asset.Store` 是可选依赖，Operation 通过 `[]operation.Operation` 收集。

当前实现使用 `ingot-abi v0.1.0` 和正式发布的 `sdk v0.2.7`。插件不修改 SDK，也不额外覆盖工作区中的 SDK 选择。

## 配置

配置示例：

```toml
[plugins."app.backend".backend]
address = "127.0.0.1:7316"
replay_capacity = 1024
subscriber_buffer = 64
heartbeat_interval_seconds = 15
operation_retention = 128
max_asset_bytes = 67108864
```

## HTTP 接口

M6 后端提供以下接口：

| 功能 | HTTP 接口 |
| --- | --- |
| 状态引导与事件 | `GET /api/state`、`GET /api/events` |
| Turn | `POST /api/turns`、`DELETE /api/turns/{id}` |
| Session | `GET/POST /api/sessions`、`GET/PATCH/DELETE /api/sessions/{id}` |
| Session 生命周期 | `POST /api/sessions/{id}/archive`、`/restore`、`/fork` |
| 历史消息 | `GET /api/sessions/{id}/history` |
| Asset | `POST /api/assets` |
| Operation | `GET /api/operations`、`POST /api/operations/{name}`、`DELETE /api/operation-invocations/{id}` |
| Interaction 响应 | `POST /api/interactions/{id}/response` |

## Turn 与流式输出

存在流式能力时优先使用流式执行。`Stream` 返回错误后不会通过 `Run` 重试。运行中的 Turn 会在状态快照中暴露 `revision`、`output` 和 `reasoning`。输出和推理增量事件包含 `invocationId`、`revision` 与 `text`；客户端应忽略不大于当前投影 revision 的事件。

Turn 完成后会从运行中注册表移除，完整历史仍以 `agent.History` 为准。

`agent.invocation.started` 携带运行中 Turn 的快照。`agent.invocation.finished` 携带 `invocationId`、状态、执行结果统计，以及规范的 `result.output` 或错误详情。这些 Web 生命周期事件也能表示 SDK Turn 生命周期建立前发生的失败。

十种 `agent.turn/round/model/tool.*` 事件仅来自 Observation，并保留 SDK correlation、sequence 和物化时间。需要将 `host` 导出的 Observer 接入 Observation Consumer 才会收到这些事件；后端本身不会创建 Consumer，也不会合成执行事实。Web invocation ID 与 SDK turn ID 始终是两个独立标识。

历史消息和规范结果使用有序内容数组、字符串形式的 `kind`，以及显式的媒体来源。内联输出字节在 JSON 中编码为 base64；URI 和 Asset 输出来源会原样保留，不会被后端读取。Turn 输入仅接受基于 Asset 的附件。空文本和仅含附件的 Turn 会交由 Agent 的领域校验处理。

## Asset 上传

Asset 上传直接使用请求体原始字节，并要求提供已知的 `Content-Length`。每个请求只上传一个 Asset。调用 Store 前会检查大小限制，零字节上传同样受支持。

上传成功返回 `201`：

```json
{
  "id": "asset-123",
  "size": 123
}
```

未提供长度时返回 `411`，超过大小限制时返回 `413`，未配置可选的 Asset Store 时返回 `501`。文件名和 MIME 元数据由后续创建 Turn 时的 Attachment DTO 提供。

## Operation

Operation 调用请求格式如下：

```json
{
  "sessionId": "optional-session-id",
  "input": {}
}
```

Operation Definition 按组件图提供的顺序生成快照，并在服务器开始监听前编译其 Draft 2020-12 Schema。Schema 必须自包含：支持本地 `$ref`，不会获取外部资源。输入和成功结果都必须是满足对应 Schema 的 JSON 对象。

Operation 不存在时返回 `404`；输入无效时会在调度前返回 `400`。调用被接受后返回 `202` 和 invocation ID。`operation.started`、`operation.completed`、`operation.failed` 与 `operation.canceled` 事件均携带 invocation 快照，该快照也会出现在 `/api/state` 中。

Operation 状态包括 `running`、`succeeded`、`failed` 和 `canceled`。除全部运行中调用外，后端还会保留最近 `operation_retention` 个终态结果。输出未通过 Schema 校验时，invocation 会以 `operation_invalid_output` 错误结束，并且不会重试 Operation。

取消已经结束的调用返回 `409`；调用 ID 不存在或已被淘汰时返回 `404`。结果可在浏览器刷新后恢复，但进程重启或超过保留上限后无法恢复。

## SSE 与状态恢复

SSE 客户端必须先通过 `GET /api/state` 获取状态快照和 cursor，再连接：

```http
GET /api/events?after=<cursor>
```

请求的 cursor 已超出有界 replay 窗口时返回 `409`，客户端需要重新获取完整状态快照。

## 生命周期

`app` 组件通过类型化的宿主依赖使用 ABI `invocation.Invocation` 和 `lifecycle.Controller`。`--ingot-check` 会校验依赖、配置和 Operation Schema，但不会监听配置的端口。HTTP 服务器异常退出时会请求关闭进程。

Cleanup 会取消 HTTP/SSE 请求和后台 invocation，等待 Turn 与 Operation 收尾；达到清理 deadline 时会强制关闭连接。单个 HTTP 请求或 SSE 连接关闭不会取消仍在运行的 Turn 或 pending interaction。

## Interaction

Interaction Request 会在注册前完成校验。提交的 JSON `null`、错误的基础类型、未知字段和声明选项之外的值都会被拒绝，且不会消费 pending request。

敏感默认值仅保留在服务端，settlement 事件不包含用户提交值。当前状态变更与对应事件保持一致顺序。Operation 使用的 Channel 会在 pending/state 快照和所有 Interaction 事件中携带 invocation scope。

State ID 仍然等于 `State.Name`；scope 不会生成新的全局 State identity。

## 验证

在插件目录中运行测试：

```sh
GOWORK=off go test -race ./...
```
