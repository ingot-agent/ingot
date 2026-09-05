# `tool.edit` Plugin v0.1 设计方案

## 1. 定位

`tool.edit` 在 `filesystem.FS` 之上提供面向 Agent 的确定性文本编辑 primitive。它不访问 `os` package，不知道 workspace 的主机路径，也不复制 filesystem provider 的路径安全逻辑。

```text
Agent -> fs_edit -> filesystem.FS -> filesystem provider -> Workspace
```

它与 `tool.fs` 共享 `filesystem.FS` capability，但不依赖 `tool.fs` Component。默认 Coding Agent profile 同时安装两者，使模型能够遵循 `READ -> EDIT` 工作流。

## 2. Component Contract

```go
type Config struct {
    MaxFileBytes int `toml:"max_file_bytes"`
}

type Dependencies struct {
    Filesystem filesystem.FS
}

type Exports struct {
    Tools []tool.Tool
}
```

`New` 导出一个工具 `fs_edit`，无长期资源，Cleanup 为 nil。`MaxFileBytes` 省略时默认为 1 MiB，负数无效。

## 3. Tool API

Input Schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["path", "old_text", "new_text"],
  "properties": {
    "path": {"type": "string", "minLength": 1},
    "old_text": {"type": "string", "minLength": 1},
    "new_text": {"type": "string"},
    "replace_all": {"type": "boolean", "default": false}
  }
}
```

Go 参数中的 `new_text` 使用 `*string`，以区分缺少字段与显式空字符串。空 `new_text` 表示删除；空 `old_text` 和 `old_text == new_text` 均拒绝。

成功结果是 JSON 文本：

```json
{
  "path": "internal/config.go",
  "replacements": 1,
  "bytes_before": 1824,
  "bytes_after": 1832
}
```

## 4. 匹配语义

编辑执行 exact UTF-8 byte-sequence replacement，不做 trim、换行转换、缩进修复、Unicode normalization 或 fuzzy matching。

默认：

```text
0 match  -> ErrNoMatch
1 match  -> replace
>1 match -> ErrAmbiguousMatch
```

显式 `replace_all=true` 时，零匹配仍失败，一个或多个匹配全部替换。计数和替换采用 Go `strings.Count` / `strings.Replace` 的不重叠匹配语义。

## 5. 执行与大小边界

```text
validate context and arguments
-> Stat and regular-file check
-> Stat.Size preflight
-> ReadFile
-> actual input byte-length check
-> UTF-8 validation
-> projected output byte-length check
-> apply edit in memory
-> context check
-> one WriteFile call
-> JSON result
```

Stat 预检避免正常情况下读取已知超大文件，读取后的长度检查处理 metadata 不准确或 Stat/Read 之间的变化。在构造替换结果前，使用匹配数量和新旧片段长度计算预计输出大小；超过 `MaxFileBytes` 时拒绝，避免 `replace_all` 产生超大内存分配。大小计算必须避免整数溢出。

由于当前 `filesystem.FS.ReadFile` 返回完整 `[]byte`，V1 仍不宣称对恶意并发增长文件提供严格的读取内存硬上限。

任一写前错误都不得调用 `WriteFile`。

## 6. 权限与原子性

插件将 `Stat` 得到的 `info.Mode().Perm()` 传给 `WriteFile`。这只保证表达 `fs.ModePerm`，不保证 ACL、xattr、owner/group、特殊 mode bits、mtime 或文件 identity。

插件只保证验证成功后发起一次 `WriteFile`。当前 `filesystem.local` 使用临时文件和原子替换，但 `filesystem.FS` interface 不要求任意 provider 都提供相同的原子性。

`Stat -> ReadFile -> WriteFile` 不是 CAS transaction。V1 不提供跨 writer 的事务隔离或 lost-update protection，也不为此修改 SDK。

## 7. 错误

公开 sentinel：

```go
ErrInvalidConfig
ErrInvalidArguments
ErrBinaryContent
ErrFileTooLarge
ErrNoMatch
ErrAmbiguousMatch
```

实现使用 `%w` 保留 filesystem、context、`io/fs` 及插件 sentinel 的错误链。错误包含 logical path 和必要的匹配数量，但不回显完整文件内容。

## 8. Approval

`tool.edit` 不依赖 `interaction`。审批由现有 Tool Interceptor 处理，可按工具名配置：

```toml
[[plugins."interceptor.approval".rules]]
tool = "fs_edit"
action = "ask"
```

## 9. V1 非目标

- 新文件创建；
- Unified Diff；
- MultiEdit；
- regex/fuzzy edit；
- AST/language-aware edit；
- SDK CAS/Revision 扩展。

后续 MultiEdit 应采用 `Read once -> apply N edits in memory -> all succeed -> Write once`，而不是连续产生部分写入。

## 10. 验收

- 唯一替换、删除、多行 Unicode、replace-all；
- zero/ambiguous/non-overlapping match；
- strict JSON、必填 `new_text`、非法 UTF-8、no-op；
- regular-file、输入双重大小检查、输出大小预检、ModePerm 传递；
- Stat/Read/Write/context 错误链；
- 所有写前失败无写入副作用；
- 默认 bundle/profile、生成配置和 Component Graph 构建通过。
