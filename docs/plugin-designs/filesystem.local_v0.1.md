# `filesystem.local` Plugin v0.1 设计方案

> 状态：Implemented v0.1  
> Component：`default`  
> Exports：`filesystem.FS`

## 1. 定位

`filesystem.local` 将一个配置的本地主机目录暴露为 workspace-relative `filesystem.FS`。它是 Agent file tool 与其他 workspace consumer 的安全边界，不是任意主机文件系统访问接口。

典型连接关系：

```text
filesystem.local --filesystem.FS--> tool.fs
```

## 2. 目标与非目标

目标：

- 所有 SDK path 相对于一个 Plugin instance 的 workspace root；
- 在 Windows、Linux 和 macOS 上使用统一的 `/` 逻辑路径语义；
- 拒绝 absolute path、parent traversal、非 root dot segment、反斜杠和 NUL；
- 保证解析后的路径和 symlink target 不离开 workspace；
- 提供并发安全、确定性排序和可识别的 `io/fs` 错误；
- `WriteFile` 不向并发 reader 暴露部分写入结果；
- `Rename` 不覆盖已有 destination。

非目标：

- glob、recursive walk、watch、文件锁或 advisory lock；
- arbitrary host path、用户 home shortcut 或环境变量展开；
- recursive delete；
- 创建 symlink、hard link、socket、FIFO 或 device；
- 权限沙箱、容器隔离或操作系统账户隔离。

## 3. Component Contract

```go
package filesystemlocal

import (
    "context"

    "github.com/ingot-agent/sdk"
    "github.com/ingot-agent/sdk/filesystem"
)

type Dependencies struct{}

type Exports struct {
    FS filesystem.FS
}

func New(
    ctx context.Context,
    cfg Config,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

## 4. Config 与 workspace root

```go
type Config struct {
    Root string `toml:"root"`
}
```

`root` required。相对路径以 Runtime Image 启动时的 process working directory 为基准，但 `New` 必须立即转换为绝对路径；推荐生产配置使用绝对路径，避免 service manager 改变 working directory 后产生歧义。

`New` 顺序：

1. 检查 `root` 非空；
2. 使用 host filesystem 规则得到 clean absolute path；
3. 解析 root 自身 symlink，得到 canonical root；
4. `Stat` 确认目标存在且为 directory；
5. 保存 canonical root，不再保存或依赖原始相对 locator；
6. Context 在初始化过程中取消时返回 Context error。

每次 `New` 的实例拥有独立 root。Config 在返回后不被实现持有或修改。

## 5. SDK path grammar

有效路径：

```text
.
README.md
src/main.go
a/b/c
```

无效路径包括：

```text
""
/
/etc/passwd
C:/Windows
C:\Windows
../secret
a/../secret
./file
a/./file
a//b
a/
a\b
含 NUL 的字符串
```

验证规则：

- path bytes 必须是 valid UTF-8；
- `/` 是唯一 separator；
- `.` 只允许作为完整 root path；
- 其他 path 的每个 segment 必须非空且不为 `.` 或 `..`；
- 拒绝 `\`，即使当前 host 是 Windows；
- 在转换为 host path 之前完成逻辑 path validation；
- Windows 还要拒绝 drive prefix、UNC、device namespace、alternate data stream 等 host-specific escape 形式。

建议 Plugin 定义可识别错误：

```go
var (
    ErrInvalidPath        = errors.New("invalid workspace path")
    ErrPathEscape         = errors.New("workspace path escape")
    ErrSymlinkUnsupported = errors.New("workspace symlink unsupported")
)
```

越界或 symlink policy 错误同时保留 `fs.ErrPermission`，使调用方可使用 `errors.Is` 进行统一处理。

## 6. Symlink 与边界策略

### 6.1 v0.1 决策

除 `New` 对 root 自身进行一次 canonicalization 外，v0.1 拒绝操作路径中任意 segment 的 symlink，包括 final target。

理由：

- 单纯的 `EvalSymlinks` 加字符串前缀检查存在检查与使用之间的竞态；
- mutation operation 的 destination 可能尚不存在，需要单独验证 parent chain；
- Windows reparse point 与 Unix symlink 语义不同；
- 一个保守但明确的边界优于表面支持内部 symlink、实际可被替换绕过的实现。

解析每个 existing segment 时使用不跟随 symlink 的 metadata 操作。发现 symlink/reparse point 时返回包装 `fs.ErrPermission` 和 `ErrSymlinkUnsupported` 的错误。

后续版本若支持 workspace 内 symlink，应使用平台安全实现：Unix 采用 directory fd/openat 风格遍历，Windows 采用 handle-based final path validation。不得只放宽字符串检查。

### 6.2 External mutation

workspace 可能被 Plugin 外部进程并发修改。`New` 打开并持有 canonical root directory handle，所有 operation 在关键阶段重新验证 root identity；关键 mutation 继续使用平台原子 primitive。路径 ancestor 仍需在执行前验证，root identity 改变时 fail-closed。测试至少覆盖检查期间 root/path 被替换的场景。

## 7. Operation semantics

### 7.1 `ReadFile`

- target 必须是 regular file；
- 返回完整 bytes，ownership 交给 caller；
- 不缓存返回 slice；
- 读取过程中观察 Context。标准阻塞文件 I/O 无法直接取消时，在每个可分段边界检查 Context，并避免启动无法回收的 goroutine。

### 7.2 `WriteFile`

- parent 必须已存在且为 directory；
- target 不存在时创建，存在 regular file 时完整替换；
- 不创建 parent；
- input bytes 仍由 caller 持有，返回后不得引用；
- 写入同目录临时文件，设置请求 mode，写完并 close 后使用平台原子 replace；
- 操作失败时清理本次创建的临时文件；
- 不允许 target 或 parent chain 为 symlink；
- 成功返回后新的 `ReadFile` 只能看到完整新内容。

是否对文件和 parent directory 执行 `Sync` 属于 durability policy，不是 SDK v0.1 的统一保证；本插件首版不承诺 power-loss durability，但必须承诺进程内 atomic visibility。

### 7.3 `ReadDir`

- path 必须是 directory；
- 只返回 direct children；
- 按 `DirEntry.Name()` 的 UTF-8 bytes 升序；
- 返回 slice ownership 交给 caller；
- 目录中的 symlink entry 可以作为 metadata 被列出，但任何以该 entry 为 target 的后续操作按 symlink policy 拒绝。

### 7.4 `Stat`

- 对有效、非 symlink target 返回 `fs.FileInfo`；
- symlink target 按 v0.1 policy 拒绝，不跟随；
- 保留 `fs.ErrNotExist` 和 `fs.ErrPermission`。

### 7.5 `MkdirAll`

- `.` 已存在时成功；
- 创建缺失 ancestor；
- existing ancestor 必须为 directory 且不是 symlink；
- 任一位置存在 regular file 时返回保留 `fs.ErrExist` 或对应平台错误链的错误；
- 并发调用创建相同目录时，只要最终为 directory，均可成功。

### 7.6 `Remove`

- 只删除 regular file 或 empty directory；
- 不递归；
- symlink 拒绝；
- non-empty directory 返回底层可识别错误；
- 不存在返回保留 `fs.ErrNotExist` 的错误。

### 7.7 `Rename`

- source 必须存在且不是 symlink；
- destination parent 必须存在且不是 symlink；
- destination 必须不存在；
- 不创建 parent，不允许覆盖；
- destination 已存在时必须满足 `errors.Is(err, fs.ErrExist)`；
- source 与 destination 都位于同一 workspace，因此正常情况不存在跨 filesystem rename；
- 检查 destination 后再调用普通 `os.Rename` 存在竞态，必须使用平台 no-replace primitive 或等价原子策略。

## 8. 并发、Context 与 Cleanup

- 实例不维护 current working directory；所有操作只依赖 immutable canonical root；
- 不使用覆盖整个 FS 的全局 mutex；独立路径允许并发；
- atomic replace 和 no-replace rename 依赖操作系统原子语义；
- Context 在路径解析、分段读取/写入及提交前检查；取消后停止继续处理并保留 Context error；
- v0.1 不启动后台任务，但保留 canonical root directory handle 作为 workspace identity anchor；成功 `New` 返回 Cleanup 负责关闭该 handle。

不得通过“为支持 Context 而每次启动一个 goroutine 包装 os 调用”的方式实现，因为取消后无法停止的 goroutine 会脱离实例生命周期。

## 9. Manifest

```toml
manifest_version = 1
name = "filesystem.local"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

Plugin 不声明 `[state]`。workspace 是用户数据，不是 Plugin-owned persistent State。

## 10. 测试方案

### 10.1 Path table

- 所有有效和无效 grammar；
- Unix absolute、Windows drive/UNC/device path；
- UTF-8、NUL、反斜杠、duplicate slash、trailing slash；
- root `.` 与非 root dot segment。

### 10.2 Boundary 与 symlink

- root 自身为 symlink时 canonicalize；
- file、directory、ancestor symlink 全部拒绝；
- symlink 指向 root 内和 root 外均按 v0.1 policy 拒绝；
- destination parent symlink；
- path 在验证与操作之间被替换时不得越界。

### 10.3 Operation

- Read/Write round trip；
- Write create 和 atomic replace；
- caller 修改 input slice 不影响已写内容；
- ReadDir direct children 与 bytewise order；
- MkdirAll 幂等与并发；
- Remove file、empty directory、non-empty directory；
- Rename success、missing parent、destination exists；
- `errors.Is` 对 `fs.ErrNotExist`、`fs.ErrExist`、`fs.ErrPermission` 有效。

### 10.4 Concurrency

- 多 goroutine 读取；
- 不同文件并发写；
- reader 不观察到 WriteFile 的临时或部分内容；
- no-replace Rename 竞争只有一个成功；
- `go test -race ./...`。

测试只使用 `t.TempDir()` 内路径，不读取或写入真实用户 workspace。

## 11. 验收标准

1. 完整实现 `filesystem.FS`；
2. path validator 使用平台无关逻辑 grammar；
3. 所有 symlink traversal 在 v0.1 fail-closed；
4. `WriteFile` whole-file visibility 和 `Rename` no-overwrite 经并发测试证明；
5. `io/fs` sentinel error chain 保留；
6. 实现不依赖 process current directory（`New` 完成 root 解析后）；
7. 无 goroutine 或文件句柄泄漏。

## 12. v0.1 实现决策

- module path 为 `github.com/ingot-agent/filesystem-local`；
- workspace 内外的 symlink 均 fail-closed，root 在 `New` 时 canonicalize；
- `New` 持有 canonical root directory handle，并在操作关键阶段验证 root identity；
- Windows、Linux 和 macOS 提供平台原子 rename 适配；其他平台使用保守 fallback，正式支持矩阵由 Runtime Image 发布策略确定；
- `WriteFile` 保证同目录 replace 的 whole-file visibility，不承诺断电持久性；
- Windows file mode 使用 `os.Chmod` 的平台 best-effort 语义。
