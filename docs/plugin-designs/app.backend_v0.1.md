# `app.backend` Plugin 设计方案

> 状态：Implemented
> Component：`host`、`app`（composite Plugin，两个 Component）
> Exports：`host` 提供 `interaction.Channel`、`interaction.ExecutionBinder`、
> `appbackend.Runtime` 与 host observation；`app` 是 graph leaf（HTTP/SSE
> browser workspace）

## 1. 定位

`app.backend` 是面向信任的本地单用户 Browser workspace。它把前端（Vue +
Tailwind，构建后嵌入 Runtime Image）暴露在本地 HTTP/SSE 地址上，作为 Session
Workspace 的 Application ingress：浏览器不是 Workspace authority，它只是通过
Application DTO 选择并创建绑定。

## 2. Workspace selection ingress

Session 创建流程：

```text
用户选择 Workspace（绝对本地目录）
        ↓
验证输入（非空、绝对路径、路径存在且为目录）
        ↓
session.Store.Create
        ↓
workspace.Manager.Assign
        ↓
Session ready
```

`Assign` 失败时，Application 使用不继承请求取消、但有固定超时的 cleanup
context 删除刚创建的 Session。若补偿也失败，返回值同时保留 Assign 与 Delete
错误，避免把孤儿 Session 静默隐藏。这仍是 Application-level compensation，
不是跨 capability 的分布式事务；底层 `session.Store` 不会因此变成
Workspace-aware。

schema v1 升级产生的历史 Session 可能暂时没有 Binding。Application 仍投影这些
Session，并由前端引导用户选择目录；`POST /api/sessions/{id}/workspace` 只允许完成
一次 Binding。绑定前创建 Turn 返回 `workspace_not_assigned`，因此未绑定 Session
不会进入执行路径。

## 3. Session projection

Session UI 需要同时看到 Session 元数据与 Workspace。WebUI backend 组合
`session.Manager.Get`（/ `Query.List`）与 `workspace.Resolver`，生成
Application-level `SessionView`（id、title、workspace、lifecycle metadata），
不把 Workspace 塞进 SDK 的 `session.Metadata`。

只有 `workspace.ErrNotAssigned` 会被投影为空 Workspace，供上述迁移流程处理；
resolver 的其他错误会直接向调用方传播。Rename、Archive、Restore 与 Fork 返回的
projection 都保留 Workspace，其中 Fork 继承源 Session 的 Binding。

## 4. 执行路径

```text
app.backend
    ↓
agent.Turn{SessionID}
    ↓
tool.Invocation{Scope:{SessionID}}
    ↓
tool.shell
    ↓
workspace.Resolver.Resolve(Scope)
    ↓
session.sqlite session_workspaces
```

## 5. Interaction execution scope

普通 `interaction.Channel` 只表达全局 Host effect。Tool execution 中的
Interaction 通过静态依赖 `interaction.ExecutionBinder` 与动态
`tool.Invocation.Scope` 组合：

```text
tool.Invocation.Scope
        ↓
interaction.ExecutionBinder.Bind
        ↓
Channel（binding immutable）
        ↓
Request / Emit / Set / Clear
```

bound Scope 的 SessionID 是业务 routing 的唯一 authority。context 中的
Observation correlation 仅在 SessionID 匹配时补充 TurnID、ToolCallID 与
RoundIndex，不能提供、覆盖或改变 Session routing；即使 correlation 缺失，pending
Interaction 仍然可以被正确的 Session UI 查询和响应。
