# `tool.ask` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`interaction.Channel`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.ask` 允许模型在一个 Agent turn 内同步向当前 logical interaction scope 询问信息。问题可以是普通文本，也可以携带有序预设选项；选项模式始终保留一项自由输入入口。它是 Tool 到 frontend 的 typed adapter，不负责审批、多字段表单、长期异步通知或多用户 routing。

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
    MaxOptions       int `toml:"max_options"`
    MaxOptionsBytes  int `toml:"max_options_bytes"`
}
```

prompt、response 和全部 option 文本的默认上限均为 16 KiB；option 数量默认上限为 8。显式配置都必须为正数。

Input Schema：

```json
{
  "type":"object",
  "additionalProperties":false,
  "required":["prompt"],
  "properties":{
    "prompt":{"type":"string","minLength":1},
    "options":{
      "type":"array",
      "minItems":1,
      "items":{
        "type":"object",
        "additionalProperties":false,
        "required":["label"],
        "properties":{
          "label":{"type":"string","minLength":1},
          "description":{"type":"string"}
        }
      }
    }
  }
}
```

`options` 可省略以兼容普通文本问答。提供时必须至少有一项，label 必须是非空、唯一的 UTF-8 字符串，description 可省略。Tool 不允许模型关闭自由输入入口。

## 3. 调用语义

1. decode 并复制 prompt 与 options；
2. 检查 UTF-8、label 唯一性、option 数量与总字节数；
3. 没有 options 时调用普通文本 `AskRequest`；
4. 有 options 时按声明顺序填入 `AskRequest.Options`，并设置 `AllowTextInput: true`；
5. 保留 Context 和 `interaction.ErrUnavailable` 错误链；
6. 检查 response UTF-8 和长度；
7. 返回 `tool.Result{Content: response.Text}`。

选择预设项时，Channel 返回对应 label；选择自由输入时返回用户原文。Plugin 不对 response trim、解释 yes/no 或添加格式。Channel 自己负责 Ask/ReadLine serialization，因此 Tool 不增加额外全局锁。

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

测试覆盖 exact Definition、纯文本兼容、选项顺序和 description 传递、强制自由输入、重复/空 label、option 数量/字节上限、response 原样返回、长度/UTF-8、Context cancellation、Unavailable、并发调用以及 dependency typed-nil startup validation。

v0.1 只返回单个文本结果，不表达多选。多字段表单、secret input 和复杂表单校验应通过新的 Interaction Contract 演进。
