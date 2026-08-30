# ingot `builder.toml` v0.1 设计方案

> 状态：Implemented
> 目标文件：`~/.ingot/builder.toml`
> 关联文件：`plugins.toml`、`plugins.lock`

## 1. 定位

`builder.toml` 是 Builder 的稳定、可扩展配置入口。v0.1 暂无用户可调的构建
选项，但仍保留该文件和显式 schema version，以便后续增加配置时有明确的兼容
边界，而不需要重新引入新的入口文件。

ingot ABI 不是用户配置：Builder 固定其 module path 与 exact version。Agent SDK、
领域 SDK 和其他 Contract Module 也不在这里声明；它们由 Plugin `go.mod` 正常
引入，并以 Go Type Identity 参与 Component Graph。

## 2. Schema

v0.1 的完整配置为：

```toml
builder_config_version = 1
```

| Field | Required | 语义 |
|---|---:|---|
| `builder_config_version` | Yes | schema major，固定为 integer `1` |

解析使用 strict mode：未知字段、未知 table、类型错误以及不支持的 schema version
都会产生 Builder Config Error。旧 `[[sdks]]` 配置和
`INGOT_BUILDER_SDKS*` 环境变量覆盖不再受支持。

## 3. 生命周期

- `ingot init` 写入默认 `builder.toml`；
- 未创建该文件的旧 home 使用 Builder 内嵌的同一份默认配置；
- `resolve` 与 `apply` 始终读取并校验该文件，即使当前没有可调选项；
- `--force` 初始化可以重写默认文件，普通初始化保留用户已有文件。

## 4. 后续扩展规则

新增配置必须：

1. 对所有 Plugin 一视同仁，不按官方 Plugin identity 特判；
2. 明确默认值、顺序语义以及是否进入 Canonical BuildManifest/ImageID；
3. 继续严格拒绝未知字段；
4. 在破坏兼容时提升 `builder_config_version`；
5. 若需要约束任意 Go module，设计通用 module constraint，而不是恢复
   primary/configured SDK 概念。
