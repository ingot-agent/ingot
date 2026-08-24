# `interceptor.script` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：无  
> Exports：Tool、Model、Model Stream、Agent typed Interceptors

## 1. 定位

`interceptor.script` 将用户配置的受信任 executable hook 接入标准 Runtime chokepoint，用于企业策略、审计和外部集成。它直接执行 executable，不通过 shell；Plugin 源码和配置脚本均按受信任代码管理。

v0.1 hook 可以观察请求/结果并在 before 阶段拒绝调用，但不能重写 typed request/response，也不逐 chunk 检查 model stream。

## 2. Component Contract

```go
type Dependencies struct{}

type Exports struct {
    ToolInterceptors        []tool.Interceptor
    ModelInterceptors       []model.Interceptor
    StreamInterceptors      []model.StreamInterceptor
    AgentInterceptors       []agent.Interceptor
}
```

Config 中 hook declaration order 决定同一 target 的 export slice order。

## 3. Config

```go
type Config struct {
    Hooks []Hook `toml:"hooks"`
}

type Hook struct {
    Name           string            `toml:"name"`
    Target         string            `toml:"target"`
    Executable     string            `toml:"executable"`
    Args           []string          `toml:"args"`
    TimeoutSeconds int               `toml:"timeout_seconds"`
    MaxOutputBytes int               `toml:"max_output_bytes"`
    Environment    map[string]string `toml:"environment"`
}
```

- target：`tool`、`model`、`model-stream`、`agent`；
- executable 必须是 absolute regular file；不使用 PATH 和 shell；
- timeout 默认 10 秒且必须为正数，并且换算为 `time.Duration` 时不得溢出；`max_output_bytes`默认64 KiB且必须为正数，分别限制stdout和stderr，每条流最多占用该值；
- hook name 全局唯一；
- environment 从非nil empty集合开始，只使用显式配置，绝不继承父进程环境；key/value不得包含NUL或非法environment name，Windows下key按大小写不敏感规则判重；
- child working directory固定为executable所在目录，不继承Runtime当前工作目录；
- executable、args、environment 在 `New` 时复制并验证。

v0.1 所有 hook failure 都是fail-closed。暂不提供`failure_policy=open`，因为当前SDK没有能够可靠记录“失败但继续”的diagnostic capability；不能用stderr、隐藏全局logger或静默忽略代替公共Contract。

## 4. Hook protocol

Plugin 通过 stdin 发送一份 compact JSON，stdout 接受一份 compact JSON。stderr 只用于诊断并受同一输出上限约束。

每次执行向stdin写入一个UTF-8 compact JSON object并关闭stdin。Before request envelope：

```json
{
  "protocol_version": 1,
  "hook": "audit",
  "target": "tool",
  "phase": "before",
  "request": {"id":"call-1","name":"fs.read","arguments":{}}
}
```

Before response exact shape：

```json
{"protocol_version":1,"action":"continue"}
```

或：

```json
{"protocol_version":1,"action":"reject","message":"policy denied"}
```

Before response只允许上述两个exact shape：`protocol_version`必须为1，`action`为`continue|reject`，reject message必须为非空UTF-8字符串；unknown、duplicate、missing、显式`null`或类型错误字段，以及非UTF-8输入，均为protocol error。

执行`next`后，只要调用Context仍有效，无论成功或业务失败都执行After hook；Before reject和Context cancellation/deadline不执行After。若`next`返回后Context已经取消，即使`next`错误地返回nil error，Interceptor也必须返回或join `ctx.Err()`，不能把已取消调用报告为成功。After request envelope：

```json
{
  "protocol_version":1,
  "hook":"audit",
  "target":"tool",
  "phase":"after",
  "request":{"id":"call-1","name":"fs.read","arguments":{}},
  "outcome":{"response":{"content":"..."},"error":null}
}
```

`outcome.response`和`outcome.error`恰好一个非null；下游返回error时response固定为null，不投影可能同时返回的partial Go value。After response只允许：

```json
{"protocol_version":1,"action":"continue"}
```

After hook只能审计，不能改写已经发生的typed response或error。

### 4.1 四种 target projection

所有object key固定存在，slice为空时编码`[]`而不是`null`；pointer optional field用JSON `null`保留absence。projection使用Plugin-owned struct与golden fixture，不依赖Go默认字段名。

| Target | Request projection | Response projection |
|---|---|---|
| `tool` | `{"id":string,"name":string,"arguments":<JSON value>}` | `{"content":string}` |
| `model` | model request shape | model response shape |
| `model-stream` | 与`model`相同 | 最终model response；不含逐chunk事件 |
| `agent` | `{"session_id":string,"input":string}` | `{"output":string}` |

model request shape：

```json
{
  "provider":"default",
  "model":"gpt-example",
  "messages":[{"role":"user","content":"hello","name":"","tool_call_id":"","tool_calls":[]}],
  "tools":[{"name":"fs.read","description":"...","input_schema":{}}],
  "temperature":null,
  "max_tokens":null,
  "stop":[]
}
```

model response shape：

```json
{
  "message":{"role":"assistant","content":"hi","name":"","tool_call_id":"","tool_calls":[]},
  "finish_reason":"stop",
  "usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},
  "provider":"default",
  "model":"gpt-example"
}
```

嵌套tool call始终使用`{"id":string,"name":string,"arguments":<JSON value>}`。所有投影字符串必须是有效UTF-8；Definition.InputSchema和Call.Arguments必须同时是有效UTF-8和valid JSON value并原样嵌入；Temperature必须是有限数值。投影前做deep copy，hook process无法修改Go aggregate。

### 4.2 Error descriptor

```json
{"kind":"context_canceled","message":"context canceled"}
```

`kind`按`errors.Is`优先级归一化为：`context_canceled`、`deadline_exceeded`、`tool_not_found`、`invalid_arguments`、`provider_not_found`、`model_not_found`、`streaming_unsupported`、`session_not_found`或`other`。`message`是原错误链的最终可读文本，不包含Go stack。Hook是管理员信任边界，管理员必须把该字段可能携带的请求、路径或服务端诊断视为敏感信息。

projection schema 与error kind集合随hook protocol version管理；任何不兼容字段变化必须提升protocol version。Model stream已经交付的chunk不可撤回。

## 5. 执行语义

- 每个 Interceptor 按 `before → next → after` 执行；before reject 不调用 next和 after；
- executable process使用调用Context并增加配置timeout；
- direct exec启动后并发写stdin、drain stdout/stderr并wait，不能因pipe填满死锁；
- Unix将child置于独立process group；Windows以suspended状态创建、在resume前加入`KILL_ON_JOB_CLOSE` Job Object。timeout/cancel先请求合作式终止，随后终止整个process group/Job并wait；containment失败时仍kill root child，返回cleanup error且不得遗留可等待child；
- stdout 必须是单个 JSON object且无额外非空 bytes；
- stdout/stderr分别持续drain到EOF；任一流超限即标记failure但仍完成drain和process回收。成功exit时stderr必须为空，避免产生无法可靠投递的诊断；
- invalid protocol、oversize、non-zero exit、non-empty success stderr、launch failure和process cleanup failure均返回包装`ErrHookFailed`，不得继续或静默；启动后若containment attach失败，必须终止进程并使用完整`exec.Cmd.Wait`等待stdin复制等内部任务退出；
- Before `reject`返回包装`ErrHookRejected`；message可供调用方展示，但不得变成typed response；
- After hook failure返回包装`ErrAfterHookFailed`。此时downstream调用已经发生，Tool、Agent或外部Model请求可能已有副作用；错误必须标记completion/commit status unknown，调用方和其他Interceptor不得因此自动retry；下游成功后的response projection failure同样属于After failure，必须返回原owned response并保留`ErrHookFailed`、`ErrAfterHookFailed`和`ErrCompletionUnknown`错误链；
- After成功时原样返回downstream response/error；After失败且downstream也失败时使用`errors.Join`同时保留两条错误链，downstream成功时返回其owned response并同时返回`ErrAfterHookFailed`；
- 请求 projection 可能包含敏感信息。使用该 Plugin 即表示管理员信任 executable；文档必须明确数据暴露范围；
- Plugin 无长期 worker，所有 child 在调用返回前 wait，成功 `New` 返回 nil Cleanup。

## 6. Manifest、测试与验收

```toml
manifest_version = 1
name = "interceptor.script"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖四种target projection golden、error kind、export order、before/after trace、reject和Context short-circuit、统一fail-closed、after side-effect/unknown-status与双错误join、isolated environment/working directory、stdout/stderr独立limit、non-empty success stderr、timeout/process-tree cleanup、protocol strictness、Context、stream已交付边界和race test。所有测试使用临时helper executable，不依赖shell，并在Windows/Unix验证各自containment primitive。

未来若SDK增加可靠diagnostic Capability，可另行设计显式fail-open模式；v0.1不支持request rewrite，避免外部JSON与Go typed Contract产生不完整映射。
