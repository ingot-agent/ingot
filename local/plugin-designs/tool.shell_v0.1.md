# `tool.shell` Plugin v0.1 设计方案

> 状态：Draft  
> Component：`default`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.shell` 向 Agent 提供受配置约束的 shell command tool。它负责进程启动、工作目录、环境、输出限制、Context cancellation 和进程树回收；是否允许执行由 `tool.Runtime` 中的 approval/policy Interceptor 决定。

Plugin 本身不绕过 `tool.Runtime` 调用，不内置交互审批，也不提供 arbitrary executable registry。

## 2. Component Contract

```go
type Dependencies struct{}

type Exports struct {
    Tools []tool.Tool
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

v0.1 导出一个 tool，稳定名称为 `shell.exec`。

## 3. Config

```go
type Config struct {
    WorkingDirectory string            `toml:"working_directory"`
    Shell            string            `toml:"shell"`
    TimeoutSeconds   int               `toml:"timeout_seconds"`
    MaxOutputBytes   int               `toml:"max_output_bytes"`
    Environment      map[string]string `toml:"environment"`
    InheritEnv       []string          `toml:"inherit_env"`
}
```

v0.1 决策：

- `working_directory` required，`New` 将其解析为存在的 absolute directory；
- `shell` required，使用 absolute executable path；不通过 PATH 搜索；
- `timeout_seconds` absent/0 默认 120，必须 `> 0`；
- `max_output_bytes` absent/0 默认 1 MiB，必须 `> 0`；
- 子进程环境从非 nil 的空集合开始，只加入 `environment` 和 `inherit_env` allowlist；空配置不得退化为继承父进程环境；
- environment key 重复、非法或 `inherit_env` 中变量不存在时返回 Config Error；Windows 按环境变量名大小写不敏感的语义判断重复；
- Config 不提供 approval bypass、root shell 或 unrestricted environment 开关。

`working_directory` 与 `filesystem.local.root` 在 v0.1 是两个独立 Runtime Config。它们通常配置为同一目录，但不通过隐藏 API互相读取。

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

per-call timeout 不得超过 Config 上限。`command` 作为一个参数传给配置 shell 的 command flag；shell flag 由平台 adapter 固定，不接受模型输入。

Result 使用确定性 text envelope：

```text
exit_code: 0
stdout:
...
stderr:
...
```

`working_directory` 只设置 child process 的初始 cwd，不是 filesystem、network、process 或 OS 权限 sandbox。

stdout/stderr 分别捕获，最终按固定顺序呈现。`max_output_bytes` 在构造采集器时固定分配为 stdout 配额 `ceil(limit/2)` 和 stderr 配额 `floor(limit/2)`；任一流超过自己的配额时截断，并在实际被截断的流中显式添加 truncation marker。若截断点落在 UTF-8 多字节字符中间，丢弃该流末尾不完整的编码字节后再添加 marker，不能由截断制造非法 UTF-8。采集器继续 drain 两条 pipe，不得静默丢弃。stdout 与 stderr reader 的完成和错误必须分别归因，不能按 goroutine 完成顺序推断来源。无 exit code 的启动或 Context 错误直接返回 error。

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

- 固定 working directory（仅是初始 cwd，不是 sandbox）；
- environment allowlist；
- output 和 execution time limit；
- 不允许模型覆盖 shell path、working directory 或 environment；
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
- 空 environment 不继承父进程变量，显式 `inherit_env` allowlist 正常传递；
- stdout/stderr/exit code；
- invalid arguments 在 Runtime schema validation 阶段被拒绝；
- timeout、caller cancellation 和平台 containment primitive 回收；
- output truncation marker；
- 多调用并发和实例隔离；
- Windows/Linux/macOS platform adapter conformance；
- race test 无 goroutine/process leak。

待确认：官方支持的 shell 列表、Windows Job Object 与 Unix process group 的完整跨平台 conformance、non-zero exit 是否需要额外结构化字段。v0.1 不允许 `shell="auto"`，避免不同主机产生隐式行为差异。
