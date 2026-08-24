# `app.cli` Composite Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Components：`interaction`、`app`

## 1. 定位

`app.cli` 是普通Composite Plugin：`interaction` Component提供单一terminal interaction scope；`app` Component运行Session-aware CLI loop并调用`agent.Runtime`。两个Component共享一个root Config，但位于Component Graph不同拓扑位置。

```text
app.cli/interaction --interaction.Channel--> tool.ask
app.cli/interaction --interaction.Channel--> interceptor.approval
app.cli/interaction --interaction.Channel--> agent.default
app.cli/interaction --interaction.Channel--> app.cli/app

agent.default ------agent.Runtime---------> app.cli/app
session.jsonl ------session.Store----------> app.cli/app
```

## 2. Root Config

```go
type Config struct {
    Interaction InteractionConfig `toml:"interaction"`
    App         AppConfig         `toml:"app"`
}

type InteractionConfig struct {
    InputPrompt  string `toml:"input_prompt"`
    AskPrompt    string `toml:"ask_prompt"`
    Color        string `toml:"color"`
    MaxLineBytes int    `toml:"max_line_bytes"`
}

type AppConfig struct {
    InitialSessionTitle string `toml:"initial_session_title"`
    ShowBanner          bool   `toml:"show_banner"`
}
```

默认prompt为`"> "`和`"? "`，color为`auto|always|never`中的`auto`，line limit 64KiB。Config不保存当前Session或其他runtime mutable state。

## 3. `interaction` Component

```go
package interactioncomponent

type Dependencies struct{}

type Exports struct {
    Channel interaction.Channel
}

func New(
    ctx context.Context,
    cfg root.Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

### 3.1 Channel semantics

- `Render` concurrent-safe，通过output mutex保证每个Event完整写入，不交错半行；
- `Ask`和`ReadLine`共享一个Context-aware input gate，严格串行；
- 所有`AskRequest`（包括无Options的纯文本提问）在输出前验证Prompt、Options和UTF-8；`AskRequest.Options` 非空时按顺序显示编号、label 和可选 description；`AllowTextInput` 为 true 时追加“Other”入口，选择该入口后的第二次读取仍属于同一次持锁的 Ask，空白自由输入继续提示而不作为有效回答返回；
- 选择预设项返回其 label，自由输入返回原文；没有 options 时保持普通文本询问；
- 等gate和等用户输入都观察Context；
- TextEvent写普通输出，StatusEvent使用可降级状态行，ErrorEvent写stderr，Tool events使用稳定人类可读格式；
- 非TTY时关闭color和in-place status，只输出plain text；
- input超过limit时丢弃到line boundary并返回`ErrInputLimit`；
- EOF返回可识别`io.EOF`；
- 不关闭并非自己拥有的stdin/stdout/stderr。

标准`bufio.Reader.ReadString`无法被Context可靠取消。Production实现必须使用platform terminal driver：Unix采用poll/select或可取消fd读取；Windows Console采用handle等待，重定向pipe必须先使用`PeekNamedPipe`确认有数据再执行同步Read，不能把pipe handle的signaled状态直接等同于可安全读取；不得用无法join的永久blocked goroutine伪装取消。

### 3.2 Lifecycle

`New`检测TTY、初始化terminal mode和内部同步结构，不读取第一行并及时返回。同一进程的标准stdin/stdout/stderr是独占物理资源，只允许一个`app.cli/interaction`实例持有；第二个实例在startup返回`ErrTerminalInUse`，Cleanup完成后释放租约。`color=auto`在Windows只在stdout Console已启用Virtual Terminal Processing时输出ANSI颜色。

Cleanup先关闭新调用入口并取消pending input，再以cleanup Context等待全部active input调用退出，最后恢复terminal mode并释放终端租约；超时返回Context错误且不能提前把仍被旧实例使用的租约交给新实例。没有可靠可取消input adapter的平台必须在startup fail-closed或声明不支持，不能违反Channel Contract。

## 4. `app` Component

`app` 是 graph leaf，不向其他 Component 导出 Capability：

```go
type Dependencies struct {
    Agent       agent.Runtime
    Interaction interaction.Channel
    Store       session.Store
}

type Exports struct{}

func New(
    ctx context.Context,
    cfg root.Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

CLI commands首版固定为：

| Command | 行为 |
|---|---|
| `/new [title]` | Create并切换Session |
| `/use <id>` | 验证Load后切换Session |
| `/list` | List并渲染Session摘要 |
| `/help` | 展示命令 |
| `/exit` | 正常停止当前CLI frontend loop，不终止整个进程 |

非`/`输入调用`Agent.Run`。在官方Graph中`agent.default`获得同一个Interaction Channel，并负责渲染assistant文本、stream delta和Tool events；App不得再次渲染`agent.Result.Output`造成重复。`Result.Output`只用于无Interaction的其他Agent consumer。没有current Session时首次输入前创建一个Session，CreatedAt使用注入clock的UTC now。

App loop本身串行处理terminal输入；Agent运行期间不读取下一条顶层命令，因此`tool.ask`/approval可以通过同一个Channel取得input gate并读取用户响应。

## 5. 普通 Component 生命周期

顶层架构已规定 UI、TUI、server、watcher 与 daemon 统一使用普通 `New`/`Cleanup` 生命周期。`app` Component 不定义全局 Entry/Runner，也不要求 graph 中只能存在一个 frontend：

```text
New
→ validate Config and Dependencies
→ derive instance-owned run context
→ start CLI loop in one owned goroutine
→ return promptly

Cleanup
→ cancel instance run context
→ wait for CLI loop to finish
→ return stored fatal loop error, if any
```

`New`只同步返回Config、Dependency和有界初始化错误；不得读取第一条输入或把loop阻塞在constructor。goroutine、current Session和loop result均属于该Component instance，不能使用package-level mutable singleton。CLI、Web、TUI等不同frontend实例可以并存；两个`app.cli`不能同时争用同一套进程标准终端，这属于显式物理资源排他而非全局Entry限制。

`app`依赖`interaction` Component，因此generated reverse order会先Cleanup app loop，再Cleanup interaction terminal adapter。Cleanup等待loop时观察自己的cleanup Context；超时不得遗留Plugin-owned goroutine。Component不得调用`os.Exit`、发送进程信号、取消parent Context或使用隐藏全局channel影响进程生命周期。

## 6. Loop结束与错误语义

- `/exit`和stdin EOF正常停止当前CLI loop并记录nil result；Component保持已构造但inactive，直到进程随后执行Cleanup；
- `/exit`不等于退出整个Runtime。CLI、TUI、Web、server等普通Component可以同时存在，任一frontend结束不能替其他Component决定process lifetime；
- process Context取消时，pending input和Agent调用观察派生Context并结束；由generated main按正常路径执行reverse Cleanup；
- 单条命令、Store或Agent业务错误添加command/session上下文，通过`interaction.ErrorEvent`渲染后继续下一条输入；
- terminal adapter fatal error或无法继续的内部错误best-effort渲染后停止loop，并保存在instance中；后续Cleanup返回该错误。顶层v0.1没有后台任务错误上报Capability，因此它不会反向取消整个进程；
- Cleanup主动取消造成的`context.Canceled`/`DeadlineExceeded`不作为loop failure重复报告；取消前已记录的独立fatal error仍返回；
- current Session只保存在app instance内存中，不写Plugin State；Session数据本身由Store持久化。

## 7. Manifest

```toml
manifest_version = 1
name = "app.cli"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "interaction"
package = "./interaction"

[[components]]
name = "app"
package = "./app"
```

Plugin不声明State；两个Component共享root Config。

## 8. 测试与验收

### Interaction

- Render并发不交错、event格式golden、TTY/plain降级；
- 纯文本与选项 Ask统一校验、自由输入入口及空白重试、Ask/ReadLine serialization和等待取消；
- input EOF、limit、invalid UTF-8；
- terminal独占租约、Windows VT color判断、terminal mode restore和Context-aware active input join；
- platform adapter conformance与race test。

### App

- 首次普通输入时自动Create Session；
- new/use/list/help/exit；
- Agent input/result/error；
- Agent运行中Ask复用Channel无deadlock；
- `New`及时返回且每个instance只有一个owned loop；
- `/exit`和EOF只停止当前frontend、不取消parent process Context；
- process Context cancel、Cleanup cancel/join和fatal loop error回收；
- 多个app/frontend instance可以并存且状态隔离；
- fake clock、Store、Agent、Channel的deterministic transcript tests。

验收以顶层 `New`/`Cleanup` lifecycle conformance为准，不新增SDK application Contract或Builder root-consumer特例。
