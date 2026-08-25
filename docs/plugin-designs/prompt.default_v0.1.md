# `prompt.default` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`[]prompt.Contributor`  
> Exports：`prompt.Renderer`

## 1. 定位

`prompt.default` 将静态 system prompt、按 MANY stable order 产生的 Contributor blocks、Session history 和当前用户输入组合为确定性的 `[]model.Message`。

```go
type Dependencies struct {
    Contributors []prompt.Contributor
}

type Exports struct {
    Renderer prompt.Renderer
}
```

它不调用 Model、不裁剪 Session、不执行工具，也不按 tokenizer计算 token budget。上下文预算与压缩由 Prompt render 之后、Model invocation 之前的独立 `contextwindow.Compactor` 负责，不属于 Renderer。

## 2. Config

```go
type Config struct {
    SystemPrompt     string `toml:"system_prompt"`
    MaxBlockBytes    int    `toml:"max_block_bytes"`
    MaxSystemBytes   int    `toml:"max_system_bytes"`
}
```

默认每个 block content 64 KiB、最终 system content 256 KiB。字符串必须 valid UTF-8，限制为正数。`New` 在构造时验证 configured system prompt；其 UTF-8 byte length 已超过 `max_system_bytes` 时直接返回 Config Error。

## 3. Render algorithm

1. deep-copy `Request.History` 和内部 ToolCalls/RawMessage；
2. 按 Contributor MANY顺序逐个调用 `Contribute`；每个 Contributor获得原始 Request 的独立 deep copy，前一个 Contributor 对参数的修改不能影响 caller或后一个 Contributor；
3. 每个 Contributor返回的 Block保持 slice顺序，不并发调用；
4. Block.Name 非空、valid UTF-8、不得含 CR/LF；Content valid UTF-8且不超限；duplicate name允许并保持顺序；
5. 构造 system content；
6. append history；
7. append 当前 `RoleUser` message，Content 为 Request.Input。

System content exact format：

```text
<configured system prompt>

## <block name>
<block content>
```

只在相邻 section都存在时加入两个 newline。所有内容为空时不产生 system message。Block name仅作为展示 heading，不提供安全隔离；Contributor content与用户输入一样可能影响模型。

`max_system_bytes` 限制上述格式化完成后 system message `Content` 的总 UTF-8 byte length，计入 configured system prompt、每个 `## ` heading、block name和所有换行分隔符。实现使用 checked addition或逐段写入前检查，发生上溢或超限时返回 `ErrSystemLimit`，不返回部分结果，也不截断任何 section。`max_block_bytes` 只限制单个 Block.Content，不能替代总限制。

Renderer 不删除或重排 history中的 system/tool消息，不把当前 Input写回 history。Input必须 valid UTF-8，可以为空。

Contributor error立即停止后续 Contributor并原样保留错误链；Context在每次调用前后检查。输出 slice和 nested data归 caller。

## 4. 并发与生命周期

Config和 Contributor collection immutable；不同 Render可并发。单次 Render内按稳定顺序串行调用 Contributors。Contributor自身按 SDK默认并发规则实现 concurrent-safe。

Plugin无资源和后台任务，返回 nil Cleanup。

## 5. Manifest、测试与验收

```toml
manifest_version = 1
name = "prompt.default"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试覆盖 zero Contributor、调用/Block顺序、Contributor参数隔离、exact formatting、empty section、duplicate name、system prompt/heading/separator计入总大小、checked overflow、size/UTF-8、Contributor error/Context、history/input ownership、并发 Render和 race test。

上下文压缩通过独立 `contextwindow.Compactor` 扩展；Renderer继续只负责确定性拼装。v0.1不用字符数假装token数，也不静默截断history或block。
