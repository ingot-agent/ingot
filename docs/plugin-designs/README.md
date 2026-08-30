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
| [`session.jsonl`](./session.jsonl_v0.1.md) | Implemented v0.1 | `session.MutableStore` | append total order、原子标题更新、持久化格式、State compatibility |
| [`tool.shell`](./tool.shell_v0.1.md) | Implemented v0.1 | `[]tool.Tool` | 子进程树、环境隔离、输出与时间边界 |
| [`tool.fs`](./tool.fs_v0.1.md) | Implemented v0.1 | `[]tool.Tool` | Filesystem-to-Tool typed adapter |
| [`tool.ask`](./tool.ask_v0.1.md) | Implemented v0.1 | `[]tool.Tool` | Tool内同步用户交互 |
| [`tool.runtime`](./tool.runtime_v0.1.md) | Implemented v0.1 | `tool.Runtime` | lookup、schema validation、Interceptor chokepoint |
| [`interceptor.approval`](./interceptor.approval_v0.1.md) | Implemented v0.1 | `[]tool.Interceptor` | allow/ask/deny与fail-closed审批 |
| [`interceptor.script`](./interceptor.script_v0.1.md) | Implemented v0.1 | typed Interceptors | 外部策略/审计hook协议与进程回收 |
| [`model.openai-compatible`](./model.openai-compatible_v0.1.md) | Implemented v0.1 | `[]ingotabi.Named[model.Provider]` | Chat Completions和SSE协议适配 |
| [`model.runtime`](./model.runtime_v0.1.md) | Implemented v0.1 | complete/stream runtimes | Named Provider选择与独立拦截链 |
| [`usage.default`](./usage.default_v0.1.md) | Implemented v0.1 | `usage.Counter` | model-aware input token计数、Profile路由、有界缓存 |
| [`prompt.default`](./prompt.default_v0.1.md) | Implemented v0.1 | `prompt.Renderer` | Contributor稳定顺序与确定性消息组合 |
| [`context.compact`](./context.compact_v0.1.md) | Implemented v0.1 | `contextwindow.Compactor` | 非破坏式增量摘要、事实Delta与checkpoint复用 |
| [`agent.default`](./agent.default_v0.1.md) | Implemented v0.1 | `agent.Runtime` | Session序列化、Model/Tool循环和持久化 |
| [`app.cli`](./app.cli_v0.1.md) | Implemented v0.1 | `interaction.Channel` + `appcli.Frontend`（app Component无导出） | TUI/plain双前端、一次性AI会话标题、turn取消与受控进程退出 |

共16个Plugin、17个Component；`app.cli`包含`interaction`和`app`两个Component。

## 依赖与建议实施批次

```text
Batch 1  http.default / filesystem.local / session.jsonl
Batch 2  tool.shell / tool.fs / tool.ask / approval / tool.runtime
Batch 3  model.openai-compatible / model.runtime / usage.default
Batch 4  prompt.default / context.compact / agent.default
Batch 5  app.cli / interceptor.script hardening
```

`app.cli/app` 遵循顶层普通 Component 生命周期：`New` 启动
instance-owned 后台 loop 并及时返回，Cleanup 取消并 join。frontend 通过 ingot
ABI 的 `lifecycle.Controller.RequestShutdown` 向 generated main 报告结束意图，
调用元数据通过 `invocation.Invocation` 读取；二者均为显式 host Dependencies。
Component 不新增 Builder root 特例，不调用 `os.Exit`，也不使用隐藏全局
channel。官方 Plugin 的 Agent Contract 统一依赖 SDK v0.1.6，Component ABI 与
host Contract 依赖 ingot ABI v0.1.0。

`app.cli/interaction`按进程参数提供两种前端：`chat`为全屏TUI（bubletea v2，markdown transcript、tool block、会话侧栏、Ask选项面板、turn取消），`chat --plain`为可取消行输入+纯文本输出（pipes/重定向）。

## 共同实现约定

- 一个 Plugin 对应一个独立 Go Module；canonical Plugin ID 来自其 `go.mod` module path。
- Manifest 显式声明 `[[components]]`；除`app.cli`包含两个Component外，其余14个Plugin均只有`default` Component。
- Component package 提供当前 package 的 `Dependencies`、`Exports` 和精确签名的 `New`。
- `New` 可重复、可并发调用，每次创建独立实例，不使用 package-level mutable singleton。
- Config 只读；需要保留 slice、map、pointer 或 bytes 时先复制。
- Capability 实现默认 concurrent-safe；等待内部顺序控制时也要观察调用 Context。
- 错误通过 `%w` 保留 SDK、Context、`io/fs` 或 `net/http` 的可识别错误链。
- 没有长期任务或实例资源时允许返回 nil Cleanup；有资源时 Cleanup 必须合作式取消并及时返回。
- 测试使用外部 package，包含 interface compile assertion、正常行为、负例、并发和 Context cancellation。

## 文档状态

16个官方Plugin均已有v0.1实现；文档中的“v0.1实现决策”记录首版实际选择。后续变更继续以顶层架构和已经实现的SDK Contract为准。
