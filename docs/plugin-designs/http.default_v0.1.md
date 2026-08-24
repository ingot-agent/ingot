# `http.default` Plugin v0.1 设计方案

> 状态：Implemented v0.1  
> Component：`default`  
> Exports：`httpx.Client`

## 1. 定位

`http.default` 为 Component Graph 提供一个共享、并发安全、由实例拥有连接池的 HTTP Client。它负责通用 HTTP transport 行为，不负责特定 API、认证协议、模型请求格式或业务级重试。

典型连接关系：

```text
http.default --httpx.Client--> model.openai-compatible
```

## 2. 目标与非目标

目标：

- 以调用参数 `context.Context` 作为 cancellation/deadline authority；
- 保持调用者传入的 `*http.Request` 不变；
- 复用连接并支持并发请求；
- 提供明确、严格解码的 transport 配置；
- Cleanup 释放本实例的 idle connections；
- 保留 Context、URL、TLS 和 `net` 错误链。

非目标：

- OpenAI-compatible 或其他业务协议；
- API key、Bearer token 等认证注入；
- request/response logging、metrics、retry 或 rate limit；
- response body 大小限制与业务错误归一化；
- 多个 Named HTTP Client。v0.1 每个 Component instance 只导出一个默认 Client。

日志、审批、重试等横切行为应由更高层 Provider 或 typed Interceptor 实现。

## 3. Component Contract

```go
package httpdefault

import (
    "context"

    "github.com/ingot-agent/sdk"
    "github.com/ingot-agent/sdk/httpx"
)

type Dependencies struct{}

type Exports struct {
    Client httpx.Client
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

`New` 不发起探测请求。它只校验配置、创建 Plugin-owned `http.Transport` 和 `http.Client`，因此初始化必须有界并及时返回。

## 4. Config

建议 root Config：

```go
type Config struct {
    ProxyMode                  string `toml:"proxy_mode"`
    ProxyURL                   string `toml:"proxy_url"`
    MaxIdleConns               int    `toml:"max_idle_conns"`
    MaxIdleConnsPerHost        int    `toml:"max_idle_conns_per_host"`
    IdleConnTimeoutSeconds     int    `toml:"idle_conn_timeout_seconds"`
    TLSHandshakeTimeoutSeconds int    `toml:"tls_handshake_timeout_seconds"`
}
```

### 4.1 默认值

| Field | Zero/absent 行为 | 约束 |
|---|---|---|
| `proxy_mode` | `"environment"` | `environment`、`direct`、`url` |
| `proxy_url` | empty | 仅 `proxy_mode="url"` 时 required |
| `max_idle_conns` | `100` | `>= 0` |
| `max_idle_conns_per_host` | `10` | `>= 0` |
| `idle_conn_timeout_seconds` | `90` | `>= 0` |
| `tls_handshake_timeout_seconds` | `10` | `>= 0` |

Proxy 语义：

- `environment`：使用 `http.ProxyFromEnvironment`；
- `direct`：Transport 的 Proxy 为 nil；
- `url`：解析 `proxy_url` 并使用固定 proxy；只接受 `http` 或 `https` scheme；
- 非 `url` 模式设置非空 `proxy_url` 时返回 Config Error，避免静默忽略拼写或配置错误。

v0.1 不设置 `http.Client.Timeout`。整个请求生命周期由每次 `Do` 的显式 Context 控制；dial、TLS handshake 和 idle connection timeout 仍是 transport 资源边界。

## 5. 实现设计

内部实现持有独立 Transport：

```go
type client struct {
    client *http.Client
}

func (c *client) Do(
    ctx context.Context,
    req *http.Request,
) (*http.Response, error) {
    req2 := req.Clone(ctx)
    return c.client.Do(req2)
}
```

推荐从 `http.DefaultTransport.(*http.Transport).Clone()` 获得 Go toolchain 对应的安全默认值，再应用已校验配置。不得直接修改全局 `http.DefaultTransport`。

### 5.1 Request ownership

- 不修改原始 request 的 Context、Header、URL、Body 或其他字段；
- `req.Clone(ctx)` 复制 request 和 Header map，但 Body 仍遵循 `net/http` 的流式 ownership；
- 调用期间 request body 由 `net/http` 消费；Plugin 不在调用返回后额外保存 request；
- 返回的 `*http.Response` 和 Body 由 caller 持有，caller 负责关闭 Body。

### 5.2 Error

Plugin 可以补充稳定操作上下文，但必须保留原错误：

```go
return nil, fmt.Errorf("http request: %w", err)
```

不得把 `context.Canceled`、`context.DeadlineExceeded`、`net.Error` 或 `*url.Error` 转换为不可识别的字符串错误。

`req == nil` 或 `ctx == nil` 属于调用方编程错误；实现可以返回明确错误，不应产生难以定位的 nil dereference。

## 6. 并发与生命周期

- `Do` 可由多个 goroutine 并发调用；
- 每次 `New` 创建独立 Transport、Client 和连接池；
- 不缓存 request/response；
- Cleanup 调用 Plugin-owned Transport 的 `CloseIdleConnections`；
- Cleanup 不关闭 caller 正在读取的 response body，也不承诺中止 active request；active request 由其调用 Context 控制；
- Cleanup 本身不阻塞，可在 Context 已取消时立即执行并返回。

Cleanup 示例：

```go
cleanup := func(context.Context) error {
    transport.CloseIdleConnections()
    return nil
}
```

## 7. Manifest

Module path 在创建实际仓库时确定。Manifest 基线：

```toml
manifest_version = 1
name = "http.default"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

Plugin 不声明 `[state]`。

## 8. 测试方案

### 8.1 Contract

- `Exports.Client` 满足 `httpx.Client`；
- `New` 具有精确 Component signature；
- unknown TOML field 被 `config.Decode` 拒绝。

### 8.2 Request semantics

- handler 观察到的是显式 `Do(ctx, req)` 的 Context；
- 取消 Context 会终止阻塞请求并保留 `context.Canceled`；
- deadline 保留 `context.DeadlineExceeded`；
- 调用前后原始 request 的 Context、Header、URL 保持不变；
- response body ownership 交给 caller。

### 8.3 Config

- 三种 proxy mode；
- invalid mode、invalid URL、unsupported scheme；
- `proxy_url` 与 mode union mismatch；
- 负数 transport 参数；
- zero/absent default materialization。

### 8.4 Concurrency 与 Cleanup

- 多个请求并发通过同一实例；
- 两次 `New` 的 Transport 互不影响；
- Cleanup 后 idle connection 不再复用；
- 一个实例 Cleanup 不影响另一个实例。

测试使用 `httptest.Server`，不得依赖公网。

## 9. 验收标准

1. 公开 Contract 与 SDK 完全一致；
2. Config strict decode 和默认值测试完整；
3. Context authority 与 request immutability 通过测试；
4. 并发测试和 `go test -race ./...` 通过；
5. Cleanup 只释放本实例拥有的 idle connection；
6. 插件不包含 Provider 认证、业务 retry 或模型协议逻辑。

## 10. v0.1 实现决策

- module path 为 `github.com/ingot-agent/http-default`；
- `proxy_mode` 默认使用 `environment`，显式 `direct` 可完全禁用代理；
- 首版不提供 `response_header_timeout_seconds`，请求级 deadline 由调用 Context 控制。
