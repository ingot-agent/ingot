# ADR 0002：Runtime Home 解析与所有权

- 状态：Frozen
- 里程碑：M0（定稿）/ M1（落地）
- 相关：`internal/builder/generate.go`、`internal/home/home.go`、`internal/builder/build.go`

## 背景

当前生成的 runtime 读取三个环境变量并依赖完整 Ingot Home：

```go
// internal/builder/generate.go:307-319
home := os.Getenv("INGOT_HOME")
configPath := os.Getenv("INGOT_CONFIG")        // 默认 <home>/config.toml
stateRoot := os.Getenv("INGOT_STATE_ROOT")     // 默认 <home>/state
```

`internal/home/home.go:561` 在派发时注入 `INGOT_HOME`；
`internal/builder/build.go:217` 在 pre-check 时注入三个变量。
runtime binary 因此无法脱离 Ingot Home 独立运行。

## 决策

### 1. 唯一的解析机制

`INGOT_RUNTIME_HOME` 是 runtime 读取的**唯一**环境变量：

```text
if INGOT_RUNTIME_HOME is set and non-empty:
    use it
else:
    use <absolute executable path>.home
```

约束：

- 路径必须解析为绝对路径；
- `<absolute executable path>` 是实际执行文件的绝对路径，
  **不是**当前工作目录，**不是** PATH 查找结果；
- 两个不同文件名的相同 binary 默认产生两个隔离的 Runtime Home；
- 同一 binary 的多次启动复用同一个 Runtime Home。

### 2. Managed 不引入新机制

ingot 管理 Runtime 不需要特殊机制，只是在启动 runtime 进程时**注入
`INGOT_RUNTIME_HOME`** 来控制其行为。standalone 与 managed 的差别仅在于
「谁设置了这个变量」，runtime 侧没有分支。

因此：

- Runtime **不是** runtime 进程侧的对象；
- Runtime 是 manager 侧的 registry 记录（name、image reference、home）；
- runtime 进程只知道「我自己的 Home」以及编译进去的 Image 身份；
- standalone 是「没有设置 `INGOT_RUNTIME_HOME`」的那个分支，不是特例。

### 3. 目录不可写必须显式失败

启动顺序固定为：

```text
解析 Runtime Home（绝对路径）
  ↓
创建目录 + 写探测   → 失败即显式报错，不构造任何 Plugin
  ↓
构造 Plugin
  ↓
Runtime 启动
```

- **必须**做真实的写探测：`os.MkdirAll` 在目录已存在时返回 `nil`，
  即使该目录不可写，因此不能只依赖它；
- 失败时不得静默降级到临时目录（会让用户误以为状态已持久化）；
- 错误信息必须包含路径、原因和可行的替代方案。

### 4. 布局

最低 contract 只需要：

```text
<runtime home>/
  state/      # 必需，Plugin-owned persistent storage
```

推荐但按需生成：

```text
<runtime home>/
  state/
  run/        # IPC / process information
  logs/       # optional logs
```

`state/` 是唯一必需目录，必须可自动创建；其余目录缺失不等于错误。
Host 不解释 `state/` 内的内容。

### 5. Plugin State Scope

每个 Plugin 获得独立 scope：

```text
<runtime home>/state/<plugin-scope>/
```

`<plugin-scope>` 与 Plugin identity 一一对应。Host 不解释其中内容；
Plugin 不得读写其它 Plugin 的 scope，也不得假设 scope 之外的路径存在。

## 后果

- M1 需要修改三处注入点：`generate.go` 只读 `INGOT_RUNTIME_HOME`；
  `home.go:RunCurrent` 注入 `INGOT_RUNTIME_HOME`；`build.go:217` 注入临时
  Runtime Home 做 pre-switch check。
- Runtime binary 不再依赖完整 Ingot Home。
- `state/` 的目录拼写是**落盘的持久格式**，一旦写入用户磁盘即不可随意更改。

## 不冻结

- `<plugin-scope>` 的具体拼写（manifest 短名 vs Plugin ID，M1 决定，
  注意现状 `generate.go:407` 用的是 Plugin ID）；
- `run/` 与 `logs/` 的具体内容与文件格式（按需，各里程碑自定）；
- managed Runtime 的 registry 目录布局（M2）；
- 首次初始化的输出文案（M1）。
