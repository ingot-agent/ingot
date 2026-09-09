# ADR 0001：Image 三层身份

- 状态：Frozen
- 里程碑：M0（定稿）/ M2（落地）
- 相关：`internal/builder/lock.go`、`internal/builder/build.go`、`internal/builder/desired.go`

## 背景

roadmap 要求 Image 是「named + versioned + content-addressed immutable artifact」，
并只给出一个 `digest` 槽位：

```text
name
version
digest
```

但当前代码里存在两个不同的哈希，它们回答的是不同问题：

| 现有字段 | 位置 | 含义 |
|---|---|---|
| `plugins_digest` | `desired.go:148` | 声明的插件集（module + version/path） |
| `ImageID` | `lock.go:625` | **完整解析后的构建输入**（module sums、dev 源码哈希、toolchain、target、build flags） |
| `ArtifactDigest` | `build.go:202` | 产物二进制字节 |

`ImageID` 与 `ArtifactDigest` 不等价：`build.go:232` 的
`INGOT-BUILD-REPRODUCIBILITY` 检查专门处理「相同构建输入产出不同字节」的情况。
因此「一个 digest 同时充当输入身份和产物身份」在现有设计中不成立。

## 决策

Image 有三个**互相独立**的身份，职责不可合并：

```text
1. Build Input Digest   完整构建输入的哈希（当前 ImageID）
                        含 plugin set、解析后的 module sums、dev 源码内容、
                        toolchain、target、build flags
2. Artifact Digest      产物二进制字节的哈希（当前 ArtifactDigest）
3. name:version         面向用户的语义化发布版本
```

发布记录同时落 `1` 和 `2`：

```text
coding-agent:1.4.0 -> { build_input_digest: sha256:A, artifact_digest: sha256:B }
```

不可变性规则：

- `name:version` 一旦发布，不得重新指向不同的 `build_input_digest` 或 `artifact_digest`；
- `name` 与 `version` **不参与**任何 digest 计算（否则改名即换镜像）；
- 修改任何构建输入（含 toolchain、target、build flags）都必须产生新的 `build_input_digest`，
  因而必须产生新的 version，除非该 version 尚未发布；
- `plugins_digest` 保留为 desired-state 漂移检查（`build.go:88` 的
  `INGOT-BUILD-DESIRED-DRIFT`），**不是**发布身份。

## 理由

- **包含 toolchain/target/build flags** 使身份真正表达「可复现的构建」：
  换 Go 版本或换 target 就是不同的产物，应当是不同的 Image。
- **两个 digest 都保留**，因为构建输入哈希用于构建缓存与漂移检测，
  产物哈希用于校验实际交付的字节，二者的用途不可互相替代。
- **name/version 独立于 digest**，使产品身份与内容身份解耦，
  避免「改名换镜像」和「同名不同内容」两种歧义。

## 后果

- M2 的 Image catalog 需要记录 `name -> version -> {build_input_digest, artifact_digest}`。
- `name:version` 的发布、yank、alias 语义在 M2 冻结。
- 现有 `ImageID` 的字段名与语义保持不变，只是在模型中明确它是**构建输入身份**。
- 需要区分 alias（`latest`/`stable`/`dev`，可变）与 version（不可变）；
  alias 不参与权威字段，越过用户交互边界即解析为具体 version 或 digest。

## 不冻结

- `name` / `version` 的语法正则（M2）；
- catalog 的文件格式与目录布局（M2）；
- GC 的引用关系计算（M2）；
- alias 的具体集合与解析规则（M2）。
