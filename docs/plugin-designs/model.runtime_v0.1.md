# `model.runtime` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Exports：`model.Runtime`、`model.StreamingRuntime`、`model.RequestResolver`、`model.CapabilityResolver`

## 1. 定位与 Contract

`model.runtime` 是 Model complete/stream 的统一 chokepoint，负责 Named Provider registry、默认选择、独立 typed interceptor chain和 streaming capability check。

```go
type Dependencies struct {
    Providers          []ingotabi.Named[model.Provider]
    Interceptors       []model.Interceptor
    StreamInterceptors []model.StreamInterceptor
}

type Exports struct {
    Runtime      model.Runtime
    Streaming    model.StreamingRuntime
    Resolver     model.RequestResolver
    Capabilities model.CapabilityResolver
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

`Resolver` 提供调用前的只读默认值物化：它 deep-copy 请求，只填充空的
Provider/Model，并验证最终选择；不会调用 Provider 或执行 Model
Interceptor。它与 Complete/Stream 共用同一份 immutable provider/default
registry，供 `usage.default` 等调用前能力使用。

`Capabilities.ResolveCapabilities`复用相同的默认值物化与Provider lookup，再要求选中Provider实现`model.CapabilityProvider`；缺失时返回`model.ErrCapabilitiesUnavailable`。Runtime验证Provider返回的kind/source/role枚举并deep-copy aggregate，调用方修改结果不得污染后续查询。

## 3. Startup

`New` snapshot provider slice并构建 immutable registry；完整执行 `sdk.CheckUniqueNames` 和 typed-nil validation。Complete chain 使用 `pipeline.Compose`，首个 Model Interceptor最外层；Streaming 使用等价的专用 compose，首个 Stream Interceptor最外层。两套 chain 完全独立。

## 4. Request execution

固定流程：

```text
deep-copy request
→ materialize provider/model defaults once
→ complete 或 stream interceptor chain
→ terminal validate final provider/model and lookup
→ provider method and terminal-only response normalization
→ deep-copy returned response
```

Interceptor 位于 Provider lookup 外层，因此可以审计、拒绝或改写本次调用拥有的 Provider/Model 选择；terminal 对改写后的最终值执行 lookup。默认值只在进入 chain 前填充一次，terminal 不会在 Interceptor 把字段改回空字符串后再次补默认值，此时按缺失 Provider/Model 报错。

规则：

- provider 不存在返回包装 `model.ErrProviderNotFound`；
- model 仍为空返回包装 `model.ErrModelNotFound`；
- Complete 调用 `Provider.Complete`；
- Stream 要求选中 Value 实现 `model.StreamingProvider`，否则返回 `model.ErrStreamingUnsupported`；
- StreamHandler error由 Provider/chain原样传播；
- 不在 Runtime 隐式 fallback或 retry；相关能力通过显式 Interceptor实现；
- `Messages`、`Tools`、`Stop`、所有 RawMessage和指针字段递归深拷贝，避免默认填充或恶意 Interceptor修改 caller aggregate；复制必须保留 `nil` 与“非 nil 空值”的 presence 差异，不能把 caller 明确提供的空 slice/RawMessage折叠为 `nil`；
- 只有 terminal 实际调用 Provider 后才执行来源归一化：`Response.Provider` 强制设为最终选中的 Named Provider，`Response.Model` 非空时保留 Provider报告的实际模型，空时填入最终 Request.Model；
- terminal成功响应必须具有assistant role、非空Provider/Model、通过`content.Validate`的Content、非负且满足`TotalTokens == InputTokens + OutputTokens`的Usage；每个ToolCall必须有非空且合法UTF-8的ID/Name和合法JSON Arguments，否则包装`ErrInvalidResponse`；
- Stream逐part验证start/delta/end序列，text只接受UTF-8 text delta，media只接受data delta；最终重建Content必须与Provider返回的final Message.Content byte-exact一致；
- Interceptor short-circuit 返回的 Response 不冒充某次 Provider调用，不填充或覆盖其 Provider/Model，只做 ownership deep-copy；
- Response aggregate 在返回前归一化为独立值，Provider或Interceptor在返回后不得影响 caller持有的数据。

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

测试覆盖Provider empty/duplicate/typed nil、Interceptor typed nil、default resolution、Resolver默认值物化/校验/不调用Provider、Capabilities lookup/validation/ownership、Interceptor改写后不二次补默认值、unknown provider/model、multimodal request/response deep copy与presence保留、错误路径partial response ownership、terminal响应校验、complete/stream interceptor完整trace、stream part状态机/final一致性、short-circuit不做来源归一化、terminal来源归一化、streaming unsupported、handler error、并发Provider选择和race test。

验收要求 Complete 与 Stream 行为对称但 chain独立；Runtime 不包含任何 OpenAI-specific logic。
