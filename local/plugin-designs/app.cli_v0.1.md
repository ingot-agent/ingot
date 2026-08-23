# `app.cli` Composite Plugin v0.1 设计方案

> 状态：Draft，存在一个SDK生命周期前置决策  
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
- 等gate和等用户输入都观察Context；
- TextEvent写普通输出，StatusEvent使用可降级状态行，ErrorEvent写stderr，Tool events使用稳定人类可读格式；
- 非TTY时关闭color和in-place status，只输出plain text；
- input超过limit时丢弃到line boundary并返回`ErrInputLimit`；
- EOF返回可识别`io.EOF`；
- 不关闭并非自己拥有的stdin/stdout/stderr。

标准`bufio.Reader.ReadString`无法被Context可靠取消。Production实现必须使用platform terminal driver：Unix采用poll/select或可取消fd读取，Windows采用Console/handle等待；不得用无法join的永久blocked goroutine伪装取消。

### 3.2 Lifecycle

`New`检测TTY、初始化terminal mode和内部同步结构，不读取第一行并及时返回。Cleanup取消pending input、恢复terminal mode并等待Plugin-owned helper退出。没有可靠可取消input adapter的平台必须在startup fail-closed或声明不支持，不能违反Channel Contract。

## 4. `app` Component

现有文档可表达其Dependencies：

```go
type Dependencies struct {
    Agent       agent.Runtime
    Interaction interaction.Channel
    Store       session.Store
}
```

CLI commands首版固定为：

| Command | 行为 |
|---|---|
| `/new [title]` | Create并切换Session |
| `/use <id>` | 验证Load后切换Session |
| `/list` | List并渲染Session摘要 |
| `/help` | 展示命令 |
| `/exit` | 正常结束application run |

非`/`输入调用`Agent.Run`。在官方Graph中`agent.default`获得同一个Interaction Channel，并负责渲染assistant文本、stream delta和Tool events；App不得再次渲染`agent.Result.Output`造成重复。`Result.Output`只用于无Interaction的其他Agent consumer。没有current Session时首次输入前创建一个Session，CreatedAt使用注入clock的UTC now。

App loop本身串行处理terminal输入；Agent运行期间不读取下一条顶层命令，因此`tool.ask`/approval可以通过同一个Channel取得input gate并读取用户响应。

## 5. 必须解决的Application生命周期Contract

现有SDK只有`agent.Runtime`和Component `New/Cleanup`：

- `New`必须及时返回，不能把CLI loop阻塞在constructor；
- 若在background goroutine运行，现有generated main只等待process signal，无法观察`/exit`、EOF或loop error；
- `os.Exit`会跳过generated reverse Cleanup；
- Context只提供取消观察，Component无法安全取消parent process context。

因此在实现`app` Component前必须补充一个显式root application Contract。推荐新增SDK package：

```go
package application

type Runner interface {
    Run(context.Context) error
}
```

然后app Component：

```go
type Exports struct {
    Runner application.Runner
}
```

Generated main作为Builder-owned root consumer要求恰好一个`application.Runner`：

```text
construct graph
→ startup validation
→ Runner.Run(processCtx)
→ Run返回或signal取消
→ cancel process context
→ reverse Cleanup
→ exit
```

这保持CLI是普通Component、退出可传播error且Cleanup不被绕过。备选方案是新增`process.Controller` Capability，但会把parent cancellation authority暴露给任意Component；v0.1推荐Runner方案。

在该Contract冻结前，`app.cli/app`状态为设计完成但实现blocked；不得通过special package import、global channel或`os.Exit`临时绕过。

## 6. Runner semantics

- 同一个Runner instance只允许一次active Run；重复/并发调用返回`ErrAlreadyRunning`；
- Run在caller goroutine执行loop，不自行转后台；
- `/exit`和stdin EOF正常返回nil；
- Context cancel返回Context error或按generated main shutdown policy归一化为正常退出；该政策需由application Contract文档固定；
- Agent/Store/Interaction错误添加command/session上下文并返回或渲染后继续，按错误分类决定：Context/terminal fatal error终止，单个Agent turn业务error渲染后继续；
- current Session只在Runner instance内存中，不写Plugin State；Session本身由Store持久化。

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
- Ask/ReadLine serialization和等待取消；
- input EOF、limit、invalid UTF-8；
- terminal mode restore和helper join；
- platform adapter conformance与race test。

### App

- empty startup自动Create Session；
- new/use/list/help/exit；
- Agent input/result/error；
- Agent运行中Ask复用Channel无deadlock；
- EOF、signal和fatal error走完整reverse Cleanup；
- Runner single-active-call；
- fake clock、Store、Agent、Channel的deterministic transcript tests。

验收前置条件：application Runner SDK Contract、Builder root-consumer规则和generated main lifecycle test全部落地。
