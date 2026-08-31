# `context.compact` Plugin v0.1 设计方案

> 状态：Implemented v0.1  
> Dependencies：`model.Runtime`、`session.Store`  
> Exports：`contextwindow.Compactor`

## 1. 定位

`context.compact` 在 Prompt render 之后、每次 Model invocation 之前，对完整 `model.Request` 的 Messages 建立非破坏性的压缩视图。它不修改 Provider、Model、Tools 或 generation parameters，不删除或改写原始 Session Entry；关闭 Plugin 后完整历史仍可重新使用。

标准上下文布局：

```text
leading system messages
first anchor turns
frozen summary + immutable state-delta segment pairs
recent raw turns
current incomplete turn
```

Plugin 通过冻结旧摘要、只追加新摘要和事实 Delta，尽量保持前缀 byte-stable。达到段数上限后才执行一次 rollup，接受一次较大范围的 cache invalidation，以换取有界上下文。

它不实现向量检索、跨 Session memory、后台异步压缩、原始 Store 数据删除或精确 tokenizer。v0.1 的触发与目标明确使用 canonical request bytes，不将字符或字节冒充 token。

## 2. Component Contract

```go
type Dependencies struct {
    Model model.Runtime
    Store session.Store
}

type Exports struct {
    Compactor contextwindow.Compactor
}
```

摘要调用使用 `model.Runtime.Complete` 且 Tools 为非 nil empty slice，仍经过标准 Model interceptor chokepoint。摘要响应不得包含 ToolCalls。

## 3. Config

```go
type Config struct {
    Provider            string `toml:"provider"`
    Model               string `toml:"model"`
    TriggerRequestBytes int    `toml:"trigger_request_bytes"`
    TargetRequestBytes  int    `toml:"target_request_bytes"`
    AnchorTurns         int    `toml:"anchor_turns"`
    RecentTurns         int    `toml:"recent_turns"`
    SummaryChunkBytes   int    `toml:"summary_chunk_bytes"`
    SummaryMaxTokens    int    `toml:"summary_max_tokens"`
    SummaryMaxBytes     int    `toml:"summary_max_bytes"`
    MaxSummaryChunks    int    `toml:"max_summary_chunks"`
    MaxSummaryPasses    int    `toml:"max_summary_passes"`
}
```

- `trigger_request_bytes` 与 `target_request_bytes` required、positive，且 target 小于 trigger；
- `anchor_turns` 默认2，`recent_turns` 默认4，负数非法；
- `summary_chunk_bytes` 默认64 KiB，表示单次优先折叠的最小 source bytes；
- `summary_max_tokens` 默认1024，仅限制摘要模型输出；
- `summary_max_bytes` 默认64 KiB，限制验证后的摘要文本；
- `max_summary_chunks` 默认8，达到上限后先 rollup；
- `max_summary_passes` 默认8，限制单次 Compact 的 Model 调用数量；
- Provider/Model 非空时覆盖摘要调用选择；为空时继承本次 Invocation。最终选择仍为空时由 `model.Runtime` 自身 default或错误语义处理。

Config、Dependencies 和 fixed summary protocol共同进入 `policy_digest`。持久化checkpoint还记录摘要响应的实际Provider/Model；只有调用前的摘要选择已经完全确定且与这两个字段精确匹配时才允许复用。选择仍依赖`model.Runtime` default，或Runtime/Interceptor报告了不同实际身份时，旧checkpoint视为stale，避免不同模型错误复用同一摘要链；需要稳定restart reuse时应显式配置准确的摘要Provider/Model。

## 4. Input validation 与 Turn grouping

Invocation aggregate immutable。实现先 deep-copy并验证：

- SessionID 非空；
- role、name、tool linkage、Provider、Model和Stop等协议string必须valid UTF-8；JSON投影无法无损表示的非UTF-8 MIME type、URI或Asset ID由本Plugin显式拒绝，不允许`encoding/json`静默替换；
- ToolCall Arguments 与 Tool InputSchema 是 valid JSON；
- system messages只能构成 leading prefix；
- system prefix 后第一条 conversation message必须是user；
- 每个新 user message开始一个turn；
- assistant ToolCalls 的 ID/Name非空且唯一；
- 紧随其后的 tool messages按ToolCalls顺序构成连续prefix；
- 只允许最后一个turn处于等待Model或尾部Tool结果未完成状态。

Turn 是压缩和保留的原子单位。Tool-call assistant 与对应 Tool results 不得拆分到不同区域。

## 5. Canonical size 与高低水位

实现以Plugin-owned JSON projection对完整 `model.Request` 计算 canonical bytes，计入 Provider、Model、Messages、Tools、Temperature、MaxTokens 和 Stop。该值用于确定性策略和测试，不承诺等于任一 Provider 的线协议或 token count。

处理顺序：

1. 从 Store恢复与当前 policy、消息前缀匹配的最新 checkpoint chain；
2. materialize 已冻结 summaries、state snapshot/deltas和剩余raw messages；
3. 如果没有历史checkpoint且raw request未达到trigger，返回独立raw copy，`Changed=false`；
4. 如果materialized request未达到trigger，返回该压缩视图，`Changed=true`；
5. 达到trigger后，继续生成segment直到不大于target，或返回明确错误。

固定 system、anchor和recent区域本身已超过target时返回`ErrContextUncompactable`，不静默删除。

## 6. 增量摘要

可压缩区间为 anchor之后、recent之前的完整turn。每次从最旧未覆盖turn开始选择至少 `summary_chunk_bytes` 的连续turn；不足时选择全部剩余eligible turns。

摘要模型接收：

```text
System: fixed protocol, schema and untrusted-data rules
User: compact JSON containing current materialized state and source turns
```

严格响应：

```json
{
  "summary": "本段历史的叙事摘要",
  "operations": [
    {"op":"set","path":"/project/root","value":"D:\\ingot-local\\ingot"},
    {"op":"delete","path":"/constraints/avoid_sdk_changes"}
  ]
}
```

规则：

- 顶层和operation拒绝未知字段与多个JSON value；
- summary required、valid UTF-8、non-empty且不超过上限；
- operation只允许`set`、`delete`；
- path使用canonical RFC 6901 JSON Pointer，非空、唯一、valid UTF-8；
- `set`要求一个valid JSON value；`delete`不得携带value；
- no-op set/delete在持久化前移除；
- 模型输出只提出Delta，Plugin负责严格验证和应用。

每个新segment冻结后不再重写。Model context中的summary使用synthetic assistant message，并带固定前缀说明它是historical data而非新的system instruction。State snapshot和Delta也使用固定、确定性的synthetic assistant JSON message。

含media part的turn可以保留在anchor或recent raw区域，但v0.1不把它发送给摘要模型；选段在第一个media turn前停止。若最旧eligible turn即含media且仍需压缩，返回`ErrContextUncompactable`，不丢弃或只总结其文本子集。

## 7. State snapshot、Delta 与 rollup

Context state是以canonical JSON Pointer为key、JSON value为value的有序materialized map。后续Delta按checkpoint sequence应用：`set`覆盖旧值，`delete`建立删除语义并移除materialized value。

普通segment保存本段summary和validated operations；展示时保留每个冻结summary与Delta，从而只追加新的稳定前缀部分。

当active summary数量达到 `max_summary_chunks` 且仍需继续压缩时：

1. 使用摘要模型把现有summary texts合并为一个rollup summary；
2. Plugin本地应用全部Delta并生成完整state snapshot，不让模型重写事实；
3. 追加rollup checkpoint，声明supersede之前active chain；
4. 后续context只展示rollup summary、snapshot和新的segments。

Rollup不删除旧Entry，只改变最新active chain。它会使anchor之后的cache失效一次，但频率由 `max_summary_chunks` 控制。

## 8. Persistence schema

```go
session.Entry{
    Kind:    "context.compact.checkpoint",
    Version: 1,
    Payload: ...,
}
```

Payload semantic shape：

```json
{
  "sequence": 3,
  "parent_sequence": 2,
  "mode": "segment",
  "policy_digest": "sha256:...",
  "covered_messages": 24,
  "source_digest": "sha256:...",
  "summary": "...",
  "base_revision": 2,
  "revision": 3,
  "operations": [],
  "state_snapshot": null,
  "provider": "provider",
  "model": "model"
}
```

- sequence在同一Session内对本Kind单调递增；
- `parent_sequence=0`开始新chain；普通segment引用当前active checkpoint；
- mode为`segment`或`rollup`；
- segment要求covered_messages递增、保存operations、snapshot为null；
- rollup保持covered_messages不变、operations empty、保存完整snapshot；
- source_digest覆盖到covered_messages为止的canonical raw conversation prefix；
- 不同policy或不匹配当前source prefix的旧chain视为stale，不参与当前结果；
- 当前active chain中结构损坏、revision断裂或摘要非法返回`ErrCorruptCheckpoint`；
- 未知Entry Version返回`ErrUnsupportedCheckpointVersion`。

Store Append失败时本次Compact失败，不使用未持久化摘要。成功checkpoint可在后续失败或进程重启后复用。

## 9. Context、并发与生命周期

- Compactor为每个Session建立Context-aware keyed gate；
- 不同Session并发，同一Session checkpoint Load/Model/Append/结果materialize整体串行；
- 等待gate、Store与Model时观察Context并保留error chain；
- Config/dependencies/fixed prompt在New后immutable；
- 无后台任务，成功New返回nil Cleanup；
- Compactor不Cleanup其Model或Store dependency。

## 10. Errors

```go
ErrInvalidConfig
ErrInvalidRequest
ErrInvalidHistory
ErrUnsupportedCheckpointVersion
ErrCorruptCheckpoint
ErrCompactionFailed
ErrContextUncompactable
```

不使用静默截断或“压缩失败则丢弃旧消息”fallback。Context、Model和Store错误保留`errors.Is`链。

## 11. Manifest 与测试

```toml
manifest_version = 1
name = "context.compact"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖Config、nil dependency、external Contract、ownership、UTF-8/JSON、turn/tool grouping、canonical size、高低水位、anchor/recent、incremental frozen segments、state set/delete/no-op、checkpoint strict decode/version/source/policy、restart reuse、rollup、Model/Store/Context errors、same-session order、cross-session concurrency和race test。
