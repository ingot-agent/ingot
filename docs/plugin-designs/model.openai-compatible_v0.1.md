# `model.openai-compatible` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`httpx.Client`、`asset.Resolver`
> Exports：`[]ingotabi.Named[model.Provider]`

## 1. 定位

`model.openai-compatible` 根据 Runtime Config 创建一个或多个 OpenAI Chat Completions-compatible Provider instance，完成 SDK `model.Request/Response` 与 HTTP JSON/SSE 协议之间的转换。

它不负责 Provider 选择、跨 Provider fallback、Runtime interceptor composition、Tool 执行或 Session persistence。

```go
type Dependencies struct {
    HTTP   httpx.Client
    Assets asset.Resolver
}

type Exports struct {
    Providers []ingotabi.Named[model.Provider]
}
```

每个导出的 Value 实现 `model.StreamingProvider`，因此也满足`model.Provider`。

## 2. Config

```go
type Config struct {
    Providers []ProviderConfig `toml:"providers"`
}

type ProviderConfig struct {
    Name               string            `toml:"name"`
    BaseURL            string            `toml:"base_url"`
    APIKey             string            `toml:"api_key"`
    Organization       string            `toml:"organization"`
    Project            string            `toml:"project"`
    Models             []string          `toml:"models"`
    DefaultHeaders     map[string]string `toml:"default_headers"`
    MaxResponseBytes   int               `toml:"max_response_bytes"`
    MaxErrorBodyBytes  int               `toml:"max_error_body_bytes"`
    MaxAssetBytes      int               `toml:"max_asset_bytes"`
    AssetConcurrency   int               `toml:"asset_concurrency"`
}
```

规则：

- providers 至少一个，declaration order 即 Named export order；
- name 长度 1–64 bytes，使用 `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` grammar，并且唯一；
- base URL required、absolute，只接受 http/https，禁止 userinfo、fragment 和 query；canonicalization 去除 trailing slash；
- API key 可为空，以支持无认证的兼容服务；该字段是已经进入 Runtime Config 的最终字符串。SDK v0.1 和本 Plugin 均不解释 `${secret:...}`、环境变量引用或其他 secret expression，外部配置生成与文件权限由部署侧负责；
- models 去重且非空；empty 表示 pass-through 任意非空 model；非空时作为 allowlist；
- default headers 的 key 按 HTTP 大小写不敏感规则判重；不得覆盖 Plugin-owned 的 `Authorization`、`Content-Type`、`Accept`、`OpenAI-Organization`、`OpenAI-Project` 和 `User-Agent`；配置中自身出现大小写不同的重复 key 也是 Config Error；
- default header name 必须是 RFC token，所有 header value 以及 API key、Organization、Project 不得包含 HTTP 非法控制字符；
- response/error/asset limits 分别默认16 MiB/64 KiB/20 MiB且必须为正数；asset concurrency默认4且必须为正数。

Config bytes、map 和 slice 在 `New` 时深拷贝。API key 不进入错误文本或 Tool/Interaction event。

## 3. HTTP protocol

v0.1 endpoint 固定为：

```text
POST <base_url>/chat/completions
```

请求映射：

- `Request.Model` required；allowlist 存在且不匹配时返回包装 `model.ErrModelNotFound`；
- Messages 映射 role、content、name、tool_call_id 和 tool_calls；纯文本Content继续编码为JSON string，包含媒体时编码为Chat Completions content parts；
- Tools 映射为 function tools，`Definition.InputSchema` 原样嵌入 parameters；
- `Temperature`、`MaxTokens`、`Stop` 保持 pointer/presence 语义；
- Complete 使用 `stream:false`，Stream 使用 `stream:true` 和 usage inclusion；
- header 在新 request 上构造，不修改调用方对象；
- `Content-Type` 固定为 `application/json`；Complete 的 `Accept` 为 `application/json`，Stream 的 `Accept` 为 `text/event-stream`；API key 非空时设置 `Authorization: Bearer <key>`，Organization/Project 非空时分别设置 `OpenAI-Organization`/`OpenAI-Project`，`User-Agent` 使用固定的 Plugin 版本标识；
- HTTP Context 精确使用 Provider 方法参数 ctx。

Provider支持所有role的text以及user message中的image：

- inline image和Asset Reference通过`data:<media-type>;base64,...`发送；MIME type必须可解析且为`image/*`，bytes不得超过`max_asset_bytes`；
- Asset读取先`Stat`校验大小，再受`asset_concurrency`限制执行`Open`，读取长度必须与immutable metadata一致，并在完成或Context取消时关闭body；
- URI image只接受absolute `http`/`https` URI并原样传递；拒绝`file:`、裸路径、`data:`、非法UTF-8及其他scheme，防止绕过asset大小和MIME边界；
- audio、video、file及非user image返回包装`content.ErrUnsupportedContent`，并携带message/part位置；

JSON mode、reasoning parameters和vendor extension仍需要新的typed Contract或明确extension map。

## 4. Complete

- 只接受 2xx status；非 2xx 读取受限 error body并返回 `ProviderHTTPError{StatusCode, RequestID, Body, Truncated}`。body 超限时截断但仍保留 HTTP 状态分类，Plugin 生成的顶层错误文本不得包含 API key；
- response body 受 `max_response_bytes` 限制，读取后总是 close；
- strict decode 已知字段，同时允许兼容服务增加未知响应字段；顶层必须是单个 JSON object，`model` 必须为非空字符串；
- `choices` 必须恰好一个且 `index=0`；choice 必须包含 assistant message 和非空 `finish_reason`，message 可以是 text、tool calls或两者；
- 每个 Tool call 的 id、function name 必须非空；arguments 保存为独立 `json.RawMessage` 且必须是 valid JSON value；
- usage 可缺失；存在时 `prompt_tokens`、`completion_tokens`、`total_tokens` 三个字段必须全部出现、非负，且 `total_tokens=prompt_tokens+completion_tokens`；
- Response.Provider 固定为当前 Named Provider name，Response.Model 使用响应中的实际 model；
- aggregate output 完全归 caller，Provider 返回后不再修改。

Provider 不在 v0.1 自动 retry。重试、fallback和速率策略应由 Model Interceptor 明确实现，避免非幂等或隐藏延迟。

## 5. Streaming

- 按 SSE event 顺序解析：支持 LF/CRLF，以空行结束 event；同一 event 的多个 `data:` line 按 SSE 规则使用 `\n` 连接；忽略 comment、`event`、`id`、`retry` 和无 data 的 heartbeat；
- `[DONE]` 必须是单个 data payload（允许字段值两侧协议空白），出现后结束；body EOF 前未出现 `[DONE]` 是 protocol error；
- `max_response_bytes` 限制整个 streaming response 的原始读取字节数，包含 SSE framing，不只限制单个 event；超限立即关闭 body并返回 `ResponseLimitError`；
- 每个 text delta 同步调用 `StreamHandler`，严格保持交付顺序；
- `delta.reasoning_content`（优先）或 `delta.reasoning`（前者为null/缺失时的别名）映射为 `StreamSemanticReasoning`；两字段同时存在不重复输出；`delta.content` 映射为默认 `StreamSemanticContent`，不从普通文本推断reasoning；
- 每种semantic使用独立的text part（index=0），start/delta/end均携带semantic；同一chunk同时包含reasoning和content时先发送reasoning，跨chunk保留到达顺序并允许交错；reasoning不累积进最终Response；
- handler 返回 error 时立即停止读取、关闭 body并原样向上传递；
- 首个 chunk 交付后任何网络/解析错误直接返回，不 retry；v0.1 整体不自动 retry，因此自然满足边界；
- 普通 data event 必须是单个 JSON object；有 choice 的 chunk 只接受一个 `index=0` choice，usage-only final chunk可以使用 empty choices；
- 按 tool-call index 累积 id、function name和 arguments fragments；index 必须从0连续出现且同一 index 的非空 id/name不得冲突，最终 arguments 必须是 valid JSON value；
- 累积 role、content、finish reason和 tool-call delta，最终构造完整 `model.Response`；Response.Provider 固定为 Named Provider name，Response.Model 使用 stream 中一致的非空 model；
- malformed SSE、invalid UTF-8、oversize event、invalid tool arguments 或提前 EOF 返回 protocol error；
- Complete 和 Stream 在 Context cancel 时主动 close body；因 close 唤醒的阻塞读取必须归一化为 Context error；
- 最终 response 的 Message.Content 与已交付Content delta一致，排除transient reasoning；usage 缺失时保持 zero value。

## 6. 并发、生命周期和错误

Provider config immutable，Complete/Stream concurrent-safe。不同 Named Provider共享依赖的HTTP和Asset capability，但不共享mutable request state；每个Provider独立限制并发Asset读取。

Plugin不拥有HTTP Transport或Asset Resolver，因此Cleanup不关闭依赖；无其他资源时返回nil Cleanup。

建议 typed error：`ProviderHTTPError`、`ProviderProtocolError`、`ResponseLimitError`。错误包装保留 Context、I/O 和 SDK sentinel。

## 7. Manifest、测试与验收

```toml
manifest_version = 1
name = "model.openai-compatible"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

使用 fake `httpx.Client`/`httptest.Server` 测试：

- Config、name/base URL/header 负例、Named order/uniqueness和 deep copy；
- request JSON/header golden、大小写重复header和 secret redaction；
- all SDK role、tools、optional fields，以及text/image content-parts mapping；
- inline/URI/Asset image、MIME/size/scheme/role负例、capabilities、asset close与取消阻塞读取；
- HTTP status 在错误 body 截断后仍保留、API key redaction、protocol/size/context errors；
- Complete mapping和 ownership；
- SSE fragmentation、LF/CRLF、multi-data line、heartbeat、required DONE、total size limit、tool-call accumulation、handler error；
- 并发 Complete/Stream和 race test；
- Provider Cleanup 不影响 shared HTTP。

v0.1兼容基线固定为Chat Completions；未来若支持Responses API，使用独立Provider或新typed Contract，不在同一Provider中自动猜测协议。
