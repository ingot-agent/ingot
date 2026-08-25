# `agent.default` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Exports：`agent.Runtime`

## 1. 定位

`agent.default` 实现一个完整 Agent turn：same-session serialization、历史加载、Prompt render、Model complete/stream、Tool loop、Interaction events和Session append。

它只通过 Runtime chokepoint调用 Model和Tool，不直接持有具体 Provider/Tool；Session storage format由 `session.Store` 所属 Plugin管理，但 Agent拥有其写入的 Entry kind/payload schema。

## 2. Component Contract

```go
type Dependencies struct {
    Model        model.Runtime
    Streaming    sdk.Optional[model.StreamingRuntime]
    Tools        tool.Runtime
    Store        session.Store
    Prompt       prompt.Renderer
    Compactor    sdk.Optional[contextwindow.Compactor]
    Interaction  sdk.Optional[interaction.Channel]
    Interceptors []agent.Interceptor
}

type Exports struct {
    Runtime agent.Runtime
}
```

Generated wiring负责依赖 nil/typed-nil validation；`New` 仍建立 immutable snapshot并验证 Config与可选能力组合。

## 3. Config

```go
type Config struct {
    Provider      string   `toml:"provider"`
    Model         string   `toml:"model"`
    Temperature   *float64 `toml:"temperature"`
    MaxTokens     *int     `toml:"max_tokens"`
    MaxToolRounds int      `toml:"max_tool_rounds"`
    Streaming     bool     `toml:"streaming"`
    ToolErrorMode string   `toml:"tool_error_mode"`
}
```

- Provider/Model 可以为空，由 `model.runtime` default处理；
- temperature若存在必须在 `[0,2]`；具体Provider可进一步收紧；
- max tokens若存在必须 `>0`；
- max tool rounds默认8，必须 `>0`；一次 round是一条含ToolCalls的assistant response及其全部tool results；
- streaming默认false；true时 `Streaming.Valid` 必须为true，否则 `New`失败；
- tool error mode：`result`（默认）或 `fail`；Context cancellation/deadline无论模式都直接失败。

## 4. Agent persistence envelope

Agent写入：

```go
session.Entry{
    Kind:    "agent.message",
    Version: 1,
    Payload: ...,
}
```

Payload exact semantic shape：

```json
{
  "role":"assistant",
  "content":"...",
  "name":"",
  "tool_call_id":"",
  "tool_calls":[
    {"id":"call-1","name":"fs.read","arguments":{}}
  ]
}
```

规则：

- 使用Plugin-owned persistence struct和golden JSON，不直接依赖Go字段默认命名；
- RawMessage在encode/decode时deep-copy且必须valid JSON；
- Load时只消费 `Kind="agent.message"`；其他Kind保留给其他组件并忽略；
- `agent.message`未知Version返回 `ErrUnsupportedEntryVersion`，不静默跳过；
- role和tool-call关联必须通过模型消息校验；corrupt Agent payload返回`ErrCorruptHistory`。

### 4.1 尾部未完成 Tool round

Assistant tool-call message 必须先于对应 Tool result 持久化，因此进程取消、Tool error、达到 round上限或崩溃可能留下**仅位于 Agent history 尾部**的未完成 round。这不是 Store corruption，Agent按以下规则恢复：

- 已存在的 Tool message 必须按 assistant `tool_calls` 顺序构成从0开始的连续前缀，`tool_call_id`逐项匹配；
- 只允许最后一个 assistant tool-call round缺少后缀结果；缺少中间结果后又出现其他 Agent message、未知call id、重复结果或多个未完成round仍返回`ErrCorruptHistory`；
- 下一次`Run`在写入新user message前，为每个缺失call按原顺序 Append一个synthetic RoleTool message，Content固定为`tool error [interrupted]: previous execution was interrupted; result unknown`；
- synthetic result只修复对话关联，不重新执行Tool。尤其不得自动重试可能已经产生副作用但commit status unknown的调用；
- recovery Append沿用正常Context和Store原子语义。中途失败时立即返回；下次Load从已提交的更长前缀继续，不重复已存在结果。

这样正常的尾部中断可以恢复，而真正的历史乱序或关联破坏仍然fail-closed。

## 5. Same-session serialization

`Runtime.Run`首先验证非空SessionID，再以Context-aware keyed gate按Session序列化**整个Interceptor chain和turn terminal**。

```text
Run
→ acquire session gate
→ agent interceptors
→ terminal turn
→ release gate
```

不能只在terminal内部加锁，否则Interceptor before logic仍可能让同一Session并发。不同Session使用不同gate并行；gate registry采用引用计数回收。

SDK所说“按调用顺序串行”以成功获得Runtime入口序号定义。实现为每个Session分配单调ticket，Context取消的等待者被移除；后续ticket继续，不能因为channel竞争产生非确定性插队。

## 6. Turn algorithm

### 6.1 初始化

1. 检查Context、SessionID和Input UTF-8；
2. `Store.Load`并decode Agent history；若存在4.1定义的尾部未完成Tool round，先完成synthetic recovery并加入in-memory history；
3. 构造当前user `model.Message`；
4. 先把user message Append到Session；
5. 调用 `Prompt.Render{SessionID, Input, History}`；
6. snapshot `Tools.Definitions()`并构造初始 `model.Request`；
7. 若可选Compactor存在，在每次Model invocation前调用`Compact{SessionID, Invocation}`，仅以返回的完整Messages替换本次Request.Messages。

User message在模型调用前持久化。之后Prompt/Model失败时，用户输入仍留在Session，便于诊断和重试；重试是一个新turn，不自动删除已提交数据。

Agent始终保留Prompt输出及随后assistant/tool消息组成的完整in-memory message sequence。Compactor输出是单次Model invocation视图，不得写回该原始sequence；工具循环的下一次调用重新从完整sequence构造Request并再次Compact。无论`CompactionResult.Changed`为何值，返回的Messages都是完整replacement。Compactor错误在主模型调用前传播并保留错误链，已经Append的Agent message不回滚。

### 6.2 Model 调用

- streaming=false使用`Model.Complete`；收到每条assistant response后，如果Interaction有效且Content非空，统一Render一次TextEvent；
- streaming=true使用`Streaming.Stream`；每个TextDelta按顺序通过可选Interaction Render为TextEvent，最终不重复Render完整Content；
- Interaction absent时仍可stream并累积，但配置校验可提示这种选择无UI收益；
- Stream handler/Render error立即停止并传播；
- 最终assistant Message完成校验后Append，再加入本轮in-memory messages。

### 6.3 Tool loop

若assistant没有ToolCalls，返回`agent.Result{Output: Message.Content}`。`Result.Output`始终保留给非交互调用方；在官方CLI Graph中assistant文本已经由Agent通过同一个Interaction Channel输出，CLI不得再次渲染Result造成重复。

否则：

1. round计数，超过上限返回`ErrMaxToolRounds`；
2. ToolCalls按模型返回slice顺序串行执行，保持确定性和依赖关系；
3. 每个Call要求ID和Name非空、Arguments valid JSON；
4. 可选Interaction依次Render ToolCallEvent和ToolResultEvent；
5. 调用`Tools.Call(ctx, call)`；
6. 成功结果生成RoleTool Message并Append；
7. 非Context error在mode=result时按以下固定映射转换为tool result；mode=fail时立即返回包装后的原错误，尚未产生的result由下一次Run按4.1恢复；
8. 将assistant和所有tool messages追加到初始rendered messages，再发起下一次Model请求。

`result`模式提供给模型和持久化历史的Content只使用稳定安全文本，不拼接`err.Error()`、Go stack或其他下游诊断：

| Error classification | Content |
|---|---|
| `errors.Is(err, tool.ErrNotFound)` | `tool error [not_found]: requested tool is unavailable` |
| `errors.Is(err, tool.ErrInvalidArguments)` | `tool error [invalid_arguments]: tool arguments were rejected` |
| 其他非Context error | `tool error [execution_failed]: tool execution failed` |

转换后的`tool.Result`正常Render `ToolResultEvent`并Append RoleTool message。原错误不向模型暴露；`fail`模式则通过`Run`错误链交给调用方。无论模式，之前已提交的assistant/tool记录不回滚。

## 7. Interceptor、ownership与错误

Agent Interceptors按MANY stable order组合，第一个最外层。Interceptor short-circuit时不进入turn terminal，也不持久化user message，但仍处于same-session gate内。

所有从Store、Prompt、Model、Tool得到的aggregate在保留前deep-copy；传给下游的Request不复用caller Turn内部mutable data（Turn当前只有string/ID）。

建议sentinel：

```go
ErrInvalidTurn
ErrMaxToolRounds
ErrInvalidModelMessage
ErrUnsupportedEntryVersion
ErrCorruptHistory
```

所有Context、SDK Runtime和Session sentinel错误链保留。

## 8. 并发与生命周期

- 不同Session Turn并发；同Session全chain严格有序；
- Runtime registry/config/dependencies在startup后immutable；
- v0.1不启动后台任务，所有Model/Tool调用属于Run调用栈；
- 成功`New`返回nil Cleanup；
- Runtime不清理其Dependencies，它们由generated wiring各自Cleanup。

## 9. Manifest

```toml
manifest_version = 1
name = "agent.default"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

Agent自身不声明Plugin State；durable data由`session.Store` Plugin拥有。

## 10. 测试与验收

- exactDependencies/Exports/New contract；
- Config与optional streaming组合；
- agent.message v1 golden和corruption/version；
- trailing incomplete Tool round的prefix识别、synthetic recovery、recovery中断续跑和禁止自动重试；
- user→prompt→model→assistant persistence顺序；
- multi-round tool call完整trace；
- ToolCalls顺序、tool error result/fail、max rounds；
- Complete/Stream、chunk顺序、handler/Render error；
- optional Compactor absent/typed-nil、完整Request输入、每轮调用、replacement ownership和错误传播；
- Interaction absent；
- Agent Interceptor outermost/short-circuit；
- same-session ticket order、等待取消、cross-session并发；
- partial failure不回滚已提交Entry；
- aggregate ownership和race test。

未来可增加独立token/turn usage event；v0.1 stream delta仍由Agent通过可选Interaction按顺序渲染，frontend不重复聚合输出。
