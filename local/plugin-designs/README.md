# ingot 官方 Plugin 设计文档

本目录保存官方 Plugin 的独立设计方案。每个文件描述一个 Plugin 的边界、公开 Component Contract、配置、生命周期、并发、错误、持久化或外部资源语义，以及实现验收标准。

## 规范关系

Plugin 设计必须遵循：

1. `../ingot_架构设计_v0.3.md` 中的静态 Component Graph 和生命周期模型；
2. `../ingot_SDK_v0.1_设计方案.md` 及 `../../../sdk/` 中已经实现的公共 Contract；
3. `../ingot.plugin.toml_设计方案_v0.1.md` 中的 Plugin Manifest 与 Component package 约定。

本目录不会重新定义 SDK interface。若 Plugin 需求无法通过现有 SDK 表达，应先提出 SDK Contract 变更，不能在具体实现中通过隐藏的全局注册表或字符串服务定位绕过 Component Graph。

## 官方 Plugin 清单

| Plugin | 状态 | Exports | 主要验证目标 |
|---|---|---|---|
| [`http.default`](./http.default_v0.1.md) | Implemented v0.1 | `httpx.Client` | Context authority、请求不可变、共享连接池 |
| [`filesystem.local`](./filesystem.local_v0.1.md) | Implemented v0.1 | `filesystem.FS` | workspace boundary、安全路径、原子文件操作 |
| [`session.jsonl`](./session.jsonl_v0.1.md) | Implemented v0.1 | `session.Store` | append total order、持久化格式、State compatibility |
| [`tool.shell`](./tool.shell_v0.1.md) | Draft | `[]tool.Tool` | 子进程树、环境隔离、输出与时间边界 |
| [`tool.fs`](./tool.fs_v0.1.md) | Draft | `[]tool.Tool` | Filesystem-to-Tool typed adapter |
| [`tool.ask`](./tool.ask_v0.1.md) | Draft | `[]tool.Tool` | Tool内同步用户交互 |
| [`tool.runtime`](./tool.runtime_v0.1.md) | Draft | `tool.Runtime` | lookup、schema validation、Interceptor chokepoint |
| [`interceptor.approval`](./interceptor.approval_v0.1.md) | Draft | `[]tool.Interceptor` | allow/ask/deny与fail-closed审批 |
| [`interceptor.script`](./interceptor.script_v0.1.md) | Draft | typed Interceptors | 外部策略/审计hook协议与进程回收 |
| [`model.openai-compatible`](./model.openai-compatible_v0.1.md) | Draft | `[]sdk.Named[model.Provider]` | Chat Completions和SSE协议适配 |
| [`model.runtime`](./model.runtime_v0.1.md) | Draft | complete/stream runtimes | Named Provider选择与独立拦截链 |
| [`prompt.default`](./prompt.default_v0.1.md) | Draft | `prompt.Renderer` | Contributor稳定顺序与确定性消息组合 |
| [`agent.default`](./agent.default_v0.1.md) | Draft | `agent.Runtime` | Session序列化、Model/Tool循环和持久化 |
| [`app.cli`](./app.cli_v0.1.md) | Draft / lifecycle gate | `interaction.Channel`、待新增`application.Runner` | Composite frontend和process lifecycle |

共14个Plugin、15个Component；`app.cli`包含`interaction`和`app`两个Component。

## 依赖与建议实施批次

```text
Batch 1  http.default / filesystem.local / session.jsonl
Batch 2  tool.shell / tool.fs / tool.ask / approval / tool.runtime
Batch 3  model.openai-compatible / model.runtime
Batch 4  prompt.default / agent.default
Batch 5  app.cli / interceptor.script hardening
```

`app.cli/app`实现前需要冻结`application.Runner`或等价的显式SDK Contract，使generated main能观察CLI正常结束并执行reverse Cleanup。该问题记录在app.cli设计中，不能通过`os.Exit`或隐藏全局channel绕过。

## 共同实现约定

- 一个 Plugin 对应一个独立 Go Module；canonical Plugin ID 来自其 `go.mod` module path。
- Manifest 显式声明 `[[components]]`，首批三个 Plugin 均只有 `default` Component。
- Component package 提供当前 package 的 `Dependencies`、`Exports` 和精确签名的 `New`。
- `New` 可重复、可并发调用，每次创建独立实例，不使用 package-level mutable singleton。
- Config 只读；需要保留 slice、map、pointer 或 bytes 时先复制。
- Capability 实现默认 concurrent-safe；等待内部顺序控制时也要观察调用 Context。
- 错误通过 `%w` 保留 SDK、Context、`io/fs` 或 `net/http` 的可识别错误链。
- 没有长期任务或实例资源时允许返回 nil Cleanup；有资源时 Cleanup 必须合作式取消并及时返回。
- 测试使用外部 package，包含 interface compile assertion、正常行为、负例、并发和 Context cancellation。

## 文档状态

首批三个 Plugin 已按各自文档实现；其“v0.1 实现决策”记录首版实际选择。其余文档仍为实现前 Draft，标为“待确认”的内容在编码前需要形成明确结论。
