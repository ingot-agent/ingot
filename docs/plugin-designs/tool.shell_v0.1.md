# `tool.shell` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Component：`default`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.shell` 向 Agent 提供受配置约束的 shell command tool。它负责进程启动、工作目录解析、环境、输出限制、Context cancellation 和进程树回收；是否允许执行由 `tool.Runtime` 中的 approval/policy Interceptor 决定。

工作目录 authority 只来自 Session Workspace：每次 `Invocation` 携带显式 `execution.Scope`，`tool.shell` 通过 `workspace.Resolver` 解析该 Session 的 Binding，并以其 `Root` 作为 child process 的初始 cwd。没有 process cwd、config `working_directory`、HOME 或其他 ambient fallback。

Plugin 本身不绕过 `tool.Runtime` 调用，不内置交互审批，也不提供 arbitrary executable registry。

## 2. Component Contract

```go
type Dependencies struct {
    Workspace   workspace.Resolver
    Observation ingotabi.Optional[observation.Consumer]
}

type Exports struct {
    Tools []tool.Tool
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, ingotabi.Cleanup, error)
```

v0.1 导出一个 tool，稳定名称为 `shell_exec`。

## 3. Config

```go
type Config struct {
    Shell            string            `toml:"shell"`
    TimeoutSeconds   int               `toml:"timeout_seconds"`
    MaxOutputBytes   int               `toml:"max_output_bytes"`
    Environment      map[string]string `toml:"environment"`
    InheritEnv       []string          `toml:"inherit_env"`
}
```

v0.1 决策：

- `working_directory` 不再是 Config：命令工作目录由每个 `Invocation` 的 `execution.Scope` 经 `workspace.Resolver` 决定，配置阶段无法预先固定；
- `workspace.Resolver` 为 required dependency，缺少时 `New` 返回 Config Error；
- `shell` optional；省略或为空时在 `New()` 阶段自动解析默认 Shell，不通过 PATH 搜索；非空值必须是 absolute executable path；
- `timeout_seconds` absent/0 默认 120，必须 `> 0`；
- `max_output_bytes` absent/0 默认 1 MiB，必须 `> 0`；
- 子进程环境默认继承父进程完整环境（用户实际环境），使 PATH/HOME 等用户变量对命令可用；显式配置 `inherit_env` 时按 allowlist 只加入所列变量；显式 `inherit_env = []` 提供隔离路径，此时不继承任何父进程变量；
- environment key 重复、非法或 `inherit_env` 中变量不存在时返回 Config Error；Windows 按环境变量名大小写不敏感的语义判断重复；
- Config 不提供 approval bypass、root shell 或 unrestricted environment 开关。

Workspace Binding 是 Session-scoped 的本地工作目录；`tool.shell` 只在执行侧读取它。`workspace.Resolver` 是 execution-side 的唯一权威来源。

## 4. Tool Definition

Input Schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["command"],
  "properties": {
    "command": {"type": "string", "minLength": 1},
    "timeout_seconds": {"type": "integer", "minimum": 1}
  }
}
```

per-call timeout 不得超过 Config 上限。`command` 作为一个参数传给已解析 Shell 的 command flag；PowerShell 使用 `-Command`，Windows cmd 使用 `/C`，Unix Shell 使用 `-c`，这些参数由 adapter 决定且不接受模型输入。

Result 使用确定性 text envelope：

```text
exit_code: 0
stdout:
...
stderr:
...
```

解析得到的 Workspace Root 只设置 child process 的初始 cwd，不是 filesystem、network、process 或 OS 权限 sandbox；Sandbox 是独立的 security capability，不在本 Plugin 内实现。


stdout/stderr 视为外部进程提供的不可信 byte stream，在进入 collector 前通过有状态 decoder 统一归一化为合法 UTF-8；Unix 保持 UTF-8 contract，Windows 优先识别 UTF-8，无法解释时按运行时 native code page 转换，最终无法恢复的字节替换为 U+FFFD。`max_output_bytes` 作用于归一化后的 UTF-8 payload，并在构造采集器时固定分配为 stdout 配额 `ceil(limit/2)` 和 stderr 配额 `floor(limit/2)`；任一流超过自己的配额时只保留完整 UTF-8 rune，并在实际被截断的流中显式添加 truncation marker。采集器继续 drain 两条 pipe，不得静默丢弃；进程结束或 timeout 后先 flush decoder 再格式化结果。stdout 与 stderr reader 的完成和错误必须分别归因，不能按 goroutine 完成顺序推断来源。无 exit code 的启动或 Context 错误直接返回 error。

若 Graph 提供 Observation Consumer，每次 stdout/stderr write 经同一个 stateful decoder 归一化后产生 `ToolProgress`，Channel 分别为 opaque local convention `stdout` / `stderr`；text Content 永远是合法 UTF-8，无法恢复的输入使用 replacement character，不降级为 binary Content。Progress 与 Final Result 共用同一个 decoder，避免实时输出和最终结果的编码语义不一致。Progress 是 transient fact，不改变 fixed final Result envelope；Tool lifecycle Started/Finished 仍由 Agent Runtime 统一拥有，`tool.shell` 不重复产生。

## 5. 执行与生命周期

- 使用 `exec.CommandContext` 的等价平台实现，并终止平台 containment primitive 内的进程；Unix 使用独立 process group，脱离 process group 的 daemon 不在 v0.1 强保证范围内；Windows 以 suspended 状态创建进程，加入带 `KILL_ON_JOB_CLOSE` 的 Job Object 后再恢复主线程，子进程不得在进入 Job 前开始执行；
- effective Context deadline 是 caller deadline 与 per-call/config timeout 的较早者；
- timeout/cancel 后先发 cooperative termination，短 grace period 后强制终止 containment primitive；Windows cooperative termination 使用 `CTRL_BREAK` best effort，随后终止整个 Job Object；
- containment termination 失败时仍尝试强制终止根进程，避免 `Invoke` 永久等待；由于此时无法确认全部后代进程均已退出，调用仍返回 `ErrProcessCleanup`；
- `Invoke` 只有在 stdout/stderr reader 退出、进程 wait 完成后才返回；
- non-zero exit 是有效 Tool Result，不是 Go error；启动失败、I/O 失败、取消和无法回收进程是 Go error；
- 每次调用使用独立 buffer 和 process，不共享 shell session；
- v0.1 不启动长期 worker，成功 `New` 可返回 nil Cleanup。

## 6. 安全与错误

`tool.shell` 是高风险能力，但审批属于 Interceptor。官方 Graph 必须保证模型只能通过 `tool.Runtime` 调用它。

Plugin 仍需执行自身安全边界：

- 命令工作目录只来自 `workspace.Resolver`，不依赖 process cwd 或隐藏 context 约定；
- environment 继承或 allowlist（`inherit_env` 显式配置时），但无论是继承还是 allowlist，都不允许模型覆盖；
- output 和 execution time limit；
- 不允许模型覆盖 shell path 或 environment；
- Context error 保留 `context.Canceled`/`DeadlineExceeded`；
- 定义 `ErrOutputLimit` 仅用于内部采集失败；正常截断作为 Result metadata；
- 定义 `ErrProcessCleanup` 表示取消后无法确认 containment primitive 内的进程退出。

## 7. Manifest

```toml
manifest_version = 1
name = "tool.shell"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

## 8. 测试与验收

- Definition 名称、描述和 exact schema；
- working directory 和 environment isolation；
- 默认继承父进程环境；显式 `inherit_env` allowlist 正常传递；显式 `inherit_env = []` 隔离父进程变量；
- stdout/stderr/exit code；
- stdout/stderr ToolProgress channel、Content ownership，且不重复 lifecycle event；
- stdout/stderr 的 UTF-8 normalization、跨 Write 字符边界、Windows native code page 与 replacement fallback；
- invalid arguments 在 Runtime schema validation 阶段被拒绝；
- timeout、caller cancellation 和平台 containment primitive 回收；
- output truncation marker；
- 多调用并发和实例隔离；
- Windows/Linux/macOS platform adapter conformance；
- race test 无 goroutine/process leak。

默认 Shell 先尊重操作系统用户配置，再使用平台 fallback。Unix 使用 `os/user.Current()` 确认当前账户；由于 Go 的 `user.User` 不暴露 Shell 字段，再从 `/etc/passwd` 读取该账户的登录 Shell，找不到或不可执行时依次尝试 `/usr/bin/zsh`、`/usr/bin/sh`、`/bin/sh`。Windows 没有对应的用户登录 Shell 字段，因此依次尝试标准安装位置的 PowerShell 7 (`pwsh.exe`)、Windows PowerShell 5 (`powershell.exe`) 和 cmd (`cmd.exe`)。解析不使用 PATH；自动解析在 `New()` 阶段执行一次，显式配置始终优先且错误时不 fallback。其他平台的自动模式不受支持。

待确认：Windows Job Object 与 Unix process group 的完整跨平台 conformance、non-zero exit 是否需要额外结构化字段。`tool.shell` 不使用 `"auto"` 等魔法配置值；缺省 `shell` 本身即表示默认模式。
