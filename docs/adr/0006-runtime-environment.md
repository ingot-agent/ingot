# ADR 0006：Runtime 环境变量收敛

- 状态：Frozen
- 里程碑：M0（定稿）/ M1（落地）
- 相关：`internal/builder/generate.go`、`internal/builder/build.go`、`internal/home/home.go`

## 背景

生成的 runtime 当前读取三个环境变量：

```go
// internal/builder/generate.go:307-319
INGOT_HOME          // Ingot Home，用于推导 config 与 state 的默认位置
INGOT_CONFIG        // config.toml 路径
INGOT_STATE_ROOT    // state 根目录
```

其中 `INGOT_CONFIG` 随统一 Runtime 配置一并被 ADR 0003 移除，
`INGOT_STATE_ROOT` 的语义被 ADR 0002 的 Runtime Home 取代。

## 决策

Runtime 侧的环境变量收敛为**一个**：

```text
INGOT_RUNTIME_HOME   # 可选，覆盖 Runtime Home 位置
```

- `INGOT_STATE_ROOT` **直接移除**，不保留废弃别名；
- `INGOT_CONFIG` 移除；
- `INGOT_HOME` 不再属于 Runtime contract：Ingot Home 只服务 Builder 与 CLI，
  Runtime 不读取它；
- **完全移除，不做兼容**（项目尚未发布，无存量用户）。

需要同步处理三处注入点：

| 位置 | 现状 | M1 处理 |
|---|---|---|
| `generate.go:307-319` | 读取三个变量 | 只读 `INGOT_RUNTIME_HOME` |
| `home.go:RunCurrent` | 注入 `INGOT_HOME` | 注入 `INGOT_RUNTIME_HOME` |
| `build.go:217` | 注入 `INGOT_STATE_ROOT`/`INGOT_CONFIG` 做 pre-check | 注入临时 `INGOT_RUNTIME_HOME` |

## 理由

- 一个变量对应一个概念（Runtime Home），避免同一状态有多个入口。
- 不做兼容是因为没有存量用户，保留别名只会长期增加维护面。

## 后果

- 这是**破坏性**变更，必须三处同步修改，否则 pre-check 或派发会失效。
- 现有依赖 `INGOT_HOME` 的文档（README、USAGE）需要同步更新。

## 不冻结

- `INGOT_RUNTIME_HOME` 之外是否还需要其它 Runtime 侧变量（M1 按需）。
