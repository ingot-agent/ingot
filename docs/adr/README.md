# ingot ADR

本目录保存 ingot 的架构决策记录（Architecture Decision Record）。每条 ADR
只冻结一个决策，可以独立评审、独立演进；后续里程碑以追加新 ADR 的方式扩展，
而不是重写旧文档。

## 约定

- 文件名：`NNNN-短横线标题.md`，编号不复用。
- 状态：`Frozen`（已定稿）、`Superseded by NNNN`（被后续 ADR 取代）、`Draft`。
- 每条 ADR 只回答一个问题，并显式列出「决策 / 理由 / 后果 / 不冻结的部分」。
- ADR 是规范来源。与更早的设计文档冲突时，以本目录为准。

## 与 roadmap 的关系

这些 ADR 对应 roadmap 的 M0（Architecture Freeze）。M0 不交付功能，只把会
影响大量代码的 contract 固化下来，使 M1 及之后可以按既定模型直接实现。

| ADR | 主题 | 首次落地 |
|---|---|---|
| [0001](./0001-image-identity.md) | Image 三层身份 | M2 |
| [0002](./0002-runtime-home.md) | Runtime Home 解析与所有权 | M1 |
| [0003](./0003-plugin-configuration.md) | Plugin Configuration 所有权 | M1 |
| [0004](./0004-operation-identity.md) | Operation 身份与同名处理 | M1 |
| [0005](./0005-collection.md) | Collection 在管道中的位置 | M4 |
| [0006](./0006-runtime-environment.md) | Runtime 环境变量收敛 | M1 |
