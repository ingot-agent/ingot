# `usage.default` Plugin v0.1 设计方案

> 状态：Implemented v0.1  
> SDK Contract：v0.1.6
> Dependencies：`model.RequestResolver`  
> Exports：`usage.Counter`

## 1. 定位

`usage.default` 为完整 `model.Request` 提供 model-aware 的调用前 input token 计算能力。它位于 Prompt render 之后、Model invocation 之前，可供 `context.compact`、请求预算检查、成本预估以及未来的 usage monitoring 共同使用。

Plugin 计算的是包含 Messages、Tool definitions、Tool calls 和 provider/model chat framing 的完整模型输入，不提供会被误用于上下文预算的简单 `CountText(string)` 接口。不同 Provider/Model 可以选择不同的内置计数 profile；调用方只依赖统一的 `usage.Counter`，不感知具体 tokenizer 或计数公式。

v0.1 不把不同 profile 拆成独立 Backend Plugin。profile registry、Provider/Model 路由、缓存和结果验证都由 `usage.default` 实例内部拥有。未来只有在第三方确实需要独立提供计数实现时，才考虑新增 Backend Contract；不得提前通过全局注册表或字符串服务定位绕过 Component Graph。

Plugin 不负责执行模型调用、修改请求、压缩上下文、记录账单、持久化用量、查询历史统计或导出监控指标。

## 2. SDK 前置 Contract

本 Plugin 已在 SDK 增加 `usage` package。Contract：

```go
package usage

type Accuracy string

const (
    AccuracyExact      Accuracy = "exact"
    AccuracyUpperBound Accuracy = "upper_bound"
    AccuracyEstimate   Accuracy = "estimate"
)

type CountRequest struct {
    Invocation model.Request
}

type CountResult struct {
    InputTokens int64
    Accuracy    Accuracy
    Source      string
    Provider    string
    Model       string
}

type Counter interface {
    CountInput(
        context.Context,
        CountRequest,
    ) (CountResult, error)
}

var ErrUnsupportedModel = errors.New("usage counter does not support model")
```

语义：

- `Invocation` 是完整、只读的模型请求；Counter 不得修改 caller aggregate；
- `InputTokens` 非负，使用 `int64` 避免累计监控场景中的平台相关 `int` 上限；
- `AccuracyExact` 表示 profile 对目标 Provider/Model 的 tokenizer、chat framing 和 tool framing 有明确且受测试约束的实现；
- `AccuracyUpperBound` 表示结果由实现保证不小于实际输入 token 数；
- `AccuracyEstimate` 只表示估算，不能冒充精确值或安全上界；
- `Source` 是稳定、非空的 profile/version 标识，用于诊断、缓存和监控来源区分；
- `Provider`、`Model` 是解析默认值后的实际计数目标；
- Counter concurrent-safe，阻塞计算观察 Context，返回的 aggregate 归 caller。

### 2.1 最终 Provider/Model 解析

现有 `model.runtime` 允许 Request.Provider/Model 为空并在调用时填充默认值。计数发生在 Provider 调用之前，因此不能让 `usage.default` 再维护一份可能漂移的默认 Provider/Model 配置。

SDK同时增加只读解析能力，并由 `model.runtime` 使用与 Complete/Stream 相同的默认选择逻辑实现：

```go
package model

type RequestResolver interface {
    ResolveRequest(
        context.Context,
        Request,
    ) (Request, error)
}
```

`ResolveRequest` deep-copy输入，只填充空 Provider/Model，不调用 Provider、不执行 Model Interceptor、不产生 usage。`model.runtime` 导出的 Resolver 与 Runtime 共享同一个 immutable provider/default registry；Complete、Stream 和 Resolver 必须复用同一内部解析函数，避免行为分叉。

`usage.default` 先调用 Resolver，再进行 route 匹配和计数。这样使用 `model.runtime` 默认模型的 Agent 与显式指定模型的 Agent 得到相同结果。

上述interface由SDK定义，Plugin实现只依赖公共Contract，不自行重复定义。

## 3. Component Contract

在 SDK 前置 Contract 落地后，Component 形状为：

```go
type Dependencies struct {
    Resolver model.RequestResolver
}

type Exports struct {
    Counter usage.Counter
}
```

Resolver required、非 nil/typed-nil。`New` snapshot并验证全部配置，创建独立的 immutable route/profile registry 和有界内存缓存。

## 4. Config

```go
type Config struct {
    Routes       []Route `toml:"routes"`
    CacheEntries int     `toml:"cache_entries"`
}

type Route struct {
    Provider     string `toml:"provider"`
    ModelPattern string `toml:"model_pattern"`
    Profile      string `toml:"profile"`
}
```

示例：

```toml
cache_entries = 1024

[[routes]]
provider = "deepseek"
model_pattern = "deepseek-v4-flash"
profile = "deepseek-v4-flash-api-default-thinking-v1"

[[routes]]
provider = "openai"
model_pattern = "gpt-5.*"
profile = "openai-o200k-chat"
```

规则：

- 至少配置一条 route；
- Provider、ModelPattern、Profile required、valid UTF-8且非空；
- Provider 使用 exact match；
- ModelPattern 使用 Go RE2 regexp并按完整字符串匹配，`New` 时完成编译；
- Profile 必须存在于本版本内置 profile registry；
- routes 按声明顺序匹配，第一条命中生效；因此更具体的规则放在前面，显式 catch-all 放在最后；
- `cache_entries=0` 使用默认1024，负数非法；允许未来通过明确配置关闭缓存，但不能产生无界缓存；
- Config 不能声明 Accuracy。Accuracy 是 profile 的代码属性，用户不能把 heuristic 配置伪装成 exact。

## 5. 内置 Profile

profile 是 package-private、immutable 的完整请求计数实现，不是 SDK Capability，也不通过 Builder 注入：

```go
type profile interface {
    CountInput(context.Context, model.Request) (int64, error)
    Accuracy() usage.Accuracy
    Source() string
}
```

一个 profile 必须同时定义并测试：

- 支持的 Provider/Model family；
- tokenizer/encoding版本；
- system/user/assistant/tool role framing；
- message boundary和special tokens；
- Tool definition、InputSchema、ToolCall Arguments及Tool result framing；
- nil与非nil empty字段的处理；
- 当前 profile 的 Accuracy 和稳定 Source ID。

新模型不能仅通过猜测其名称复用现有 exact profile。只有 wire/chat-template兼容性有依据并通过golden vector验证时才能声明 `exact`。无法保证精确但可以给出保守上界时声明 `upper_bound`；普通字符、字节或经验比例只能声明 `estimate`。

v0.1 内置两个 Profile：

- `unicode-estimate-v1`：ASCII按约4 bytes/token、非ASCII按1 rune/token估算，并加入版本化的OpenAI-style message/tool framing常量；
- `deepseek-v4-flash-api-default-thinking-v1`：使用固定版本的 DeepSeek-V4-Flash 官方 `tokenizer.json` 和 `encoding_dsv4.py` 的 thinking-mode 模板语义，覆盖 system/user/assistant、DSML Tool Schema、Tool Call、Tool Result、special token、连续 Tool Result 排序以及无 System 时的工具注入；同时计入托管 API 默认 thinking/high effort 经线上 golden vector 校准得到的 79-token server framing。

两个 Profile 当前均报告 `estimate`，且必须由用户显式 route。DeepSeek Profile 的本地 tokenizer 与官方文本、基础消息和工具调用 golden vector 已一致；托管 API 的基础、长文本和工具请求也确认默认 high effort 均比公开模板多 79 tokens。由于该 framing 由服务端拥有、可能不随公开 tokenizer 版本同步变化，而且 SDK 当前不能表达 `reasoning_content`、thinking mode 开关和 reasoning effort 等 DeepSeek 完整请求维度，因此仍不能声明 `exact` 或安全上界。

官方 tokenizer 资源固定到 `deepseek-ai/DeepSeek-V4-Flash` revision `60d8d70770c6776ff598c94bb586a859a38244f1`，以 gzip 形式嵌入 Plugin，运行时不联网；来源、SHA-256 与 MIT License 记录在 `plugins/usage-default/assets/`。未配置匹配 route 的模型返回包装 `usage.ErrUnsupportedModel`，不静默退回 heuristic。后续 exact/upper-bound Profile 必须先取得目标 Provider/Model 的 tokenizer、chat template 和托管 API golden vector 依据。

## 6. CountInput 流程

固定流程：

```text
validate Context and request aggregate
→ deep-copy CountRequest.Invocation
→ Resolver.ResolveRequest
→ validate resolved Provider/Model
→ first-match route selection
→ lookup immutable internal profile
→ compute cache key and lookup
→ profile counts the complete resolved request
→ validate non-negative result
→ return caller-owned CountResult
```

请求验证至少覆盖：

- Provider、Model、Stop和所有Message string为valid UTF-8；
- Role属于SDK支持集合；
- ToolCall ID/Name非空且Arguments为valid JSON；
- Tool Definition Name/Description为valid UTF-8，InputSchema为valid JSON；
- Temperature为finite value；
- MaxTokens为nil或positive；
- aggregate中的slice、pointer和RawMessage保持presence语义。

当前内置profile没有可靠的多模态计数策略。resolved Request只要包含非text part，就在构造JSON cache key或调用profile前返回包装`ErrUnsupportedModel`，不得把媒体metadata或bytes静默当作普通文本估算。

Counter 不负责验证完整对话顺序；合法但处于中间tool round的 `model.Request` 仍可能需要计数。对话turn分组继续属于 `context.compact` policy，而不是 usage capability。

## 7. Accuracy 与调用方策略

Counter只报告事实和来源，不替调用方决定是否可接受：

```text
exact       可直接用于精确高低水位
upper_bound 可用于保守预算，但可能提前触发压缩
estimate    只适合预估、展示或显式允许的降级策略
```

`context.compact` 后续增加可接受精度配置，例如：

```toml
allowed_accuracies = ["exact", "upper_bound"]
```

若结果精度不在允许集合中，Context Compactor返回明确错误；不得自动切回canonical bytes，也不得把bytes和tokens放进同一个数值水位比较。

## 8. Cache、ownership 与隐私

计数结果可按以下内容的稳定摘要缓存：

```text
profile Source ID
resolved Provider/Model
完整 model.Request token-bearing projection
```

projection至少包含Messages、Tools及所有可能影响profile framing的presence信息。缓存key使用SHA-256等固定digest，不在key、日志或错误中保存/输出prompt原文。缓存value只含token数、Accuracy和Source。

缓存为每个Component instance独立的有界LRU：

- concurrent-safe；
- 相同key的并发miss合并为一次实际计算；
- 等待single-flight时观察各自Context；
- eviction不影响正确性；
- 不写Session Store或磁盘；
- Cleanup后不再接受新计算，若实现无后台任务可同步释放内存并及时返回。

所有输入在跨越调用边界时deep-copy；profile不能保留caller请求引用。CountResult只含scalar/string，返回后与内部cache独立。

## 9. 与实际 Usage 和未来监控的边界

`usage.Counter` 给出调用前计算值；Provider成功响应中的 `model.Response.Usage` 是调用后实际值。二者不能互相覆盖。

未来独立监控可作为 Model complete/stream interceptor观察所有模型调用：

1. Provider返回Usage时优先记录Provider实际值；
2. Provider未返回Usage时，可单独记录Counter的preflight estimate及其Accuracy，但不得用它填补`agent.Accounting`的execution Usage；
3. 监控事件必须记录实际/计算来源和Accuracy；
4. 主Agent调用、context summary调用和其他后台调用通过request-scoped scope区分；
5. 默认只记录count、Provider、Model、operation、duration和status，不记录prompt/response正文。

`model.Usage.Reported`已经区分“Usage缺失”和“Provider明确报告0 token”。它只表达presence，不表达Estimate/Exact/UpperBound；Counter的Accuracy语义保持独立，也不定义持久化、价格表或监控查询API。

## 10. `context.compact` 接入

后续迁移中，`context.compact`增加`usage.Counter` dependency，并把高低水位从canonical bytes迁移到input tokens：

```go
type Dependencies struct {
    Model   model.Runtime
    Store   session.Store
    Counter usage.Counter
}
```

每次materialize raw或checkpoint view后，以完整candidate `model.Request`调用Counter。达到trigger后按完整turn生成summary，再重新计数，直到不大于target。

`summary_chunk_bytes`仍可作为“单次优先折叠多少source数据”的内部选段参数，但不再决定是否触发或结束压缩；未来可另行增加token-based chunk selection。`policy_digest`必须加入token水位、allowed accuracies、Counter Source和Accuracy，防止不同计数策略错误复用checkpoint chain。

## 11. Context、并发、生命周期与错误

- Counter concurrent-safe，不按Session串行；相同请求只在cache single-flight层合并；
- Resolver/profile调用和等待cache计算时观察Context；
- Config、compiled routes和profile registry在New后immutable；
- Plugin不拥有Resolver生命周期；
- 不使用package-level mutable singleton；
- 无持久状态，正常重启只丢失可重建cache；
- 错误通过`%w`保留Context、Resolver和SDK sentinel。

Plugin错误建议：

```go
ErrInvalidConfig
ErrInvalidRequest
ErrUnsupportedModel
ErrCountFailed
ErrClosed
```

不支持模型、无匹配route、profile失败和Context取消必须可区分。错误信息可包含Provider、Model、route序号和Source，不得包含Messages、Tool Arguments、Tool Schema正文或其他潜在敏感输入。

## 12. Manifest、测试与验收

```toml
manifest_version = 1
name = "usage.default"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖：

- exact Dependencies/Exports/New external Contract；
- nil/typed-nil Resolver；
- empty/invalid/ordered routes、regexp full match和first-match规则；
- unknown profile、unsupported model和显式heuristic route；
- Resolver default materialization与显式Provider/Model保持；
- complete Request ownership、nil/empty presence、UTF-8/JSON/finite number校验；
- 每个内置profile的官方或固定golden vectors，包括system/messages/tools/tool calls；
- exact/upper_bound/estimate不能由Config伪造；
- cache hit、bounded eviction、Source/version隔离和prompt不进入diagnostic；
- same-key single-flight、不同key并发、等待取消、Cleanup和race test。

验收要求：新增一个模型计数profile不修改 `context.compact` 或其他消费者；未知模型不静默估算；同一resolved Request、profile版本和Config产生稳定结果；Counter输出可被上下文预算和未来监控共同消费。

## 13. v0.1 Non-goals

- 不拆分独立Backend Plugin；
- 不提供任意纯文本Tokenizer API；
- 不实现上下文裁剪或摘要；
- 不调用主生成接口来猜测token数；
- 不保存prompt、response或usage历史；
- 不实现价格表、货币换算、配额、告警或监控UI；
- 不把Provider报告的实际Usage改写成Counter计算值；
- 不承诺一个profile适用于名称相似但chat template未验证的新模型。

## 14. v0.1实现决策

- SDK已新增`usage.Counter`、`CountRequest`、`CountResult`、`Accuracy`和`ErrUnsupportedModel`；
- SDK `model.RequestResolver`由`model.runtime`导出，与Complete/Stream复用同一默认值物化逻辑；Resolver验证最终Provider/Model，但不调用Provider或Interceptor；
- Plugin实现有序Provider/完整Model regexp路由、完整Request校验与deep-copy、SHA-256摘要key、有界instance-local LRU和same-key single-flight；
- Cleanup关闭实例并清空cache，关闭后新调用返回`ErrClosed`；
- 首版包含显式 `unicode-estimate-v1` 与 `deepseek-v4-flash-api-default-thinking-v1`；DeepSeek Profile 已固定官方 tokenizer/template，并通过本地及托管 API golden vectors，但因 provider-owned high-effort framing 与 SDK 请求维度尚未稳定，仍为 `estimate`，因此尚不能作为 `context.compact` 默认接受的 exact/upper-bound token 水位来源。
