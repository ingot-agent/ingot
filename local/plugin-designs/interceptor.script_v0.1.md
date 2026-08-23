# `interceptor.script` Plugin v0.1 设计方案

> 状态：Draft  
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
    FailurePolicy  string            `toml:"failure_policy"`
    Environment    map[string]string `toml:"environment"`
}
```

- target：`tool`、`model`、`model-stream`、`agent`；
- executable 必须是 absolute regular file；不使用 PATH 和 shell；
- timeout 默认 10 秒，output 默认 64 KiB；
- failure policy：`closed` 或 `open`，默认 `closed`；
- hook name 全局唯一；
- environment 从空集合开始，只使用显式配置；
- executable、args、environment 在 `New` 时复制并验证。

`open` 只适用于 hook process 自身失败；脚本明确返回 `reject` 时始终拒绝。

## 4. Hook protocol

Plugin 通过 stdin 发送一份 compact JSON，stdout 接受一份 compact JSON。stderr 只用于诊断并受同一输出上限约束。

Before request：

```json
{
  "protocol_version": 1,
  "hook": "audit",
  "target": "tool",
  "phase": "before",
  "request": {}
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

After request 包含 typed response 或规范化 error descriptor；After response 只允许 `continue`。After hook 不能把已经成功的调用改成新 response，但在 closed policy 下 hook failure 可使整个调用返回 error。

Typed request/response 使用稳定的 Plugin-owned JSON projection，不直接依赖 Go 默认 JSON field naming。projection schema 随 hook protocol version 管理并为四种 target分别维护 golden fixtures。Model stream 的 after phase 接收最终 `model.Response`；已经交付的 chunk 不可撤回。

## 5. 执行语义

- 每个 Interceptor 按 `before → next → after` 执行；before reject 不调用 next和 after；
- executable process 继承调用 Context，并增加配置 timeout；
- timeout/cancel 后终止整个 process tree并 wait；
- stdout 必须是单个 JSON object且无额外非空 bytes；
- invalid protocol、oversize、non-zero exit 和 launch failure按 failure policy处理；
- open policy 继续 next，同时通过标准诊断渠道记录 hook failure；SDK 当前没有 logger capability，首版至少在返回链或 interaction 外的实现日志中记录，不能静默；
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

测试覆盖四种 target、export order、before/after trace、reject short-circuit、open/closed failure、timeout/process-tree cleanup、protocol strictness、output limit、Context、stream 已交付边界和 race test。所有测试使用临时 helper executable，不依赖 shell。

待确认：SDK 缺少统一 logger/diagnostic Capability，`failure_policy=open` 的可靠可观测性需要在实现前确定；v0.1 不支持 request rewrite，避免外部 JSON 与 Go typed Contract 产生不完整映射。
