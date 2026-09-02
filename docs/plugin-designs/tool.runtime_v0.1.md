# `tool.runtime` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`[]tool.Tool`、`[]tool.Interceptor`  
> Exports：`tool.Runtime`

## 1. 定位

`tool.runtime` 是所有 Tool 调用的标准 chokepoint，集中完成 definition snapshot、名称校验、lookup、JSON Schema validation、Interceptor composition、Invoke 和 Result normalization。

```go
type Dependencies struct {
    Tools        []tool.Tool
    Interceptors []tool.Interceptor
}

type Exports struct {
    Runtime tool.Runtime
}
```

## 2. Config

```go
type Config struct {
    MaxArgumentsBytes  int `toml:"max_arguments_bytes"`
    MaxTextBytes       int `toml:"max_text_bytes"`
    MaxInlinePartBytes int `toml:"max_inline_part_bytes"`
    MaxInlineBytes     int `toml:"max_inline_bytes"`
}
```

默认参数1 MiB、text总量4 MiB、单个inline media 16 MiB、inline media总量32 MiB，且必须为正数。这是Runtime的内存/边界保护，不替代具体Tool的领域限制；URI和Asset Reference不重复计算外部资源bytes。

## 3. Startup validation

`New` 按 MANY stable order 遍历 Tools：

1. 拒绝 nil/typed-nil Tool；
2. 每个 Tool 的 `Definition()` 恰好调用一次并 snapshot；
3. Name 非空、满足 `[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*` 且全局唯一；
4. Description 非空且为 valid UTF-8；
5. InputSchema 非空、valid JSON、是 object schema，并可由选定 validator 编译；
6. 深拷贝 `json.RawMessage`，不保留 Tool 返回的 mutable bytes；
7. 按输入顺序保存 Definitions，registry lookup 不改变公开顺序；
8. 按 MANY 顺序组合 Interceptors，第一个是最外层。

任一错误使 `New` 失败，不创建部分 Runtime。v0.1 JSON Schema dialect固定为 Draft 2020-12；schema 未声明 `$schema` 时按该 dialect 解释。

## 4. `Definitions`

- 返回 startup snapshot 的新 slice；
- 每个 `InputSchema` 是独立 copy；
- caller 修改返回结果不影响 Runtime；
- 顺序精确等于 Tool MANY aggregation order；
- 不在调用时重新执行 Tool.Definition。

## 5. `Call`

固定处理顺序：

```text
Context/size/JSON syntax
→ lookup
→ schema validation
→ interceptor chain
→ selected Tool.Invoke
→ result normalization
```

因此 Interceptor 只接收已经找到且参数满足 schema 的 Call。v0.1 将 validated `tool.Call` 视为 immutable execution request；Approval 和审计可以信任 `Call.Name` 对应已注册 Tool，且实际执行不会换成另一个 Call。

规则：

- unknown name 返回包装 `tool.ErrNotFound`；
- invalid JSON、超限或 schema mismatch 返回包装 `tool.ErrInvalidArguments`；
- `Call.ID` 是 opaque correlation value，允许为空，但进入 chain 后不得修改；
- Runtime 在进入 chain 前复制整个 `Call` 的 `Arguments`；v0.1 Interceptor 不得修改 `Call.ID`、`Call.Name` 或 `Call.Arguments`，Runtime 在 terminal 前再次验证 schema，并在检测到 mutation 时返回 `ErrCallMutation`；
- `tool.ErrNotFound`与`tool.ErrInvalidArguments`只允许在`Tool.Invoke` dispatch boundary之前返回；lookup/JSON/schema rejection因此可被Agent安全转换为synthetic Result；
- Tool或Interceptor在dispatch之后返回上述reserved sentinel时，Runtime改为不暴露该sentinel的`ErrPostDispatchRejection`，避免上层误判为definitely-not-executed；其他Tool/Interceptor error保留链；
- Result.Content必须通过`content.Validate`；text总量、每个inline media及inline media总量分别不得超过对应限制，否则返回Runtime error；
- Runtime 不把 Tool error转换成成功 Result；只有`agent.default`可按明确的pre-dispatch sentinel生成synthetic Result；
- 不 recover Plugin panic；panic 是实现缺陷，应由进程级诊断暴露。

## 6. 并发、生命周期和错误

Runtime startup 后 registry、definition snapshot 和 chain 都 immutable；`Definitions` 与 `Call` concurrent-safe。不同调用不共享 mutable arguments/result buffer。

Plugin 无后台任务和外部资源，成功 `New` 返回 nil Cleanup。

建议额外 sentinel：`ErrInvalidDefinition`、`ErrDuplicateTool`、`ErrInvalidResult`、`ErrPostDispatchRejection`。它们不替代 SDK 的 `ErrNotFound` 和 `ErrInvalidArguments`。

## 7. Manifest、测试与验收

```toml
manifest_version = 1
name = "tool.runtime"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

测试必须覆盖：

- empty tool collection 合法；
- duplicate/invalid name、invalid schema、typed nil；
- Definitions 顺序与深拷贝；
- lookup 和 schema error sentinel；
- first Interceptor outermost、short-circuit、error propagation；
- Interceptor 只能在 validation 后运行；
- arguments/result ownership、多模态Content validation、text/inline part/inline total size；
- 多 Tool 并发和 race test。

待确认：选用的 Draft 2020-12 validator library及其格式扩展策略；实现必须把 validator 版本作为 Plugin module dependency 固定。
