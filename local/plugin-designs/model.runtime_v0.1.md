# `model.runtime` Plugin v0.1 设计方案

> 状态：Draft  
> Exports：`model.Runtime`、`model.StreamingRuntime`

## 1. 定位与 Contract

`model.runtime` 是 Model complete/stream 的统一 chokepoint，负责 Named Provider registry、默认选择、独立 typed interceptor chain和 streaming capability check。

```go
type Dependencies struct {
    Providers          []sdk.Named[model.Provider]
    Interceptors       []model.Interceptor
    StreamInterceptors []model.StreamInterceptor
}

type Exports struct {
    Runtime   model.Runtime
    Streaming model.StreamingRuntime
}
```

## 2. Config

```go
type Config struct {
    DefaultProvider string `toml:"default_provider"`
    DefaultModel    string `toml:"default_model"`
}
```

- Providers 至少一个；Named name 非空、唯一，Value 非 nil/typed-nil；
- default provider absent 时：只有一个 Provider则自动选择，多于一个则 Config Error；
- 显式 default 必须存在；
- default model 可以为空，此时每个 Request 必须显式提供 Model；
- default 只填充空字段，不覆盖 caller 显式值。

## 3. Startup

`New` snapshot provider slice并构建 immutable registry；完整执行 `sdk.CheckUniqueNames` 和 typed-nil validation。Complete chain 使用 `pipeline.Compose`，首个 Model Interceptor最外层；Streaming 使用等价的专用 compose，首个 Stream Interceptor最外层。两套 chain 完全独立。

## 4. Request execution

固定流程：

```text
deep-copy request
→ materialize provider/model defaults
→ complete 或 stream interceptor chain
→ terminal provider lookup
→ provider method
→ normalize provider/model fields
```

Interceptor 位于 Provider lookup 外层，因此可以审计、拒绝或按 copy-on-write 改写 Provider/Model 选择；terminal 对改写后的值重新 lookup。

规则：

- provider 不存在返回包装 `model.ErrProviderNotFound`；
- model 仍为空返回包装 `model.ErrModelNotFound`；
- Complete 调用 `Provider.Complete`；
- Stream 要求选中 Value 实现 `model.StreamingProvider`，否则返回 `model.ErrStreamingUnsupported`；
- StreamHandler error由 Provider/chain原样传播；
- 不在 Runtime 隐式 fallback或 retry；相关能力通过显式 Interceptor实现；
- `Messages`、`Tools`、`Stop`、所有 RawMessage 深拷贝，避免默认填充或恶意 Interceptor修改 caller aggregate；
- Response aggregate 在返回前归一化为独立值。

## 5. 并发、生命周期与错误

registry和 chains 在 startup 后 immutable；Complete/Stream concurrent-safe。Provider 本身按 SDK Contract concurrent-safe。

Runtime 不拥有 Provider生命周期，不能调用其 Cleanup；无后台任务，返回 nil Cleanup。错误使用 `%w` 保留 Provider、Interceptor、Context和 SDK sentinel。

## 6. Manifest、测试与验收

```toml
manifest_version = 1
name = "model.runtime"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖 Provider empty/duplicate/typed nil、default resolution、unknown provider/model、request deep copy、complete/stream interceptor完整 trace、short-circuit、streaming unsupported、handler error、并发 Provider选择和 race test。

验收要求 Complete 与 Stream 行为对称但 chain独立；Runtime 不包含任何 OpenAI-specific logic。
