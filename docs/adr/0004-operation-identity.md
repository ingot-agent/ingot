# ADR 0004：Operation 身份与同名处理

- 状态：Frozen
- 里程碑：M0（定稿）/ M1（落地）
- 相关：`sdk/operation/operation.go`、`plugins/app-webui/app/operations.go`、`plugins/app-webui/app/http.go`、`web/app-webui/src/views/OperationsView.vue`

## 背景

Operation 由 Host 聚合后统一展示：

```go
// plugins/app-webui/app/server.go:39
Operations []operation.Operation
```

`operationController` 以 name 为 map key：

```go
// plugins/app-webui/app/operations.go:42-46
if _, exists := c.entries[definition.Name]; exists {
    return nil, fmt.Errorf("duplicate operation %q: ...", definition.Name)
}
c.entries[definition.Name] = ...
```

当前无任何 Plugin 提供 Operation（`grep` 确认无 provider），因此改动的现状成本为零。

随着生态增长会出现两个需求：

1. 两个 Plugin 各自导出同名 Operation（例如都叫 `config`）；
2. 前端在 Operation 很多时需要分组显示。

## 决策

### 1. Definition 增加分组字段

`operation.Definition` 增加一个由 Plugin 自行填写的分组标识，用于前端分组显示。

- 该字段是**纯业务语义**，由 Plugin 决定取值；
- SDK 只提供槽位，不解释其含义；
- Builder 不参与，也不理解该字段；
- 允许为空，落入「未分组」桶。

### 2. 同名 Operation 共存，用内部 ID 区分

同名 Operation **不拒绝、不覆盖**，而是共存：

```text
对外显示：  Definition.Name（可能重名）
内部标识：  Host 聚合时生成的哈希 / 内部 ID
内部索引：  map[internalID]operation
```

- 收集到所有 Operation 时，Host 内部生成唯一 ID 用于区分；
- 对外展示保持原样（显示 name，按分组字段归类）；
- 前端调用与取消改为使用内部 ID，不再用 name 作为唯一标识。

这不是模型问题，而是「用 name 作 map key 导致同名互相覆盖」的缺陷。

### 3. 冲突检查不做大动干戈

**不**为此引入 Builder 参与、复合命名空间 key、跨插件唯一性校验等机制。
Builder 完全不知道 Operation 的存在（`internal/builder/*.go` 无任何 operation 引用），
不应为这个缺陷承担业务语义。

## 理由

- 用 name 当唯一 key 只是实现细节；同名共存是更宽容也更简单的结果。
- 让 Builder 参与会违反「Builder 不干涉业务语义」的边界。
- 分组字段由 Plugin 填写，符合「Plugin 拥有业务语义」的分工。

## 后果

- M1 需要修改：`operations.go` 的 `entries` key 改为内部 ID；
  `http.go:47,384` 的 `POST /api/operations/{name}` 路径参数改为 ID；
  `runtime.ts:237` 的 `invoke` 与 `OperationsView.vue:73` 的下拉 value 改用 ID，
  显示仍用 name。
- 需要定义内部 ID 的生成方式（哈希输入的稳定性、跨重启是否可复用）。
- 前端需要处理「两个同名 Operation」的展示歧义（例如附带分组或来源提示）。

## 不冻结

- 分组字段的具体名称与校验规则（M1 在 SDK 内定稿）；
- 内部 ID 的生成算法与稳定性要求（M1）；
- 前端同名 Operation 的具体去歧义展示方式（M1/M6）。
