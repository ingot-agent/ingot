# app.backend

`app.backend` 是 ingot 的浏览器应用，包含 Vue 3 + Tailwind CSS 前端及轻量 HTTP/SSE 应用边界。插件目录名为 `app-webui`，manifest ID 保持 `app.backend`。它是一个包含两个组件的复合插件：

- `host` 持有进程内的 `EventHub`、支持作用域的 `interaction.Channel` 和 `observation.Observer`。该组件没有依赖，因此 Agent 可以使用这些能力而不会在组件图中形成环。
- `app` 持有 HTTP 服务器、Controller、运行中的 Turn，以及保留的 Operation 结果。它依赖 `agent.History`、`session.Store`、`session.Manager` 和 `session.Query`。相互独立且可选的 `agent.Runtime` 与 `agent.StreamingRuntime` 至少需要提供一个。`asset.Store` 是可选依赖，Operation 通过 `[]operation.Operation` 收集。

当前实现使用 `ingot-abi v0.1.0` 和正式发布的 `sdk v0.2.7`。插件不修改 SDK，也不额外覆盖工作区中的 SDK 选择。

## 启动 Web UI

在已初始化、配置好模型供应商的 ingot home 中，用 Web 应用替换 CLI。以下命令从仓库根目录执行；使用独立 home 时，为每条命令添加相同的全局 `--home /path/to/home` 参数：

```sh
go build -o ingot ./cmd/ingot
./ingot plugin remove github.com/ingot-agent/app-cli
./ingot plugin add --path ./plugins/app-webui
# 在该 home 的 config.toml 中添加下方 app.backend 配置
./ingot apply
./ingot web
```

然后打开 `http://127.0.0.1:7316/`。`web` 是转发给 Runtime Image 的命令；HTTP 监听由应用组件的生命周期启动，不依赖 Builder 的专用命令或插件特判。不要同时保留 CLI 的 Interaction 提供者。

前端产物通过 Go `embed` 编入 Runtime Image；运行时不需要 Node、Vite 或外部 CDN。修改前端后需重新构建前端，再执行 `ingot apply`。开发命令见 [前端说明](../../web/app-webui/README.md)。

初版面向可信的本机单用户环境，没有登录、多租户隔离或公网部署保护。请保持回环监听，不要直接暴露至局域网或互联网。

## 工作区能力

- 会话搜索、新建、重命名、归档/恢复、分叉与确认删除；正在执行时禁用生命周期变更。
- Markdown、代码高亮/复制、折叠推理、工具调用卡片和独立执行详情；Turn、Round、Model、Tool 与用量信息来自公开 SDK 能力。
- 流式输出及 Run-only 降级、停止执行、内联审批/自由输入、跨会话待处理请求抽屉。
- 拖放、选择及粘贴图片上传；历史附件按需预览或下载。Asset 能力缺失时禁用上传。
- Operation 简单表单与复杂 JSON 输入、服务端 Schema 校验、结果恢复和取消；JSON 模式保留提交的原始文本，不经数值转换。
- 浅色/深色/跟随系统、中英文、桌面侧栏及移动端抽屉；最小适配宽度 360px。

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
| Asset | `POST /api/assets`、`GET /api/assets/{id}` |
| Operation | `GET /api/operations`、`POST /api/operations/{name}`、`DELETE /api/operation-invocations/{id}` |
| Interaction 响应 | `POST /api/interactions/{id}/response` |

## Turn 与流式输出

存在流式能力时优先使用流式执行。`Stream` 返回错误后不会通过 `Run` 重试。运行中的 Turn 会在状态快照中暴露 `revision`、`output` 和 `reasoning`。输出和推理增量事件包含 `invocationId`、`revision` 与 `text`；客户端应忽略不大于当前投影 revision 的事件。

Turn 完成后会从运行中注册表移除，完整历史仍以 `agent.History` 为准。

`agent.invocation.started` 携带运行中 Turn 的快照。`agent.invocation.finished` 携带 `invocationId`、状态、执行结果统计，以及规范的 `result.output` 或错误详情。这些 Web 生命周期事件也能表示 SDK Turn 生命周期建立前发生的失败。

十种 `agent.turn/round/model/tool.*` 事件仅来自 Observation，并保留 SDK correlation、sequence 和物化时间。需要将 `host` 导出的 Observer 接入 Observation Consumer 才会收到这些事件；后端本身不会创建 Consumer，也不会合成执行事实。Web invocation ID 与 SDK turn ID 始终是两个独立标识。

历史消息和规范结果使用有序内容数组、字符串形式的 `kind`，以及显式的媒体来源。内联输出字节在 JSON 中编码为 base64；URI 和 Asset 输出来源会原样保留，不会被后端读取。Turn 输入仅接受基于 Asset 的附件。空文本和仅含附件的 Turn 会交由 Agent 的领域校验处理。

## Asset 上传与读取

Asset 上传直接使用请求体原始字节，并要求提供已知的 `Content-Length`。每个请求只上传一个 Asset。调用 Store 前会检查大小限制，零字节上传同样受支持。

上传成功返回 `201`：

```json
{
  "id": "asset-123",
  "size": 123
}
```

未提供长度时返回 `411`，超过大小限制时返回 `413`，未配置可选的 Asset Store 时返回 `501`。文件名和 MIME 元数据由后续创建 Turn 时的 Attachment DTO 提供。

`GET /api/state` 的 `assets` 字段返回 `available` 和 `maxBytes`。读取接口通过 `Store.Stat/Open` 流式传输已有 Asset；不存在时返回 `404`，未配置 Store 时返回 `501`。响应使用 `application/octet-stream`、`Content-Disposition: attachment`、`nosniff` 和 `no-store`，不会信任历史消息中的 MIME 类型来执行内容。

前端仅对允许的图片、音频和视频格式创建 Blob 预览；HTML、SVG 等文件保留为下载。Markdown 原始 HTML 被禁用，远程图片转换为显式链接，避免后台请求第三方资源。

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

前端每次重连都重新引导，不自动重发 Turn、Operation 或 Interaction 提交。历史加载与 SSE 独立：Agent 正在执行时，History 可能等待该 Turn 收尾，但审批、流式输出和取消仍可使用。输出与推理共享 revision，重叠回放不会重复追加。

持久消息以 `agent.History` 为准；终态 Turn 用量、推理和 Observation 详情仅在当前连接的内存中可见，刷新/重连后不提供历史执行回放。Operation 结果按服务端保留策略恢复。未发送草稿和敏感输入不写入本地存储；只有语言、主题与面板偏好会保存。

## 生命周期

`app` 组件通过类型化的宿主依赖使用 ABI `invocation.Invocation` 和 `lifecycle.Controller`。`--ingot-check` 会校验依赖、配置和 Operation Schema，但不会监听配置的端口。HTTP 服务器异常退出时会请求关闭进程。

Cleanup 会取消 HTTP/SSE 请求和后台 invocation，等待 Turn 与 Operation 收尾；达到清理 deadline 时会强制关闭连接。单个 HTTP 请求或 SSE 连接关闭不会取消仍在运行的 Turn 或 pending interaction。

## Interaction

Interaction Request 会在注册前完成校验。提交的 JSON `null`、错误的基础类型、未知字段和声明选项之外的值都会被拒绝，且不会消费 pending request。

敏感默认值仅保留在服务端，settlement 事件不包含用户提交值。当前状态变更与对应事件保持一致顺序。Operation 使用的 Channel 会在 pending/state 快照和所有 Interaction 事件中携带 invocation scope。

普通 Channel 的 Request/Emit/Set/Clear 会从 context 中的 SDK Observation correlation 提取 Agent scope，让 `tool.ask` 与审批请求能够定位到会话/工具卡片。显式 `Scoped` Channel 的作用域优先，不会被 context 覆盖。SDK correlation 未表达 Round 是否存在，只有工具关联明确时才投影其 RoundIndex。

State ID 仍然等于 `State.Name`；scope 不会生成新的全局 State identity。

## 验证

在插件目录中运行测试：

```sh
GOWORK=off go test -race ./...
```
