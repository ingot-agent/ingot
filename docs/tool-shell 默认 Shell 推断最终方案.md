# tool-shell 默认 Shell 推断方案

## 一、背景

当前 `tool-shell` 要求用户必须显式配置 Shell 的绝对路径，例如：

```toml
[plugins."tool.shell"]
shell = "/bin/bash"
```

`normalizeConfig()` 中会直接校验：

```go
if cfg.Shell == "" || !filepath.IsAbs(cfg.Shell) {
    return normalizedConfig{}, fmt.Errorf(
        "shell must be an absolute executable path: %w",
        ErrInvalidConfig,
    )
}
```

这意味着一个默认启用 `tool.shell` 的 Ingot 环境，在没有提前知道目标机器 Shell 路径的情况下无法直接启动。

另一方面，当前 `internal/home/init.go` 已经存在一套 `defaultShellPath()` 逻辑。`ingot init` 会在初始化阶段探测当前机器上的 Shell，然后将类似：

```toml
shell = "/bin/sh"
```

这样的机器相关路径写入配置。

这实际上把 Runtime 环境探测职责放到了 `home/init` 层，并且导致生成的配置携带机器相关信息。例如在 Linux 上生成的配置迁移到 Windows 后，原来的 Shell 路径自然失效。

因此本次修改的核心不是简单“再增加一套 Shell 推断”，而是将已有职责重新归位：

```text
当前

ingot init
    ↓
探测当前机器 Shell
    ↓
写入绝对路径到 config.toml
    ↓
tool.shell 强制要求 shell 配置


修改后

ingot init
    ↓
生成不包含显式 shell 的配置
    ↓
tool.shell New()
    ↓
运行时自动推断默认 Shell
```

即：

> Shell 的默认选择属于 `tool.shell` Runtime 环境解析职责，而不是 `ingot init` 的配置生成职责。

---

# 二、设计目标

本次改动需要满足以下目标：

1. 用户不配置 `shell` 时，`tool.shell` 能直接工作。
2. 用户显式配置 Shell 时，继续保持严格校验。
3. 显式配置永远优先，配置错误不得自动 fallback。
4. 默认 Shell 的选择尽量确定，不依赖用户 PATH、登录 Shell 等环境偏好。
5. 不改变现有 Shell command invocation contract。
6. 不引入 PowerShell、Fish、Nushell 等新的 Shell dialect。
7. Shell 只在组件初始化时解析一次。
8. 保持现有 `Config`、Plugin ABI 和调用接口不变。
9. 删除 `home/init` 中重复的 Shell 探测职责。

---

# 三、配置语义

保持当前配置结构：

```go
type Config struct {
    Shell          string            `toml:"shell"`
    TimeoutSeconds int               `toml:"timeout_seconds"`
    MaxOutputBytes int               `toml:"max_output_bytes"`
    Environment    map[string]string `toml:"environment"`
    InheritEnv     []string          `toml:"inherit_env"`
}
```

不修改为：

```go
Shell *string
```

也不增加：

```toml
shell = "auto"
```

新的语义定义为：

```text
Shell == ""
    自动模式

Shell != ""
    显式模式
```

因此：

```toml
[plugins."tool.shell"]
```

就已经是一份合法配置。

用户需要覆盖默认 Shell 时仍然可以：

```toml
[plugins."tool.shell"]
shell = "/bin/bash"
```

---

# 四、核心行为模型

Shell 解析分成两条完全独立的路径。

## 4.1 显式模式

当：

```go
cfg.Shell != ""
```

时：

```text
读取配置值
    ↓
检查是否绝对路径
    ↓
检查目标是否存在
    ↓
检查是否满足 Shell executable 要求
    ↓
成功
```

任何一步失败：

```text
直接 ErrInvalidConfig
```

绝不进入默认 Shell 推断。

例如：

```toml
shell = "/opt/bash"
```

但 `/opt/bash` 不存在。

错误行为：

```text
/opt/bash 不存在
↓
偷偷 fallback 到 /bin/sh
```

正确行为：

```text
/opt/bash 不存在
↓
启动失败
```

因为用户一旦进行了显式配置，就意味着这是他的明确意图。

---

## 4.2 自动模式

当：

```go
cfg.Shell == ""
```

时：

```text
inferDefaultShell()
    ↓
按照当前 GOOS 获取候选 Shell
    ↓
依次静态验证
    ↓
找到第一个可用 Shell
    ↓
保存绝对路径
```

如果全部候选失败：

```text
ErrInvalidConfig
```

自动模式本身也不应该拖到第一次 `Invoke()` 才失败。

必须在：

```go
New()
```

阶段 fail fast。

---

# 五、为什么默认模式不使用 PATH

本方案明确规定：

> auto mode 不通过 PATH 搜索 Shell。

这里的主要原因不是 `inherit_env`。

`inherit_env` 控制的是后续 Shell 子进程可以继承哪些环境变量，而 Shell 推断发生在 `tool.shell.New()` 阶段，两者生命周期并不相同。

真正不依赖 PATH 的原因是：

### 1. 确定性

相同操作系统上的默认行为尽可能一致。

如果使用：

```go
exec.LookPath("sh")
```

最终结果会受到 Ingot 启动方式影响，例如：

- 用户 Terminal；
- IDE；
- systemd；
- Docker；
- SSH；
- GUI application；
- profile script。

### 2. 避免 ambient environment 影响默认语义

默认模式的目标不是：

> 找当前用户最喜欢的 Shell。

而是：

> 找到 Ingot 能够依赖的最低公共 Shell execution contract。

### 3. 配置行为更容易预测

用户需要特殊 Shell，可以显式配置。

默认模式不承担 Shell preference discovery。

---

# 六、支持的默认 Shell

## 6.1 Unix-like

优先级：

```text
1. /bin/sh
2. /usr/bin/sh
```

找到第一个有效 Shell 即停止。

不自动尝试：

```text
$SHELL
/bin/bash
/usr/bin/bash
/bin/zsh
/usr/bin/zsh
fish
nu
```

### 原因

当前 Unix 调用协议是：

```go
shell -c command
```

因此 auto mode 应寻找一个稳定的 POSIX-style `sh` contract。

例如：

```text
/bin/sh -c "echo hello"
```

用户如果明确需要 Bash feature：

```toml
shell = "/bin/bash"
```

即可。

默认模式没有必要替用户决定 Bash、Zsh 或其他 Shell。

---

# 七、Windows 默认 Shell

Windows 默认候选：

```text
1. %ComSpec%
2. %SystemRoot%\System32\cmd.exe
```

其中 `%ComSpec%` 必须满足：

```text
absolute path
+
存在
+
regular file
+
basename == cmd.exe（case-insensitive）
```

例如：

```text
C:\Windows\System32\cmd.exe
```

如果：

```text
ComSpec=C:\foo\custom-shell.exe
```

即使该文件存在，也不作为默认 Shell 接受。

然后 fallback：

```text
%SystemRoot%\System32\cmd.exe
```

若 `SystemRoot` 不存在或目标文件不存在，则自动模式失败。

---

# 八、为什么 Windows 不自动选择 PowerShell

当前代码的 Shell invocation protocol 实际是：

```go
func shellCommandArgs(command string) []string {
    if runtime.GOOS == "windows" {
        return []string{"/C", command}
    }

    return []string{"-c", command}
}
```

Windows 上实际调用：

```text
shell.exe /C command
```

这是 `cmd.exe` 的 Shell contract。

PowerShell 的正常 invocation contract 是：

```text
pwsh.exe -Command command
```

因此当前：

```text
pwsh.exe /C command
```

并不是 `tool.shell` 已定义支持的行为。

所以本次 auto mode：

```text
Windows = cmd.exe
```

而不是：

```text
Windows = 任意 Shell
```

PowerShell 支持属于另一个独立功能，不应混入本次改动。

---

# 九、Shell 校验

建议将当前散落在 `normalizeConfig()` 中的 Shell validation 抽离出来。

例如：

```go
func validateShell(path string) (string, error)
```

职责包括：

```text
absolute path
↓
os.Stat
↓
不是 directory
↓
平台相关 executable validation
↓
返回规范化后的绝对路径
```

## Unix

继续检查：

```go
info.Mode() & 0o111 != 0
```

## Windows

不需要实现一个通用的：

```text
.exe
.cmd
.bat
.com
```

可执行文件识别系统。

因为这里验证的是：

> Shell executable

而不是：

> 任意 Windows executable。

对于 auto mode，只接受 `cmd.exe`。

显式模式则继续维持当前 Windows 文件存在性校验即可，不在本次 PR 中顺带收紧显式 Shell 类型。

---

# 十、代码结构

推荐按照当前 `process_*.go` 的平台结构设计：

```text
plugins/tool-shell/
├── toolshell.go
├── toolshell_test.go
│
├── shell.go
├── shell_windows.go
├── shell_unix.go
├── shell_other.go
│
├── process_windows.go
├── process_unix.go
└── process_other.go
```

不需要：

```text
shell_infer.go
    ↓
runtime.GOOS switch
```

因为 Go build tags 本身已经完成了平台分派。

---

# 十一、`shell.go`

负责平台无关逻辑：

```go
func resolveShell(configured string) (string, error) {
    if configured != "" {
        if !filepath.IsAbs(configured) {
            return "", fmt.Errorf(
                "shell must be an absolute executable path: %w",
                ErrInvalidConfig,
            )
        }

        shell, err := validateShell(configured)
        if err != nil {
            return "", err
        }

        return shell, nil
    }

    inferred, err := inferDefaultShell()
    if err != nil {
        return "", fmt.Errorf(
            "infer default shell: %w: %w",
            ErrInvalidConfig,
            err,
        )
    }

    shell, err := validateShell(inferred)
    if err != nil {
        return "", err
    }

    return shell, nil
}
```

这里最好不要：

```go
cfg.Shell = inferred
```

而是：

```text
Config
    ↓
resolve
    ↓
normalizedConfig
```

保持配置输入不可变。

---

# 十二、Unix 实现

`shell_unix.go`：

```go
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package toolshell

func inferDefaultShell() (string, error) {
    candidates := []string{
        "/bin/sh",
        "/usr/bin/sh",
    }

    for _, candidate := range candidates {
        if usableShell(candidate) {
            return candidate, nil
        }
    }

    return "", fmt.Errorf(
        "no usable default shell found; tried: %v",
        candidates,
    )
}
```

具体静态校验可以复用 `validateShell()` 的底层 helper，避免出现：

```text
infer 校验一套
normalize 再校验一套
```

导致逻辑漂移。

---

# 十三、Windows 实现

`shell_windows.go`：

```go
//go:build windows

package toolshell

func inferDefaultShell() (string, error) {
    var candidates []string

    if comspec := os.Getenv("ComSpec"); comspec != "" {
        if filepath.IsAbs(comspec) &&
            strings.EqualFold(filepath.Base(comspec), "cmd.exe") {
            candidates = append(candidates, comspec)
        }
    }

    if root := os.Getenv("SystemRoot"); root != "" {
        candidates = append(
            candidates,
            filepath.Join(root, "System32", "cmd.exe"),
        )
    }

    for _, candidate := range candidates {
        if usableShell(candidate) {
            return candidate, nil
        }
    }

    return "", fmt.Errorf(
        "no usable default cmd shell found; tried: %v",
        candidates,
    )
}
```

不使用：

```go
exec.LookPath("cmd.exe")
```

也不探测：

```text
pwsh.exe
powershell.exe
bash.exe
```

---

# 十四、其他平台

`process_other.go` 当前允许其他平台至少拥有基本的 process execution fallback，但这不意味着 auto mode 必须猜测一个 Shell。

因此：

```go
func inferDefaultShell() (string, error) {
    return "", fmt.Errorf(
        "automatic shell resolution is unsupported on this platform",
    )
}
```

更安全。

这些平台仍然可以通过：

```toml
shell = "/absolute/path/to/shell"
```

显式使用现有能力。

也就是说：

```text
process support
```

和：

```text
default shell discovery support
```

是两个独立概念。

---

# 十五、修改 `normalizeConfig`

当前：

```go
if cfg.Shell == "" || !filepath.IsAbs(cfg.Shell) {
    ...
}
```

修改为：

```go
shell, err := resolveShell(cfg.Shell)
if err != nil {
    return normalizedConfig{}, err
}
```

最终：

```go
return normalizedConfig{
    shell:          shell,
    timeout:        timeout,
    maxOutputBytes: maxOutputBytes,
    ...
}, nil
```

这样后续 `Invoke()` 完全不用知道 Shell 是：

```text
用户配置的
```

还是：

```text
系统推断的
```

对于 Runtime 来说，它们最终都只是：

```go
normalizedConfig.shell
```

---

# 十六、解析时机

Shell 推断只发生一次：

```text
New()
↓
normalizeConfig()
↓
resolveShell()
↓
inferDefaultShell()
↓
normalizedConfig.shell
```

之后：

```text
Invoke #1
Invoke #2
Invoke #3
...
```

全部使用已经解析完成的绝对路径。

绝不在每次：

```go
Invoke()
```

时重新推断。

这可以保证：

1. 不重复做文件系统检查；
2. Runtime 生命周期内 Shell 行为稳定；
3. 环境变量即使中途改变，也不会改变已有 Component 行为。

---

# 十七、`ingot init` 修改

这是本次改动必须同步完成的部分。

删除：

```go
internal/home/defaultShellPath()
```

以及：

```text
renderConfigTOML()
    ↓
探测 Shell
    ↓
写 shell = "..."
```

的逻辑。

新的默认生成结果：

```toml
# --- shell tool execution boundary ---
# Shell is automatically resolved when omitted.
# Set an absolute shell path to override the default.

[plugins."tool.shell"]
# shell = "/absolute/path/to/shell"
# timeout_seconds = 30
# max_output_bytes = 1048576
```

这里建议注释写：

```text
/absolute/path/to/shell
```

而不要固定展示：

```text
/bin/bash
```

否则 Windows 用户看到生成配置容易产生误解。

这样 `config.toml` 不再携带初始化机器相关的 Shell 信息。

---

# 十八、测试方案

## 18.1 清理现有测试 helper

当前测试中存在类似：

```go
if cfg.Shell == "" {
    cfg.Shell = testShellPath()
}
```

这段逻辑必须删除。

否则：

```go
testShell(t, Config{})
```

仍然不会真正测试 auto mode。

修改后：

```go
Config{}
```

应该原样传入：

```go
New()
```

这会让现有大量测试自然覆盖默认模式。

---

## 18.2 通用测试

### 空配置可以初始化

```go
Config{}
```

应该成功构建 `tool.shell`。

并执行：

```text
echo hello
```

成功。

---

### 显式 Shell 优先

```go
Config{
    Shell: testShellPath(),
}
```

必须使用该路径，不执行 auto inference。

---

### 显式非法路径不能 fallback

例如：

```go
Config{
    Shell: "/definitely/not/exist",
}
```

期望：

```text
errors.Is(err, ErrInvalidConfig) == true
```

即使机器上 `/bin/sh` 存在也必须失败。

---

### 相对路径继续非法

```go
Shell: "sh"
```

仍然：

```text
ErrInvalidConfig
```

本次改动不改变显式配置 contract。

---

# 十九、平台测试

## Unix

测试候选优先级：

```text
/bin/sh
↓
/usr/bin/sh
```

并检查返回值为绝对路径。

由于真实 CI 环境难以模拟 `/bin/sh` 不存在，建议将候选选择的核心逻辑抽成可测试的小函数，例如：

```go
func firstUsableShell(
    candidates []string,
    usable func(string) bool,
) (string, error)
```

这样可以测试候选顺序，而不需要修改宿主文件系统。

---

## Windows

重点测试：

### ComSpec 正常

```text
ComSpec=C:\Windows\System32\cmd.exe
```

优先返回。

### ComSpec 非 cmd.exe

例如：

```text
ComSpec=C:\Tools\pwsh.exe
```

不能接受，应继续 fallback。

### ComSpec 无效

目标不存在：

```text
fallback -> SystemRoot\System32\cmd.exe
```

### 所有候选失败

返回：

```text
ErrInvalidConfig
```

---

# 二十、`internal/home` 测试

修改 `init_test.go`。

以前可能检查生成配置存在：

```toml
shell = "..."
```

现在应该改为：

```text
存在 [plugins."tool.shell"]
```

但：

```text
不存在 active shell = "..."
```

同时保证生成的 TOML 仍能正常解析。

---

# 二十一、错误语义

不新增 exported sentinel。

继续统一：

```go
ErrInvalidConfig
```

即可。

错误信息区分两种场景。

## 显式配置错误

例如：

```text
configured shell "/foo/bar" does not exist: invalid tool.shell config
```

## 自动推断失败

例如：

```text
infer default shell: no usable default shell found; tried [/bin/sh /usr/bin/sh]: invalid tool.shell config
```

暂时没有必要增加：

```go
ErrShellUnavailable
```

因为调用方当前只需要知道：

```text
Component configuration failed
```

未来如果上层真的需要区分：

```text
配置错误
vs
系统无 Shell
```

再增加 sentinel。

---

# 二十二、兼容性

本次修改可以保持完全向后兼容。

旧配置：

```toml
[plugins."tool.shell"]
shell = "/bin/bash"
```

行为完全不变。

新配置：

```toml
[plugins."tool.shell"]
```

开始合法，并启用默认 Shell。

因此不需要：

- Plugin ABI bump；
- SDK API 修改；
- Manifest schema 修改；
- config migration；
- compatibility adapter。

---

# 二十三、设计文档修改

同步更新：

```text
docs/plugin-designs/tool.shell_v0.1.md
```

原来的：

```text
shell required
```

修改为：

```text
shell optional
```

明确：

```text
shell omitted / empty
    -> automatic default resolution

shell non-empty
    -> explicit absolute path
```

删除原先类似：

```text
v0.1 不允许 shell="auto"
```

的设计表述。

新的规则可以写成：

> `tool.shell` 不使用 `"auto"` 等魔法配置值。缺省 `shell` 本身即表示默认模式。

另外明确：

> 自动模式只解析当前插件 invocation protocol 明确支持的 baseline shell，不负责发现用户偏好的任意 Shell。

---

# 二十四、本次 PR 明确不做的事情

为了控制改动边界，本次不实现：

### 1. PowerShell 自动发现

不做：

```text
pwsh
powershell.exe
```

### 2. Shell kind

不增加：

```go
type ShellKind string
```

### 3. Shell driver

不增加：

```go
type ShellDriver interface {
    Args(command string) []string
}
```

### 4. PATH discovery

不使用：

```go
exec.LookPath()
```

### 5. `$SHELL`

Unix 不根据用户 login shell 自动选择。

### 6. 通用 Shell dialect detection

不尝试判断：

```text
bash
zsh
fish
nu
cmd
powershell
```

### 7. 配置语法扩展

不增加：

```toml
shell = "auto"
shell_kind = "powershell"
```

这些都可以作为后续独立 capability 演进。

---

# 二十五、未来演进方向

未来如果需要正式支持：

```text
cmd
PowerShell
POSIX sh
fish
Nushell
```

则当前：

```go
shellCommandArgs()
```

不应该再根据：

```go
runtime.GOOS
```

决定参数。

而应该升级成 Shell dialect abstraction，例如：

```go
type shellKind uint8

const (
    shellPOSIX shellKind = iota
    shellCMD
    shellPowerShell
)

type shellSpec struct {
    Path string
    Kind shellKind
}
```

然后：

```go
func (s shellSpec) commandArgs(command string) []string {
    switch s.Kind {
    case shellCMD:
        return []string{"/C", command}

    case shellPowerShell:
        return []string{"-Command", command}

    default:
        return []string{"-c", command}
    }
}
```

届时才能真正讨论：

```text
Windows 默认 cmd 还是 PowerShell
```

但这不属于本次默认模式 PR。

---

# 二十六、最终实现范围

本次改动建议控制在四组代码：

```text
1. plugins/tool-shell
   新增 shell resolver
   修改 normalizeConfig

2. plugins/tool-shell tests
   删除测试层默认 Shell 注入
   增加 auto / explicit 行为测试

3. internal/home
   删除 defaultShellPath()
   init 不再写具体 shell 路径

4. docs
   修改 tool.shell_v0.1.md
```

最终 Runtime 流程：

```text
                     Config.Shell
                          │
                 ┌────────┴────────┐
                 │                 │
               empty            non-empty
                 │                 │
                 ▼                 ▼
       inferDefaultShell()   validate explicit
                 │                 │
                 ▼                 │
            validate               │
                 │                 │
                 └────────┬────────┘
                          ▼
               normalizedConfig.shell
                          │
                          ▼
                     Invoke()
                          │
                          ▼
                    exec.Command
```

本次功能最终可以概括为：

> `tool.shell` 从“必须告诉我 Shell 在哪里”变成“默认提供一个稳定的系统 Shell，如果用户明确指定，则严格遵从用户配置”。

---

# 二十七、决策点

## 决策 1：空 `shell` 是否代表 auto mode？

### 方案 A
```toml
shell = ""
```

或省略字段即自动推断。

### 方案 B
显式增加：

```toml
shell = "auto"
```

### 倾向

**A。**

理由：

- 不引入 path/string keyword 联合语义；
- `Shell` 字段仍然只表示路径；
- zero value 自然代表默认行为；
- 不需要修改 Config 类型。

---

## 决策 2：Shell 默认探测应该放在哪里？

### A
继续放在：

```text
internal/home/init.go
```

### B
放到：

```text
plugins/tool-shell
```

### 倾向

**B。**

Runtime capability 应由 capability 自己解析运行环境。

`init` 不应写入机器相关 Shell 路径。

---

## 决策 3：auto mode 是否允许 PATH lookup？

### A
允许：

```go
exec.LookPath()
```

### B
只使用确定的绝对位置和系统环境变量。

### 倾向

**B。**

优先保证确定性和配置可移植性。

---

## 决策 4：Unix 是否读取 `$SHELL`？

### A
读取用户默认 Shell。

### B
不读取，只选择 `sh` baseline。

### 倾向

**B。**

`$SHELL` 表示用户 preference，不表示 Agent execution contract。

---

## 决策 5：Unix 默认候选有哪些？

### A

```text
/bin/sh
/usr/bin/sh
```

### B

```text
/bin/sh
/usr/bin/bash
/usr/bin/zsh
...
```

### 倾向

**A。**

auto mode 提供 baseline，不负责寻找“功能更强”的 Shell。

---

## 决策 6：Windows 默认选择 cmd 还是 PowerShell？

### A
`cmd.exe`

### B
优先 `pwsh.exe`

### 倾向

**A，而且本次实际上不应把它作为运行时 preference。**

当前 Windows invocation 使用：

```text
/C
```

本身就是 cmd contract。

PowerShell 支持需要先引入 Shell dialect abstraction。

---

## 决策 7：Windows 是否接受任意 `ComSpec`？

### A
只要文件存在就接受。

### B
要求最终 basename 是：

```text
cmd.exe
```

### 倾向

**B。**

避免 `ComSpec` 指向与 `/C` contract 不兼容的 command processor。

---

## 决策 8：显式配置失败后是否自动 fallback？

### A
自动 fallback 到默认 Shell。

### B
立即报错。

### 倾向

**B。**

显式配置代表明确用户意图，silent fallback 会掩盖配置错误。

---

## 决策 9：Shell 推断什么时候执行？

### A
每次 `Invoke()`。

### B
`New()` 时一次性解析。

### 倾向

**B。**

更稳定、更容易 fail fast，也避免重复系统调用。

---

## 决策 10：unsupported GOOS 是否尝试猜测 `/bin/sh`？

### A
尝试。

### B
auto mode 直接返回 unsupported；显式配置仍允许。

### 倾向

**B。**

默认探测应是明确支持，而不是猜测。

---

## 决策 11：是否新增 `ErrShellUnavailable`？

### A
新增。

### B
继续使用 `ErrInvalidConfig`。

### 倾向

**B。**

当前没有调用方需要区分这一层错误，避免过早扩展公开错误 contract。

---

## 决策 12：是否保留 `internal/home/defaultShellPath()`？

### A
保留，和 `tool.shell` 各做一套。

### B
删除。

### 倾向

**B。**

避免同一个 Shell selection policy 出现两个 source of truth。

---

## 决策 13：是否修改 Plugin ABI / Config schema？

### A
升级。

### B
保持不变。

### 倾向

**B。**

`string` zero value 已经足够表达新的默认语义。

---

## 决策 14：本次是否顺便实现 Shell dialect abstraction？

### A
实现 `ShellKind` / `ShellDriver`。

### B
暂不实现。

### 倾向

**B。**

本次需求只是：

```text
Config.Shell == "" 可以工作
```

而不是构建一个通用多 Shell subsystem。

---

# 最终建议

以上 14 个决策我都建议直接采用倾向方案。

也就是最终收敛为：

```text
Unix:
    /bin/sh
    ↓
    /usr/bin/sh

Windows:
    ComSpec(cmd.exe)
    ↓
    SystemRoot\System32\cmd.exe

显式 Shell:
    absolute + valid
    ↓
    永远优先
    ↓
    错误时不 fallback

不使用:
    PATH
    $SHELL
    PowerShell auto
    ShellKind
    magic "auto"
```

这是当前代码架构下改动最小、contract 最清晰，同时又能真正让 `tool.shell` 达到 zero-config usable 的版本。