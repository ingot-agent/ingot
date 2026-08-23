# `model.openai-compatible` Plugin v0.1 设计方案

> 状态：Draft  
> Dependencies：`httpx.Client`  
> Exports：`[]sdk.Named[model.Provider]`

## 1. 定位

`model.openai-compatible` 根据 Runtime Config 创建一个或多个 OpenAI Chat Completions-compatible Provider instance，完成 SDK `model.Request/Response` 与 HTTP JSON/SSE 协议之间的转换。

它不负责 Provider 选择、跨 Provider fallback、Runtime interceptor composition、Tool 执行或 Session persistence。

```go
type Dependencies struct {
    HTTP httpx.Client
}

type Exports struct {
    Providers []sdk.Named[model.Provider]
}
```

每个导出的 Value 实现 `model.StreamingProvider`，因此也满足 `model.Provider`。

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
}
```

规则：

- providers 至少一个，declaration order 即 Named export order；
- name 使用 Named identity grammar，非空且唯一；
- base URL required、absolute，只接受 http/https，禁止 fragment/query；canonicalization 去除 trailing slash；
- API key 可为空，以支持无认证的兼容服务；secret interpolation 在 Config Loader 层完成，本 Plugin 只接收最终值；
- models 去重且非空；empty 表示 pass-through 任意非空 model；非空时作为 allowlist；
- default headers 不得覆盖 Authorization、Content-Type、Organization、Project 或 User-Agent 等 Plugin-owned header；
- response/error limits 分别默认 16 MiB/64 KiB且必须为正数。

Config bytes、map 和 slice 在 `New` 时深拷贝。API key 不进入错误文本或 Tool/Interaction event。

## 3. HTTP protocol

v0.1 endpoint 固定为：

```text
POST <base_url>/chat/completions
```

请求映射：

- `Request.Model` required；allowlist 存在且不匹配时返回包装 `model.ErrModelNotFound`；
- Messages 映射 role、content、name、tool_call_id 和 tool_calls；
- Tools 映射为 function tools，`Definition.InputSchema` 原样嵌入 parameters；
- `Temperature`、`MaxTokens`、`Stop` 保持 pointer/presence 语义；
- Complete 使用 `stream:false`，Stream 使用 `stream:true` 和 usage inclusion；
- header 在新 request 上构造，不修改调用方对象；
- HTTP Context 精确使用 Provider 方法参数 ctx。

v0.1 只支持 SDK text/tool calling 范围；图像、音频、JSON mode、reasoning parameters 和 vendor extension 需要新的 typed Contract 或明确 extension map，不能偷偷塞入 Content string。

## 4. Complete

- 只接受 2xx status；非 2xx 读取受限 error body并返回 `ProviderHTTPError{StatusCode, RequestID, Body}`，不得包含 API key；
- response body 受 `max_response_bytes` 限制，读取后总是 close；
- strict decode 必需字段，同时允许兼容服务的未知响应字段；
- choice 必须恰好存在首个可用结果；
- Tool calls 的 arguments 保存为独立 `json.RawMessage` 且必须 valid JSON；
- 映射 finish reason、usage、实际 provider name和 model；
- aggregate output 完全归 caller，Provider 返回后不再修改。

Provider 不在 v0.1 自动 retry。重试、fallback和速率策略应由 Model Interceptor 明确实现，避免非幂等或隐藏延迟。

## 5. Streaming

- 按 SSE event 顺序解析 `data:`；忽略 comment/heartbeat；`[DONE]` 结束；
- 每个 text delta 同步调用 `StreamHandler`，严格保持交付顺序；
- handler 返回 error 时立即停止读取、关闭 body并原样向上传递；
- 首个 chunk 交付后任何网络/解析错误直接返回，不 retry；v0.1 整体不自动 retry，因此自然满足边界；
- 累积 role、content和 tool-call delta，最终构造完整 `model.Response`；
- malformed SSE、invalid UTF-8、oversize event、invalid tool arguments 或提前 EOF 返回 protocol error；
- Context cancel 时 close body并保留 Context error；
- 最终 response 的 Message 与已交付 delta一致；usage 缺失时保持 zero value。

## 6. 并发、生命周期和错误

Provider config immutable，Complete/Stream concurrent-safe。不同 Named Provider 共享依赖的 HTTP capability，但不共享 mutable request state。

Plugin 不拥有 HTTP Transport，因此 Cleanup 不关闭 HTTP dependency；无其他资源时返回 nil Cleanup。

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

- Config、Named order/uniqueness和 deep copy；
- request JSON/header golden及 secret redaction；
- all SDK role、tools、optional fields；
- HTTP/protocol/size/context errors；
- Complete mapping和 ownership；
- SSE fragmentation、heartbeat、DONE、tool-call accumulation、handler error；
- 并发 Complete/Stream和 race test；
- Provider Cleanup 不影响 shared HTTP。

待确认：兼容基线是否只采用 Chat Completions，或另建 Responses API Provider；本方案选择 Chat Completions v0.1，不在一个 Provider 中做自动协议猜测。
