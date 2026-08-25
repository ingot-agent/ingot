# `tool.fs` Plugin v0.1 设计方案

> 状态：Implemented v0.1
> Component：`default`  
> Dependencies：`filesystem.FS`  
> Exports：`[]tool.Tool`

## 1. 定位

`tool.fs` 将 `filesystem.FS` 适配为模型可调用的 text-oriented file tools。它不访问 `os` package，不知道 workspace 的主机路径，所有边界和 `io/fs` 语义由依赖的 FS Provider执行。

```go
type Dependencies struct {
    Filesystem filesystem.FS
}

type Exports struct {
    Tools []tool.Tool
}
```

## 2. Config

```go
type Config struct {
    MaxReadBytes   int `toml:"max_read_bytes"`
    MaxListEntries int `toml:"max_list_entries"`
    FileMode       int `toml:"file_mode"`
    DirectoryMode  int `toml:"directory_mode"`
}
```

默认：读取 1 MiB、目录 1000 项、文件 mode `0644`、目录 mode `0755`。Mode 使用十进制 TOML integer，`New` 校验只包含 permission bits。限制必须为正数。

## 3. 导出工具

按以下固定顺序导出，顺序进入 MANY aggregation：

| Name | Arguments | Result |
|---|---|---|
| `fs.read` | `path` | UTF-8 文件内容 |
| `fs.write` | `path`、`content` | 写入成功摘要 |
| `fs.list` | `path` | 按 entry name UTF-8 bytes 升序的 compact JSON array |
| `fs.stat` | `path` | compact JSON metadata |
| `fs.mkdir` | `path` | 创建成功摘要 |
| `fs.remove` | `path` | 删除成功摘要 |
| `fs.rename` | `source`、`destination` | rename 成功摘要 |

所有 Input Schema 使用 `additionalProperties:false`，路径为非空 string。`fs.write` 只接受 text content；v0.1 不支持 base64/binary。`fs.read` 遇到 invalid UTF-8 返回 `ErrBinaryContent`，不进行有损替换。

`fs.list` 每项 exact shape：

```json
{"name":"main.go","type":"file"}
```

type 为 `file`、`directory`、`symlink` 或 `other`。超过配置限制时返回 `ErrResultLimit`，不返回会被误认为完整目录的部分列表。

`fs.stat` 输出 name、size、mode、modified_at、type；时间为 UTC RFC3339Nano。输出字段和顺序由固定 struct 产生。

## 4. 调用语义

- 每个 Tool 直接把 `Invoke` Context 传给 `filesystem.FS`；
- 不对 path 做 host normalization，也不把 `\` 改写为 `/`；
- FS error 用 `%w` 包装并保留 `fs.Err*`；
- `fs.read` 在返回 Result 前检查 size limit；当前初版 SDK 的 `ReadFile` 是 whole-file API，因此该限制是结果限制而不是 provider 内存上限，不能在不改变 SDK Contract 的前提下伪装成 bounded read；
- `fs.write` 在调用返回前将 JSON argument decode 到独立 string/bytes，不保留 Arguments；
- `fs.remove` 不提供 recursive option；
- `fs.rename` 不提供 overwrite option；
- Tool 本身无 mutable state并可并发调用；成功 `New` 返回 nil Cleanup。

## 5. Manifest

```toml
manifest_version = 1
name = "tool.fs"
ingot = ">=0.3.0 <0.4.0"
config_package = "."

[[components]]
name = "default"
package = "."
```

## 6. 测试与验收

- `Exports.Tools` 顺序、名称唯一和 JSON Schema golden；
- 使用 fake `filesystem.FS` 验证 Context 和参数原样传递；
- UTF-8/binary、read/list limit；
- deterministic JSON result；
- remove 不递归、rename 不覆盖；
- `errors.Is` 保留 `fs.ErrNotExist/Exist/Permission`；
- 并发调用无共享 buffer race；
- 不直接导入或调用 host `os` filesystem。

在 SDK 增加 ranged/limited read 与 directory pagination 之前，`max_read_bytes` 和 `max_list_entries` 只能保证不会返回部分结果；它们不保证 Provider 在返回前不会先加载超限数据。

待确认：大文件是否需要新的 ranged-read Capability；v0.1 不在 Tool 层实现隐式分页或截断。
