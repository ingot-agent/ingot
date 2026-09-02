# Ingot Agent 基础能力路线图

## 背景

当前 `ingot` 的 Agent 基础能力已经从“为了让 demo 跑起来”逐步演进到一个相对完整的执行内核：具备 Turn 执行、Model 调用、Tool Loop、Session 持久化、流式输出、多模态输入、Context Compaction、Tool 审批与中断恢复等能力。

接下来的目标不应继续以“CLI 缺什么就临时补什么”为驱动，而应该先明确 Agent Execution Core 的完整边界，把关键执行语义补齐并稳定下来，再基于这些能力重新构建 `app.cli`。

这一阶段的核心目标是：

> 建立一套足够稳定、可观察、可控制、可推理的 Agent Execution Model，使上层 Application 不再需要通过临时约定或私有 workaround 弥补底层语义缺口。

---

## 总体路线

```text
Milestone 1 — Agent Execution Model
        ↓
Milestone 2 — Execution Observation
        ↓
Milestone 3 — Execution Semantics
        ↓
Milestone 4 — Execution Outcome
        ↓
──────── Agent Core 基本封口 ────────
        ↓
Milestone 5 — Session Management
        ↓
Milestone 6 — app.cli v2
```

其中：

- Milestone 1–4 属于 Agent Execution Core；
- Milestone 5 属于 Application-facing 基础能力；
- Milestone 6 用于验证整个 Application Surface 是否成立。

---

# Milestone 1 — Agent Execution Model

## 目标

补全当前 Agent execution hierarchy，正式引入 `Round` 这一语义层级，并提供 Round-level interception。

当前已经存在三个较明确的执行粒度：

```text
Turn
Model Invocation
Tool Call
```

但真实的 Agent 执行过程实际上是：

```text
Turn
  ├── Round 0
  │    ├── Model Invocation
  │    └── Tool Calls
  ├── Round 1
  │    ├── Model Invocation
  │    └── Tool Calls
  └── Round N
       └── Final Model Invocation
```

因此当前真正缺失的不是单纯的“另一个 interceptor”，而是 `Round` 作为 Agent execution semantics 的正式存在。

## 需要明确的执行层级

未来 Agent Core 应明确以下四层：

```text
Turn
  └── Round
       ├── Model Invocation
       └── Tool Call *
```

### Turn

一次用户发起的完整 Agent execution。

例如：

```text
agent.Run(...)
agent.Stream(...)
```

### Round

一次模型决策以及由该决策产生的 Tool 执行。

典型结构：

```text
Model Invocation
    ↓
Assistant Decision
    ↓
0..N Tool Calls
```

如果模型直接产生最终回答，则该 Round 没有 Tool Call，并成为 Turn 的最终 Round。

### Model Invocation

一次具体的模型请求。

它具有明确的 Provider、Model、Request、Response、Usage 等语义。

### Tool Call

一次具体工具执行。

它具有独立的参数、风险、审批、结果、错误和 side effect semantics。

## Round Interceptor

现有 interceptor 应理解为不同 scope 上的 control plane：

```text
Turn          → agent.Interceptor
Round         → Round Interceptor
Model         → model.Interceptor
Tool Call     → tool.Interceptor
```

Round Interceptor 的价值不是替代 Tool Interceptor。

例如：

```text
识别单个 shell call 是否危险
```

仍然属于 Tool Call scope。

而下面这种情况才属于 Round scope：

```text
这一轮模型同时请求：

rm ...
git reset --hard ...
git push --force ...
```

单个 Tool Call 的风险判断与整个 Round 的组合风险不是同一个问题。

Round-level interception 应允许 policy 在 Tool 真正执行前看到完整的模型决策和 Tool Call 集合。

# Milestone 2 — Execution Observation

## 目标

建立独立于 Interceptor 的 Observation 机制。

必须明确区分：

```text
Interceptor = Control Plane
Observer    = Observation Plane
```

Interceptor 用于：

- inspect；
- modify；
- reject；
- short-circuit；
- enforce policy。

Observer 用于：

- logging；
- CLI rendering；
- tracing；
- metrics；
- audit；
- debugging；
- evaluation。

Observer 不应改变执行结果。

## 为什么必须独立

如果继续使用 interceptor 顺便承担 observation，长期会形成：

```text
security policy
logging
UI spinner
metrics
telemetry
debug output
```

全部混在同一 middleware chain 中。

这会使执行顺序、side effect 和 failure behavior 越来越难以推理。

因此 Observation 应作为独立的一等能力存在。

## Observation 覆盖范围

Observation 应覆盖完整 execution hierarchy：

```text
Turn
Round
Model Invocation
Tool Call
```

至少应能表达：

```text
Turn Started
Round Started

Model Started
Model Progress
Model Completed
Model Failed

Tool Started
Tool Completed
Tool Failed

Round Completed
Turn Completed
Turn Failed
Turn Canceled
```

Streaming delta 可以视为 progress event 的一种特殊形式。

## Correlation

Observation 必须天然支持 execution correlation。

至少需要能够关联：

```text
Turn ID
Round ID / Round Index
Model Invocation ID
Tool Call ID
```

从而允许上层看到完整执行树：

```text
Turn #42
  Round #0
    Model Invocation
      reasoning...
      output...
    Tool shell_exec
      started
      completed

  Round #1
    Model Invocation
      ...
```

## 与 Interaction 的边界

`interaction.Channel` 仍然属于 Host Interaction，而不是 Agent Observation。

例如：

```text
approval request
ask_user
structured input
host state
```

这些事情可能发生在 Agent execution 中，但不应该因此重新塞回 Agent event bus。

Observation 与 Interaction 可以通过 execution correlation 建立关联，但仍保持独立 contract。

---

# Milestone 3 — Execution Semantics Stabilization

> 状态：Implemented（2026-09-02）

## 目标

对已经存在的执行能力进行语义收口。

这一阶段重点不是增加 feature，而是确保上层 Application 可以安全依赖 Agent contract。

系统已经收口四类语义：

```text
Capability
Cancellation
Failure
Durability
```

## Streaming Semantics

Streaming fallback固定在Agent内部完成，Application不需要以新的Run重试同一Turn。

理论上 Application 很容易写成：

```text
Stream()
  ↓
ErrStreamingUnsupported
  ↓
fallback Run()
```

但如果 `Stream()` 在发现 Provider 不支持 streaming 前已经产生 durable mutation，例如已经写入 User Message，那么 fallback `Run()` 就可能重复执行同一个 Turn。

最终规则：不建立capability preflight shadow system；缺少Streaming依赖时直接Complete。只有`model.Stream`返回`model.ErrStreamingUnsupported`且尚未交付任何Agent Event时，才允许在同一Round以相同Request fallback到Complete。其他streaming error和任何已交付Event后的错误都立即停止；Event始终是transient progress，只有`err == nil`的Result才是canonical。

## Cancellation Semantics

不同阶段取消统一遵循forward-only语义：

```text
before model
during model
between model and tools
during tool
after tool side effect
during persistence
```

Cancellation停止未来工作但不回滚已完成的durable mutation或external side effect，也不表示retry safe。in-flight operation若没有authoritative outcome，其结果按unknown处理。

## Partial Progress

Agent execution不是事务。

典型过程可能是：

```text
User Message persisted
Model completed
Tool A completed
Tool A result persisted
Tool B side effect happened
process interrupted
```

恢复只读取durable history。存在Assistant Tool Call但没有对应durable Tool Result时，outcome一律视为unknown；Core补写unknown-outcome synthetic Result以恢复对话结构，但绝不自动重新执行旧Tool Call。

## Retry / Fallback Boundary

M3默认不提供自动retry。明确允许的唯一transparent fallback是零Agent Event交付前的`Stream → ErrStreamingUnsupported → Complete`。任意persistence error、model普通错误、Tool post-dispatch错误、cancellation和unresolved Tool recovery都不得自动retry；unknown outcome是execution barrier。

---

# Milestone 4 — Execution Outcome & Accounting

> 状态：Implemented（2026-09-02）

## 目标

让 Agent Core 能够统一描述一次执行最终发生了什么、消耗了多少资源、以什么状态结束。

Observation 回答：

> 执行过程中发生了什么？

Outcome / Accounting 回答：

> 最终累计成了什么？

## 当前缺口

底层 Model 已经可以产生类似：

```text
input tokens
output tokens
total tokens
provider
model
```

但 Agent Turn 级别还缺少统一视角。

例如一次 Turn 结束后，上层应该可以知道：

```text
总 Round 数
总 Model Invocation 数
总 Tool Call 数
总 Input Tokens
总 Output Tokens
Provider / Model 使用情况
总耗时
最终状态
失败阶段
```

## Outcome 的方向

最终采用独立的 `agent.Execution` envelope：成功时携带 canonical
`agent.Result`，Turn lifecycle 建立后的失败或取消仍携带 authoritative
`Outcome` 与 `Accounting`。Outcome 不进入 Result，也不依赖 Observer 聚合。

Accounting 统计 started Round、Model Runtime operation 与 canonical Tool
Runtime attempt；Usage 只聚合 provider-reported execution usage，并通过
Unavailable / Partial / Complete 表达覆盖度。Provider / Model 只依据成功的
authoritative Response 归属。Failure 使用 execution stage 定位终止位置，但
不推导 rollback、durability、external side effect 或 retry safety。

未来它将直接服务于：

- CLI status；
- token/cost 展示；
- budget policy；
- metrics；
- audit；
- evaluation；
- billing；
- execution diagnostics。

---

# Agent Core 封口标准

当 Milestone 1–4 完成后，可以认为 Agent Execution Core 基本完成。

核心结构应稳定为：

```text
Execution Model
    Turn
      Round
        Model Invocation
        Tool Call

Control Plane
    Turn Interceptor
    Round Interceptor
    Model Interceptor
    Tool Interceptor

Observation Plane
    Read-only execution observation

Execution Semantics
    cancellation
    failure
    durability
    partial progress
    streaming
    retry/fallback boundaries

Outcome
    usage
    timing
    status
    execution accounting
```

达到这一状态后，不应再因为普通 Application 需求频繁修改 Agent Core。

---

# Milestone 5 — Session Management

## 目标

将当前“Agent 可以保存历史”的 Session 能力扩展为 Application 真正能够管理 conversation 的能力。

当前基础 Store 已经基本满足 Agent execution：

```text
Create
Append
Load
List
Rename
```

因此 Session 并不是当前 Agent Core 的主要 blocker。

但正式 Application 通常还需要：

```text
Get
Delete
Archive
Pagination / Cursor
Metadata
Possibly Fork
```

## 设计原则

应明确区分：

```text
Session Storage Semantics
```

与：

```text
Session Management Semantics
```

Agent Runtime 只依赖最小的 storage contract。

Application 可以依赖额外 management capability。

不要为了 CLI convenience 把所有管理接口直接塞进基础 `session.Store`。

---

# Milestone 6 — app.cli v2

## 目标

彻底重写 `app.cli`，并将它作为新 Agent Application Surface 的第一个完整消费者。

新的 CLI 不应该继续 patch 旧实现。

## app.cli v2 应验证的能力

```text
Round semantics
Observation
Streaming
Interaction
Usage / Outcome
Session Management
Cancellation
History Recovery
Multimodal Input
```

它的价值不仅是提供 CLI 产品，还应该承担：

> 验证 Agent Core 是否真的足够被一个复杂 Application 自然消费。

如果 CLI 仍然需要大量 private workaround，就意味着底层 contract 还没有真正完成。

---

# 当前阶段明确不进入核心路线的能力

以下能力可能长期有价值，但暂时不应继续扩大 Agent Core 范围：

```text
Parallel Tool Calls
Automatic Retry
Provider Failover
Long-term Memory
Sub-agent
Planner
Workflow Engine
Sandbox
Distributed Execution
Checkpoint / Resume
Human Delegation
```

这些能力属于 Agent 能力扩展，而不是当前基础执行模型的缺失。

其中尤其是 Retry / Failover，应等待 Execution Semantics 稳定后再设计。

原因很简单：

> 在不知道什么情况下 retry 是安全的之前，不应该先设计 retry。

---

# 路线图总结

当前阶段建议正式锁定以下六个节点：

```text
1. Agent Execution Model
   明确 Turn / Round / Model Invocation / Tool Call，
   引入 Round 与 Round-level Interception。

2. Execution Observation
   建立独立、只读、结构化、可关联的 execution observation。

3. Execution Semantics
   收口 streaming、cancellation、failure、
   durability、partial progress、retry/fallback 边界。

4. Execution Outcome
   统一 usage、timing、status 和 execution accounting。

5. Session Management
   补 Application-facing session lifecycle，
   同时保持 Agent 所依赖 Store contract 的克制。

6. app.cli v2
   基于以上能力彻底重写 CLI，
   用真实 Application 验证整个 Agent Surface。
```


- [ ]  Agent Execution Model
- [ ] Execution Observation
- [ ] Execution Semantics
- [ ] Execution Outcome
- [ ] Session Management
- [ ] app.cli v2


