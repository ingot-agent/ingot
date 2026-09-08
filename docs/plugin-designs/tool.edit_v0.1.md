# `tool.edit` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Dependencies：`workspace.Resolver`
> Exports：`[]tool.Tool`

## 1. 定位

`tool.edit` 向 Agent 提供三个 workspace 文件工具：`edit_file`（精确 UTF-8
文本替换）、`read_file`（读取文本文件）、`search`（字符串搜索）。三者都以
session workspace 内的相对路径为基准，避免模型经由 shell 处理文件时遇到参数
转义、命令拼接等脆弱性。

工作目录 authority 只来自 Session Workspace：每次 `Invocation` 携带显式
`execution.Scope`，`tool.edit` 通过 `workspace.Resolver` 解析该 Session 的
Binding，并以其 `Root` 作为文件路径基准。没有 process cwd、config
`working_directory`、HOME 或其他 ambient fallback。

工具只处理文本读写/搜索语义，清单与目录枚举等通用能力不在范围内，也不必绕过
`tool.runtime` 调用；是否允许执行由 `tool.runtime` 中的 approval/policy
Interceptor 决定。

## 2. Component Contract

```go
type Dependencies struct {
    Workspace workspace.Resolver
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

v0.1 导出三个 tool，按顺序为 `edit_file`、`read_file`、`search`。

> 说明：顶层架构设计（`docs/ingot_架构设计_v0.3.md`）将 `tool.edit` 描述为
> 消费 `filesystem.FS`。SDK 目前尚未实现 `filesystem.FS` 契约（仓库中也没有
> `sdk/filesystem` 包），而 `workspace.Resolver` 是已经落地的
> Session-scoped 工作目录契约（`tool.shell` 亦依赖它）。因此 v0.1 实现直接消费
> `workspace.Resolver`；待 `filesystem.FS` 契约落地后可平滑迁移，无需改动三个
> tool 的公共 Definition 或行为语义。

## 3. Config

```go
type Config struct {
    MaxFileBytes int `toml:"max_file_bytes"`
    MaxScanBytes int `toml:"max_scan_bytes"`
}
```

- `workspace.Resolver` 为 required dependency，缺少时 `New` 返回 Config Error；
- `max_file_bytes` absent/0 默认 1 MiB，必须 `> 0`；限制单个文件读入内存的大小，
  `read_file` 与 `edit_file` 对目标强制，`search` 用于跳过超过该边界的文件；
- `max_scan_bytes` absent/0 默认 4 MiB，必须 `> 0`；限制 `search` 一次递归
  扫描的累计字节预算，超过后停止扫描并明确标记 truncated。

## 4. 三个工具

### 4.1 `edit_file`

Input Schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["path", "old"],
  "properties": {
    "path": {"type": "string", "minLength": 1},
    "old": {"type": "string", "minLength": 1},
    "new": {"type": "string"},
    "replace_all": {"type": "boolean"}
  }
}
```

- `path` 是 workspace 相对路径；绝对路径、`..` 遍历被拒绝；
- `old` 必须是非空、valid UTF-8 的查找串；`new` 默认为空字符串，即删除匹配文本；
- `replace_all` 为 false（默认）时只替换第一处；为 true 时替换全部匹配。

Result 为确定性 text envelope：

```text
edited: <path>
replacements: <count>
bytes: <new file size>
```

- 业务结果（文件不存在、匹配文本不存在、文件是目录、文件超限、文件非 UTF-8、
  old==new 无操作、路径越界）作为普通 Tool Result 返回，携带 nil error；
- 替换后检查结果差异，确认内容确实变化，否则作为无操作结果返回；
- 原子写入：目标目录内创建临时文件，写入、chmod 原文件权限、sync 后 `os.Rename`
  覆盖，避免部分写入。

### 4.2 `read_file`

Input Schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["path"],
  "properties": {
    "path": {"type": "string", "minLength": 1},
    "start_line": {"type": "integer", "minimum": 1},
    "end_line": {"type": "integer", "minimum": 1}
  }
}
```

- `path` 必填，`workspace` 相对路径；
- `start_line` / `end_line` 可选，1-based、含边界，用于只读取一个行区间。两者都省略
  时返回全文；只给 `start_line` 表示读到文件末尾，只给 `end_line` 表示从第 1 行开始；
- 返回 Content 为单一 text part；`start_line`/`end_line` 均省略时返回字符串原文，
  指定行区间时按行拼接（省略终止换行），不输出行号前缀；
- 行区间规则：`start_line` 与 `end_line` 均须 `>= 1`，且 `start_line <= end_line`；
  `end_line` 超出文件行数时收窄到末行；`start_line` 超过文件行数时作为业务结果返回；
  文件末尾单个换行不额外计为一行；
- 业务结果：文件不存在、是目录、超限、非 UTF-8、路径越界、非法行区间。

### 4.3 `search`

Input Schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["pattern"],
  "properties": {
    "pattern": {"type": "string", "minLength": 1},
    "path": {"type": "string"},
    "glob": {"type": "string"}
  }
}
```

- `pattern` 必填；`path` 可选，为空时从工作区根目录递归搜索；`glob` 可选，用于
  按文件名模式过滤（Go `filepath.Match`）；
- 跳过隐藏文件/目录（dot 前缀，工作区根除外）、非普通文件、非 UTF-8 二进制内容、
  超过 `max_file_bytes` 的文件；
- 命中按文件相对路径 + 行号 + 行内容输出，排序确定；
- 累计扫描字节超过 `max_scan_bytes` 时停止，结果中显式标记 truncated；
- 业务结果：无匹配、路径不存在、路径非目录、路径越界、pattern 缺失。

## 5. 执行与生命周期

1. decode 并复制参数，校验 UTF-8、path 相对约束与 pattern 非空；
2. 通过 `t.workspace.Resolve(ctx, invocation.Scope)` 解析 Session Workspace Binding；
3. 解析目标绝对路径并验证其不越出 workspace root；
4. `edit_file`/`read_file` 对单文件 `Stat` + `ReadFile`；`search` 递归 `WalkDir`；
5. 文件必须是 valid UTF-8，否则业务结果；
6. 各阶段 before/after 检查 Context。

Context error 保留 `context.Canceled` / `DeadlineExceeded`；三工具均无后台任务，
成功 `New` 返回 nil Cleanup。

## 6. 安全与错误

- path 相对约束与 root 校验是路径卫生守卫，不是 filesystem sandbox；Workspace
  Binding Contract 明确不隐含 confinement、permission 或 path escape 保护。
- 不覆盖 shell 路径、环境变量或任何全局配置。
- 建议 sentinel：
  - `ErrInvalidConfig`：配置或依赖非法；
  - `ErrInvalidArguments`：参数缺失、非法 UTF-8、未知字段、绝对/越界 path；
  - `ErrUnsafePath`：path 越出 workspace root。

## 7. Manifest

```toml
manifest_version = 1
name = "tool.edit"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

## 8. 测试与验收

- 三个 Definition 名称、描述与 exact schema；
- `edit_file` 默认只替换第一处、`replace_all` 全替换、原子写入保留权限；业务结果路径；
- `read_file` 返回完整内容；`start_line`/`end_line` 区间（start-only、end-only、
  双边界含、单行、end 收窄、末尾换行不计为一、空文件）；非法行区间业务结果；
  文件不存在/目录/超限/非 UTF-8/越界业务结果；
- `search` 跨目录命中、默认根目录、glob 过滤、隐藏与二进制跳过、scan 上限
  truncated、无匹配/路径不存在/路径非目录/越界业务结果；
- 参数非法（缺失字段、空串、未知字段、错 call name）；
- 从 `Invocation.Scope` 解析 workspace，按 session 隔离；
- `New` 缺 resolver 报 Config Error；caller 取消传播 `context.Canceled`；
- 多 Session 并发与 race test。
