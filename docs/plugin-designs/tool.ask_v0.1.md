# `tool.ask` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`interaction.Channel`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.ask` 允许模型在一个 Agent turn 内同步请求当前Host环境提供文本信息。问题可以是普通文本，也可以携带有序建议选项；Host可以通过UI、CLI、policy或其他设施回答。它是Tool到Interaction Request的typed adapter，不负责审批、多字段表单、长期异步通知或多用户routing。

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
3. 构造稳定identity为`ask_user`的Interaction Request；
4. Request包含一个必填`answer` String Field；有options时按声明顺序转换为Field Options。String Field的Options是建议值，不限制Host返回自由文本；
5. 保留 Context 和 `interaction.ErrUnavailable` 错误链；
6. Response必须包含唯一的`answer` String Value，否则返回`ErrInvalidResponse`；
7. 检查answer UTF-8和长度；
8. 返回`tool.Result{Content: answer}`。

Option的稳定Value与模型提供的label相同；Host选择建议项时返回该Value，提供自由输入时返回原文。Plugin不对answer trim、解释yes/no或添加格式，也不增加额外全局锁。

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

测试覆盖exact Definition、纯文本Request、选项顺序和description传递、自由输入、重复/空label、option数量/字节上限、Response字段和ValueKind校验、answer原样返回、长度/UTF-8、Context cancellation、Unavailable、并发调用以及dependency typed-nil startup validation。

v0.1只返回单个文本结果，不表达多选。多字段表单、secret input和复杂表单校验应由面向相应需求的独立Tool或领域Contract提供。
