# `app.cli` Composite Plugin v0.1 设计方案

> 状态：Implemented v0.1（TUI/plain 双前端）
> Components：`interaction`、`app`

## 1. 定位

`app.cli` 是普通 Composite Plugin：`interaction` Component提供terminal interaction scope与本地前端传输；`app` Component运行Session-aware CLI loop并调用`agent.Runtime`。两个Component共享一个root Config，但位于Component Graph不同拓扑位置。

```text
app.cli/interaction --interaction.Channel--> tool.ask
app.cli/interaction --interaction.Channel--> interceptor.approval
app.cli/interaction --interaction.Channel--> agent.default
app.cli/interaction --appcli.Frontend-----> app.cli/app
app.cli/interaction --interaction.Channel----> app.cli/app

agent.default ------agent.Runtime---------> app.cli/app
agent.default ------agent.History----------> app.cli/app
model.runtime ------model.Runtime----------> app.cli/app
session.jsonl ------session.MutableStore----> app.cli/app
```

process 级调用元数据（`application.Process`）由 generated main 通过 Context 注入，不经过 Component Graph。

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
    TitleProvider       string `toml:"title_provider"`
    TitleModel          string `toml:"title_model"`
}
```

默认prompt为`"> "`和`"? "`，color为`auto|always|never`中的`auto`，line limit 64KiB。`title_provider`和`title_model`为空时复用`model.runtime`的默认选择；可显式指向更便宜的标题模型。Config不保存当前Session或其他runtime mutable state。

## 3. `interaction` Component

```go
package interactioncomponent

type Dependencies struct{}

type Exports struct {
    Channel  interaction.Channel
    Frontend appcli.Frontend
}

func New(
    ctx context.Context,
    cfg root.Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

### 3.1 Frontend 契约（app.cli 本地传输）

`appcli.Frontend` 是 app.cli 内部的本地传输接口，**不是** SDK `interaction.Channel` 契约的一部分，不要求插件或其他 frontend 实现：

```go
type Frontend interface {
    LineInput
    Sync(context.Context, SessionView) error          // 整体替换可见会话状态
    StartTurn(context.Context, string) error          // 开始渲染一次 turn
    FinishTurn(context.Context, TurnState) error      // 记录 turn 结束状态
    Interrupts() <-chan Interrupt                     // turn 进行中的用户打断
}
```

`SessionView` 是调用者拥有的快照（`Current`、`Sessions`、`Messages`）；Frontend 在 `Sync` 返回后必须复制所有可变值。`Interrupt` 只有两种：`InterruptCancel`（取消当前 turn，前端继续运行）与 `InterruptExit`（取消当前 turn 并请求正常进程退出）。

### 3.2 前端模式选择

`New` 从 Context 取 `application.Process`，然后按下述优先级选择实现：

1. `process.Check()` 为 true（`--ingot-check`）：构造**校验模式**实例——`Channel`/`Frontend` 都指向一个丢弃 stdout/stderr 的 line channel，不读终端、不启动 TUI，Cleanup 只做幂等释放。这样 graph startup validation 可以完整构造并清理 interaction scope；
2. `appcli.ParseArguments(process.Arguments())`：
   - `["chat"]` → **ModeTUI**（全屏终端前端）；
   - `["chat", "--plain"]` → **ModePlain**（可取消行输入 + 纯文本输出）；
   - 其他参数 → `ErrInvalidArguments`（usage: `ingot chat [--plain]`）。

### 3.3 ModePlain

普通行式前端：`Ask`/`ReadLine` 通过 Context-aware input gate 严格串行；UTF-8 与 `max_line_bytes` 校验、EOF 语义、Options 编号选择与自由输入重试均同 v0.1（plain 模式固定无色输出，color 配置只影响 TUI）。

`Frontend` 由同一 `channel` 实例承担：

- `Sync`/`StartTurn`/`FinishTurn` 只校验 Context，不改变输出（纯文本无需状态同步）；
- `Interrupts()` 返回 nil——plain 模式下 turn 运行期间用户无法打断，只能随 process Context 取消。

### 3.4 ModeTUI

全屏 bubletea v2 前端，需要 terminal stdin 与 stdout，否则启动失败返回 `ErrTerminalRequired`（pipe/重定向请使用 plain 模式）；同一进程只能有一个实例持有终端，第二个实例返回 `ErrTerminalInUse`。

TUI 布局：header（当前 Session / working 状态）、transcript viewport + composer、footer（状态与按键提示）。宽度 ≥100 时显示固定侧栏会话列表，窄屏退化为 `Ctrl+O` 浮层。终端可输出颜色时以背景色适配（`tea.RequestBackgroundColor`）。

Transcript 渲染：

- assistant 消息按流式 delta 聚合到同一 block，使用 GFM markdown 渲染（标题、列表、引用、围栏代码块、行内代码、强调、链接显示 label+destination、图片折叠为 `[label]`）；
- tool 调用以独立 block 展示 name/id、arguments（截断至 4KiB）与按 call ID 配对的 result；
- 渲染以 30fps 节流（`markdownFrame`），用户上翻后停止自动跟随。

按键：

| 按键 | 行为 |
|---|---|
| `Enter` | 发送（`busy` 时不响应） |
| `Shift+Enter` / `Alt+Enter` / `Ctrl+J` | 输入框换行 |
| `Ctrl+N` | `/new` |
| `Ctrl+O` | 打开/关闭会话侧栏，`Tab` 在输入与侧栏间切换焦点 |
| `↑/↓` 或 `j/k` | 侧栏选择；`Enter` 执行 `/use <id>` |
| `PgUp/PgDn/Home/End` | 滚动 transcript |
| `Ctrl+C` | 输入框为空时 `/exit`，否则清空输入；turn 进行中 = 取消当前 turn |
| `Ctrl+Q` | 退出（turn 进行中先取消再退出） |
| `F1` | 帮助浮层，`Esc`/`q` 关闭 |

Ask 渲染：选项列表（`›` 标记 + 编号），`↑/↓` 或 `j/k` 或 `1-9` 选择，`Enter` 确认；`AllowTextInput` 时追加 `Other…` 入口，选择后的输入仍属于同一次持锁 Ask，空输入继续提示；`Esc`/`Ctrl+C` 取消当前 turn，`Ctrl+Q` 退出。

生命周期：`New` 启动 program goroutine、等待 first frame `ready` 后返回；`Render`/`Sync`/`StartTurn`/`FinishTurn` 通过 `program.Send` 投递并等待 ack（观察调用 Context 与 program done）；`ReadLine`/`Ask` 复用 input gate 串行化。Cleanup：取消 instance context → program 结束 → 释放终端租约 → 返回 program fatal error；非取消类 fatal error 通过 `process.Shutdown` 上报。

### 3.5 Channel 语义（两种模式共同）

- `Render` concurrent-safe，通过output mutex保证每个Event完整写入，不交错半行；
- `Ask`严格串行，通过Context-aware input gate；行输入是frontend-local的`appcli.LineInput`能力，由同一实现层gate与`Ask`串行，但不属于SDK `interaction.Channel`契约；
- 所有`AskRequest`（包括无Options的纯文本提问）在输出前验证Prompt、Options和UTF-8；`AskRequest.Options` 非空时按顺序显示编号、label 和可选 description；`AllowTextInput` 为 true 时追加“Other”入口，选择该入口后的第二次读取仍属于同一次持锁的 Ask，空白自由输入继续提示而不作为有效回答返回；
- 选择预设项返回其 label，自由输入返回原文；没有 options 时保持普通文本询问；
- 等gate和等用户输入都观察Context；
- TextEvent写普通输出，StatusEvent使用可降级状态行，ErrorEvent写stderr，Tool events使用稳定人类可读格式；
- 非TTY时关闭color和in-place status，只输出plain text；
- input超过limit时丢弃到line boundary并返回`ErrInputLimit`；
- EOF返回可识别`io.EOF`；
- 不关闭并非自己拥有的stdin/stdout/stderr。

标准`bufio.Reader.ReadString`无法被Context可靠取消。Production实现必须使用platform terminal driver：Unix采用poll/select或可取消fd读取；Windows Console采用handle等待，重定向pipe必须先使用`PeekNamedPipe`确认有数据再执行同步Read，不能把pipe handle的signaled状态直接等同于可安全读取；不得用无法join的永久blocked goroutine伪装取消。

### 3.6 Lifecycle

`New`检测TTY、初始化terminal mode和内部同步结构，不读取第一行并及时返回。同一进程的标准stdin/stdout/stderr是独占物理资源，只允许一个`app.cli/interaction`实例持有；第二个实例在startup返回`ErrTerminalInUse`，Cleanup完成后释放租约。`color=auto`在Windows只在stdout Console已启用Virtual Terminal Processing时输出ANSI颜色。

Cleanup先关闭新调用入口并取消pending input，再以cleanup Context等待全部active input调用退出，最后恢复terminal mode并释放终端租约；超时返回Context错误且不能提前把仍被旧实例使用的租约交给新实例。没有可靠可取消input adapter的平台必须在startup fail-closed或声明不支持，不能违反Channel Contract。

## 4. `app` Component

`app` 是 graph leaf，不向其他 Component 导出 Capability：

```go
type Dependencies struct {
    Agent       agent.Runtime
    History     agent.History
    Model       model.Runtime
    Interaction interaction.Channel
    Frontend    appcli.Frontend
    Store       session.MutableStore
}

type Exports struct{}

func New(
    ctx context.Context,
    cfg root.Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

`New` 从 Context 取 `application.Process`（缺失或非法 → `ErrInvalidConfig`）；`process.Check()` 为 true 时直接返回无 Cleanup 的 Exports，不启动 loop（与 interaction 的校验模式配合，`--ingot-check` 只验证构造）。

CLI commands首版固定为：

| Command | 行为 |
|---|---|
| `/new <title>` | 立即Create并切换人工命名Session，不触发AI改名 |
| `/new` | 回到无current Session状态；下一条普通消息创建Session |
| `/rename <title>` | 原子修改当前Session标题，随后 `syncSession` |
| `/use <id>` | 通过 `History.Load` 验证后切换Session并 `syncSession` |
| `/list` | List并渲染Session摘要 |
| `/help` | 展示命令 |
| `/exit` | 请求正常进程退出（`process.Shutdown(nil)`） |

非`/`输入调用`Agent.Run`。在官方Graph中`agent.default`获得同一个Interaction Channel，并负责渲染assistant文本、stream delta和Tool events；App不得再次渲染`agent.Result.Output`造成重复。App仅在首轮成功后将`Result.Output`与首条用户输入交给标题模型。没有current Session时首次输入前创建一个Session，CreatedAt使用注入clock的UTC now。

### 4.1 Session标题生命周期

自动创建Session采用稳定的一次性标题策略：

1. 收到首条普通消息后，合并空白、移除简单Markdown前缀并截断到48个rune，立即作为临时Title创建Session和`syncSession`；清理后为空才回退`initial_session_title`或`New Session`；
2. 首个Agent turn成功后，以第一条用户消息和`agent.Result.Output`组成JSON请求，调用同一个`model.Runtime`生成正式标题；请求不携带Tools，temperature=0.2、max_tokens=1024、超时10秒，user/assistant文本各截断到4KiB；较高生成预算用于兼容默认先输出reasoning tokens的模型，最终展示标题仍受32-rune边界约束；
3. 返回值必须是assistant单行文本、无tool call、清理后非空且不超过32个rune；合法时调用`MutableStore.Rename`并再次同步Session；
4. 无论标题Model调用、返回校验或Rename成功与否，每个自动Session最多尝试一次。失败静默保留临时标题，不改变已成功的Agent turn；
5. `/new <title>`与`/rename <title>`是人工标题，规范化空白后最多80个rune，永不被自动覆盖；`/use`切换已有Session也不触发自动命名；
6. 标题在首次自动替换后保持稳定，后续turn不再自动重写。

标题模型直接使用标准Model chokepoint，因此Provider选择、model interceptor、Context和错误链语义保持一致；标题请求不写Agent Session history。Title本身只用于展示，Session ID仍是持久化identity。

### 4.2 会话同步

`syncSession` 在每个关键点被调用（启动、`/new`、`/rename`、`/use`、自动标题替换、每个 turn 结束、turn 取消后）：`store.List(limit 100)` 取摘要 + `history.Load(current)` 取已持久化 model 消息，打包为 `SessionView` 交给 `Frontend.Sync`。TUI 用它重建 transcript 与侧栏；plain 模式无操作。

### 4.3 Turn 生命周期

```text
Frontend.StartTurn(input)
→ agent.Run 在 instance-owned goroutine 执行（turnCtx 可取消）
→ 等待: done | interrupt | ctx.Done
→ Frontend.FinishTurn(TurnCompleted | TurnCanceled | TurnFailed)
→ 首个自动命名turn成功: best-effort生成并Rename标题
→ syncSession
→ turn 出错: interaction.ErrorEvent（"session %q: %w"）后继续下一条输入
```

- `InterruptCancel`：取消 turnCtx、等待 goroutine join、`FinishTurn(TurnCanceled)`、同步并渲染 status "turn canceled"，继续读取下一条输入；
- `InterruptExit`：取消并 join 后请求进程退出；
- process Context 取消时同样先 join turn goroutine 再返回 Context 错误，不遗留 zombie。

App loop本身串行处理terminal输入；Agent运行期间不读取下一条顶层命令，因此`tool.ask`/approval可以通过同一个Channel取得input gate并读取用户响应（TUI 下由 Ask 的选项面板承接）。

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

## 6. Loop结束与进程退出语义

生成代码为每个 runtime 进程注入一个 `application.Process` 控制（SDK Contract，自 v0.1.2 起正式发布）：

- `Arguments()` 返回 `os.Args[1:]`（`--ingot-check` 时为 nil）；`Check()` 返回是否有 `--ingot-check`；
- `Shutdown(err)` 幂等，第一次调用决定进程结果：nil → 正常完成（退出码 0），非 nil → 记录 fatal error（generated main 退出码 1）；
- generated main 在 `<-ctx.Done()` 后取 `process.result()` 作为最终错误返回，与 reverse Cleanup 错误 `errors.Join`。

因此：

- `/exit`、stdin EOF 与 TUI `Ctrl+Q` 正常停止 frontend loop 并调用 `process.Shutdown(nil)`：当前 runtime 进程退出码 0；
- loop 内部无法继续的 fatal error（如 terminal adapter failure）：best-effort 渲染后保存，`process.Shutdown(err)`——进程以 1 退出，Cleanup 同时返回该错误；
- 单条命令、Store、Agent 业务错误只渲染 `interaction.ErrorEvent`，不会结束进程；
- Cleanup 主动取消造成的 `context.Canceled`/`DeadlineExceeded` 不作为 loop failure 重复上报；取消前已记录的独立 fatal error 仍返回；
- 进程退出仍由 generated main 统一完成，Component 不调用 `os.Exit`、不发信号、不取消 parent Context——`app.cli` 只是通过 SDK `application.Process` 优雅地报告自己的结束原因；
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
- 纯文本与选项 Ask统一校验、自由输入入口及空白重试、Ask串行化与行输入互斥、等待取消；
- input EOF、limit、invalid UTF-8；
- ParseArguments：`chat`→TUI、`chat --plain`→plain、其余→`ErrInvalidArguments`；
- 校验模式不读终端、不启动TUI，Cleanup幂等；
- TUI：响应式布局（wide sidebar/narrow overlay）、markdown 流式聚合、tool block 配对、history 重建与 mutable ownership、输入/选项响应、控制序列清理与 UTF-8 截断；
- terminal独占租约、`ErrTerminalRequired`、Windows VT color判断、terminal mode restore和Context-aware active input join；
- platform adapter conformance与race test。

### App

- 首次普通输入以规范化输入立即Create Session，首轮成功后最多一次AI Rename；
- 自动标题model request、输入边界、输出校验和失败保留临时标题；
- new/rename/use/list/help/exit，人工标题不自动覆盖；
- 启动与每轮结束后的 `Sync` 快照（列表 + history 加载）；
- turn 开始/结束状态机（Completed/Canceled/Failed）、Cancel 不退出、`InterruptExit` 退出；
- Agent input/result/error；
- Agent运行中Ask复用Channel无deadlock；
- `New`及时返回且每个instance只有一个owned loop；
- `/exit`、EOF 请求 `Shutdown(nil)`；fatal loop error 请求 `Shutdown(err)` 且 Cleanup 返回同一错误；
- 缺 `application.Process`、typed-nil Dependencies 拒绝构造；check 模式不启动 loop；
- process Context cancel、Cleanup cancel/join和fatal loop error回收；
- 多个app/frontend instance可以并存且状态隔离；
- fake clock、Store、Model、Agent、History、Frontend的deterministic transcript tests。

验收以顶层 `New`/`Cleanup` lifecycle conformance为准；进程退出统一经 SDK `application.Process` 报告，Builder 无 root-consumer 特例。
