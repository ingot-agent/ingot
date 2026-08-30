# `interceptor.approval` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`ingotabi.Optional[interaction.Channel]`
> Exports：`[]tool.Interceptor`

## 1. 定位

`interceptor.approval` 在 Tool chokepoint 根据 Runtime Config 决定 allow、deny 或通过当前 Interaction Channel 请求Host环境作出决定。Host可以使用UI、CLI、policy或其他设施回答。它是安全策略层，不执行 Tool、不读取 shell/filesystem 实现，也不绕过后续 Interceptor。

```go
type Dependencies struct {
    Interaction ingotabi.Optional[interaction.Channel]
}

type Exports struct {
    Interceptors []tool.Interceptor
}
```

v0.1 导出一个 Interceptor。

## 2. Config

```go
type Config struct {
    DefaultAction  string `toml:"default_action"`
    ArgumentDisplay string `toml:"argument_display"`
    MaxDisplayBytes int `toml:"max_display_bytes"`
    Rules []Rule `toml:"rules"`
}

type Rule struct {
    Tool   string `toml:"tool"`
    Action string `toml:"action"`
}
```

- action 为 `allow`、`ask`、`deny`；默认 `ask`；
- rule 只做 exact tool-name match，v0.1 不支持 glob/regex；
- Tool name 不重复；匹配 rule 优先，否则使用 default；
- argument display 为 `full` 或 `name-only`，默认 `full`；
- max display 默认 4096 bytes，超出时截断并明确标记；
- ask policy 在 Interaction absent/typed-nil 时 fail-closed，不退化为 allow。

Config 内的 allow/deny 是用户明确 Runtime policy。Plugin 不根据 Tool 名称自行推断“危险等级”。未来若需要风险 metadata，应扩展 Tool Definition Contract。

## 3. Interceptor 语义

```text
allow → next(ctx, call)
deny  → ErrApprovalDenied，不调用 next
ask   → Interaction.Request → allow/deny
```

Request使用稳定identity `tool_approval`，包含一个必填的`decision` Choice Field，协议值为`allow`和`deny`，展示label为`Yes`和`No`。Request Description至少包含Tool name、Call ID和按配置展示的Arguments。Arguments使用valid JSON compact form，不对key重排作安全承诺。含secret的Tool不应把secret放入模型可见arguments；v0.1没有通用schema-level redaction metadata。

Response必须包含唯一的`decision` String Value。值`allow`执行next，值`deny`明确拒绝；缺失、重复、错误ValueKind或未知值再次请求，最多3次，超过次数返回deny。

Context取消、Interaction error和unavailable都保留错误链且不调用next。Host明确返回`deny`时返回`ErrApprovalDenied`。

Interceptor自身immutable、concurrent-safe；并发Request的调度由Channel implementation负责。成功`New`返回nil Cleanup。

## 4. Manifest、测试与验收

```toml
manifest_version = 1
name = "interceptor.approval"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖rule/default resolution、allow/deny short-circuit、结构化Request及稳定协议值、invalid response retry、missing Channel fail-closed、Context、argument truncation、错误链、并发Request，以及Interceptor在MANY中保持声明顺序。

待确认：是否需要 schema 驱动的 secret redaction 和 risk metadata；在 SDK 支持前，不使用脆弱的 key-name猜测进行自动脱敏。
