# 为 ingot 贡献

[**English**](../CONTRIBUTING.md) · [**中文**](./CONTRIBUTING.zh.md)

感谢你帮助改进 ingot。我们欢迎代码、测试、文档、Bug 报告和设计反馈。

## 开始之前

- 提交新 Issue 前，先搜索已有 Issue 和 Pull Request。
- 小型修复和文档改进可以直接提交 Pull Request。
- 大型功能、架构调整、新的文件格式行为和破坏性变更，应先与维护者讨论，再投入实现。
- 不要在公开 Issue 中报告安全漏洞。请使用仓库的私密安全报告渠道；如果该渠道不可用，请私下联系维护者。

## 项目原则

所有贡献都必须保持 ingot 的核心性质：

- **插件一律平等。** 核心行为不得依赖特定插件名称、Module Path、Package Path 或官方插件身份。
- **在构建期完成组合。** Builder 解析并校验静态依赖图，生成普通 Go wiring；运行时不发现或动态加载插件。
- **构建确定且可追溯。** 顺序、规范化输入、锁定源码、镜像身份和产物校验必须保持稳定。
- **镜像不可变。** 激活、回滚、恢复和垃圾回收必须保持镜像完整性。

如果一项功能需要新的 Builder 行为，应将其表达为所有插件都可使用的通用 Manifest 规则、类型 Contract 或机制。不得为最先提出需求的插件添加特殊分支。

## 开发流程

### 1. 创建分支

禁止直接向 `main` 提交或推送。请从最新的 `main` 创建一个目标明确的分支：

```sh
git switch main
git pull --ff-only
git switch -c feat/short-description
```

常用前缀包括 `feat/`、`fix/`、`docs/`、`test/` 和 `refactor/`。

### 2. 完成一项聚焦的变更

- 一个 Pull Request 只处理一个连贯目标。
- 遵循已有 Package 边界和局部代码风格。
- 为变化的行为新增或更新测试，包括失败场景。
- 同步更新受影响的用户文档、示例和设计文档。
- 不要提交密钥、本地配置、IDE 元数据、构建产物或无关的格式化变更。

使用 `gofmt` 格式化每个修改过的 Go 文件：

```sh
gofmt -w path/to/changed_file.go
```

### 3. 执行完整测试

开发过程中可以运行定向测试，但每个 Module 都必须在开启 race detector 的情况下通过，变更才能视为可提交。请在仓库根目录执行与 CI 相同的 Module 发现流程：

```bash
while IFS= read -r -d '' mod_file; do
  module_dir="$(dirname "$mod_file")"
  (
    cd "$module_dir"
    GOWORK=off go test -race ./...
  )
done < <(find . -type f -name go.mod -not -path '*/vendor/*' -print0 | sort -z)

git diff --check
```

仅在仓库根目录运行 `go test ./...` 并不充分，因为各插件是独立的嵌套 Go Module。如果完整测试无法执行或未能通过，必须清楚记录阻塞原因，不得把变更描述为已经可以合并。

### 4. 清晰地提交

Commit Subject 应简洁、使用祈使语气，并遵循仓库已有风格：

```text
feat(builder): support a general component rule
fix(app-cli): preserve cancellation errors
docs: clarify plugin composition
```

保持 Commit 易于审查，不要混入无关变更。未经其他贡献者协调，不要重写共享分支的历史。

### 5. 创建 Pull Request

以 `main` 为目标分支创建 Pull Request，并说明：

- 修改了什么以及为什么；
- 相关 Issue 链接；
- 已执行的测试与验证；
- 用户可见影响或兼容性影响；
- 已知限制或后续工作。

所有必要的 CI 检查都必须通过。Review 开始后，尽量通过追加 Commit 响应意见；除非已经和 Reviewer 协调，否则不要 Force Push。

## 代码与测试要求

- 编写符合 Go 习惯的代码，并准确记录所有导出标识符。
- 在阻塞调用中保持 `context.Context` 的取消和 Deadline 语义。
- 包装错误时，确保调用者仍可使用 `errors.Is` 和 `errors.As`。
- 保持 Component 构造和清理的确定性；Cleanup 按创建顺序的逆序执行。
- 使用正例和负例测试保护严格解析与校验行为。
- 优先测试公开行为。核心插件机制测试应使用任意或合成身份，不应依赖官方插件。
- 当行为依赖顺序或并发时，应显式断言并运行 race detector。

## 贡献插件

`plugins/` 下的官方插件是独立 Go Module，拥有自己的 `go.mod`、`ingot.plugin.toml`、Component 实现和测试。新插件必须遵循与所有第三方插件相同的 Manifest 与 Component 规则。

把插件加入官方 Profile 是一项独立的产品决策。Profile 可以选择插件，但不能赋予它不同的 Builder 或运行时行为。

## 文档

确保命令与示例可执行，并与当前行为一致。修改同时存在中英文版本的文档时，应同步更新两版；如果只修改其中一版，Pull Request 必须明确说明原因。

## 许可证

提交贡献即表示你同意该贡献可按照仓库的 [MIT License](../LICENSE) 分发。
