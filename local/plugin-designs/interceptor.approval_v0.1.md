# `interceptor.approval` Plugin v0.1 设计方案

> 状态：Draft  
> Dependencies：`sdk.Optional[interaction.Channel]`  
> Exports：`[]tool.Interceptor`

## 1. 定位

`interceptor.approval` 在 Tool chokepoint 根据 Runtime Config 决定 allow、deny 或通过当前 Interaction Channel 询问用户。它是安全策略层，不执行 Tool、不读取 shell/filesystem 实现，也不绕过后续 Interceptor。

```go
type Dependencies struct {
    Interaction sdk.Optional[interaction.Channel]
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
ask   → Interaction.Ask → allow/deny
```

Ask prompt 至少包含 Tool name、Call ID 和按配置展示的 Arguments。Arguments 使用 valid JSON compact form，不对 key 重排作安全承诺。含 secret 的 Tool 不应把 secret 放入模型可见 arguments；v0.1 没有通用 schema-level redaction metadata。

接受的响应在 trim + ASCII case-fold 后为：

- allow：`y`、`yes`；
- deny：`n`、`no`；
- 其他内容再次询问，最多 3 次；超过次数返回 deny。

Context 取消、Interaction error 和 unavailable 都保留错误链且不调用 next。明确用户拒绝返回 `ErrApprovalDenied`。

多个并发 ask 由 Channel Contract 串行；Interceptor 自身 immutable、concurrent-safe。成功 `New` 返回 nil Cleanup。

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

测试覆盖 rule/default resolution、allow/deny short-circuit、ask yes/no/retry、missing Channel fail-closed、Context、argument truncation、错误链、并发 Ask，以及 Interceptor 在 MANY 中保持声明顺序。

待确认：是否需要 schema 驱动的 secret redaction 和 risk metadata；在 SDK 支持前，不使用脆弱的 key-name猜测进行自动脱敏。
