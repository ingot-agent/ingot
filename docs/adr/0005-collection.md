# ADR 0005：Collection 在管道中的位置

- 状态：Frozen
- 里程碑：M0（定稿）/ M4（落地）
- 相关：`internal/builder/desired.go`、`internal/home/home.go`

## 背景

roadmap 把 Collection 定义为「可复用的 Plugin Composition Recipe」，
但同时用大量否定句界定它：不进入 Runtime、不进入 Component Graph、
Plugin 不依赖 Collection、不嵌套 Collection、不负责 Runtime Config。

一个只能靠「它不是什么」来定义的对象，说明它在模型中缺少位置。

## 决策

### 1. Collection 是一个单文件，描述一组插件的集合

Collection 是**单文件**，声明一组插件的集合及其顺序。

### 2. Collection 是 desired-state 的输入变换，不是独立的一等对象

```text
Collection
  ↓（输入侧变换）
Direct Plugin Set（唯一 desired state 表示）
  ↓
plugins.toml
```

- Collection 只存在于构建管道的**输入侧**；
- Apply 后真正的 desired state 仍然只是 Plugin Set；
- 因此不需要声明「它不进入 Runtime / Graph」——它根本不参与那部分模型。

### 3. 本 ADR 不涉及合并语义

合并顺序、冲突处理（已有 Plugin 是否升级、Local Dev 是否被替换、
现有顺序如何满足）等留到 M4 具体设计。

## 理由

- Collection 的产物是 Plugin Set，与用户手写 `plugins.toml` 的产物相同；
  它是输入的一种表达形式，不是新的运行时概念。
- 降为输入变换后，「不进入 X」的否定句不再必要，模型更简洁。

## 后果

- M4 需要设计：Collection 文件格式、Planner（区分 Add / Satisfied /
  VersionConflict / SourceConflict / OrderConflict）、Apply 的原子性、
  以及是否记录非权威 provenance（Receipt）。
- Builder 侧只需在 desired state 之前增加一个解析步骤，不需要理解 Collection
  的运行时含义。

## 不冻结

- Collection 文件格式与字段（M4）；
- 合并与冲突语义（M4）；
- Apply 的提交与回滚细节（M4）；
- provenance / receipt 是否引入（M4）。
