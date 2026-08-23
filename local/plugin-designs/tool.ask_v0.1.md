# `tool.ask` Plugin v0.1 设计方案

> 状态：Draft  
> Dependencies：`interaction.Channel`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.ask` 允许模型在一个 Agent turn 内同步向当前 logical interaction scope 询问文本信息。它是 Tool 到 frontend 的 typed adapter，不负责审批、表单、长期异步通知或多用户 routing。

```go
type Dependencies struct {
    Interaction interaction.Channel
}

type Exports struct {
    Tools []tool.Tool
}
```

v0.1 导出一个稳定名称 `ask_user` 的 Tool。

## 2. Config 与 Definition

```go
type Config struct {
    MaxPromptBytes   int `toml:"max_prompt_bytes"`
    MaxResponseBytes int `toml:"max_response_bytes"`
}
```

默认均为 16 KiB，必须为正数。

Input Schema：

```json
{
  "type":"object",
  "additionalProperties":false,
  "required":["prompt"],
  "properties":{"prompt":{"type":"string","minLength":1}}
}
```

## 3. 调用语义

1. decode 并复制 prompt；
2. 检查 UTF-8 和长度；
3. 调用 `Interaction.Ask(ctx, interaction.AskRequest{Prompt: prompt})`；
4. 保留 Context 和 `interaction.ErrUnavailable` 错误链；
5. 检查 response UTF-8 和长度；
6. 返回 `tool.Result{Content: response.Text}`。

Plugin 不对 response trim、解释 yes/no 或添加格式；用户原始文本交给模型。Channel 自己负责 Ask/ReadLine serialization，因此 Tool 不增加额外全局锁。

`New` 只校验 Config 和非 nil dependency，无资源、无后台任务，返回 nil Cleanup。Tool 可被多个 Session 并发调用；实际交互顺序由每个 Channel Contract 决定。

## 4. Manifest、测试与验收

```toml
manifest_version = 1
name = "tool.ask"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖 exact Definition、prompt 原样传递、response 原样返回、长度/UTF-8、Context cancellation、Unavailable、并发调用以及 dependency typed-nil startup validation。

v0.1 明确只支持单个文本字段。结构化选择、多字段表单和 secret input 应通过新的 Interaction Contract 演进。
