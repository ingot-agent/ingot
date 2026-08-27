# ingot `builder.toml` v0.1 设计方案

> 状态：Implemented
> 目标文件：`~/.ingot/builder.toml`
> 关联文件：`plugins.toml`、`plugins.lock`

## 1. 定位

`builder.toml` 保存 Builder 的可部署配置。目前配置面只包含生成 Runtime
Image 所需的有序 SDK module 列表，从而避免 SDK identity 与版本写死在
Builder 代码中。`ingot init` 写入默认文件；旧 home 未包含该文件时使用
Builder 内嵌的同一份发行默认配置。

## 2. Schema

```toml
builder_config_version = 1

[[sdks]]
module = "github.com/ingot-agent/sdk"
version = "v0.1.4"

[[sdks]]
module = "example.com/acme-sdk/v2"
version = "v2.3.0"
path = "../acme-sdk"
```

| Field | Required | 语义 |
|---|---:|---|
| `builder_config_version` | Yes | schema major，固定为 `1` |
| `sdks` | Yes | 非空、有序且 module 唯一的 SDK 列表 |
| `sdks[*].module` | Yes | canonical Go module path |
| `sdks[*].version` | Yes | 与 module path 匹配的 exact canonical Go module version |
| `sdks[*].path` | No | 本地开发 checkout；相对路径以 `builder.toml` 所在目录为基准 |

第一项是 primary SDK：生成代码使用它的 runtime helper 类型保存统一 Cleanup
链、解析 Plugin Config。每个已配置 SDK 都必须提供标准的 root、`config` 与
`application` package；Builder 会同时：

- 识别各 SDK 的 `Optional[T]`、`Named[T]` 与 `Cleanup`；
- 为各 SDK 注入 `application.Process` 与 Plugin state directory context；
- 将不同 SDK 的 Cleanup 转换到 primary Cleanup 链并保持严格逆序清理；
- 把所有 SDK 的 selected version/replacement 写入 lock 和 ImageID。

同一 module path 不能重复声明；Go MVS 对一个 module path 只能选择一个版本。
需要并行使用不兼容 major 时，它们必须具有不同 semantic import path（如
`example.com/sdk` 与 `example.com/sdk/v2`）。

## 3. 环境变量覆盖

环境变量在 TOML 解析、严格校验后应用，覆盖结果再次完整校验：

| 环境变量 | 语义 |
|---|---|
| `INGOT_BUILDER_SDKS` | 以逗号分隔的 `module@version` 项整体替换列表 |
| `INGOT_BUILDER_SDK_MODULE` | 覆盖第一项的 module |
| `INGOT_BUILDER_SDK_VERSION` | 覆盖第一项的 version |

整体列表覆盖不能与首项覆盖混用。环境变量只表达 remote SDK；需要本地源码时
使用 `path` 或最近 `go.work` 中匹配该 module 的无版本 `replace`。

## 4. 确定性与锁定

SDK 声明顺序有语义，进入 `plugins.lock` 的 `[[sdks]]` 与 canonical build
manifest。远程 SDK 是完整 locked module graph 的节点；本地 SDK 通过
`[[replacements]]` 保存 absolute locator、selected exact version 与
`ModuleSourceDigest`，构建时复制到 staging 后使用相对 `replace` 编译。
