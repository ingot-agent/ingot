# ingot 项目协作说明

## 项目定位与当前状态

ingot 的目标是在构建期把 Go Plugin 解析为静态 Component Graph，生成并编译不可变的原生 Runtime Image；运行期只负责读取配置、实例化静态 wiring、执行能力调用和逆序清理。

当前工作区包含以下部分：

- `local/`：架构 v0.3、SDK v0.1、Plugin Manifest、`plugins.toml`、`plugins.lock` 和官方 Plugin 的设计稿；设计稿描述目标与实现基线，状态以各文档为准。
- `cmd/`、`internal/` 和根 `go.mod`：Bootstrap/Builder/CLI 的当前实现，module path 为 `github.com/ingot-agent/ingot`。
- `../sdk/`：公共 Contract，是与本仓库相邻的独立 Go module 和独立 Git 仓库，module path 为 `github.com/ingot-agent/sdk`。
- `plugins/http-default/`、`plugins/filesystem-local/`、`plugins/session-jsonl/`：第一批官方 Plugin 的独立 Go modules；每个目录包含 Component、Manifest 和外部包测试。
- `plugins/tool-fs/`、`plugins/tool-ask/`、`plugins/tool-shell/`、`plugins/interceptor-approval/`、`plugins/tool-runtime/`：第二批官方 Plugin 的独立 Go modules；它们按设计稿依次提供 filesystem、interaction、shell、approval 和 runtime 能力。

不要把设计稿中尚未实现的部分当成现有能力。新增 Runtime Image 或后续官方 Plugin 时，应继续保持清晰的 module/package 边界，而不是塞进公共 SDK。

## 资料优先级

修改已实现的 SDK 时，按以下顺序判断现有行为：

1. `../sdk/` 中的公开代码与测试；
2. `../sdk/README.md`；
3. `local/ingot_SDK_v0.1_设计方案.md`；
4. `local/ingot_架构设计_v0.3.md`；
5. 其余文件格式设计稿。

设计文档仍处于 Draft/Discussion Draft 阶段，个别早期文档之间存在演进差异。处理跨文档冲突时不要自行拼接出第三种规范：明确指出冲突，选择与当前任务对应的较新、较专门规范，并同步更新所有受影响文档、示例和测试。实现行为发生变化时，至少同步检查 `../sdk/README.md` 与 SDK 设计稿。

## 核心模型

- Plugin 是分发、版本、配置、状态和用户操作边界。
- Component 是构造、依赖、导出和生命周期边界，也是图节点；identity 为 `<plugin-id>/<component-name>`。
- Capability 是 Component 之间交换的稳定 Go Contract；匹配由 Builder 使用 `go/types` 和 `types.AssignableTo` 完成。
- Runtime Instance 是一次 `New` 调用创建的独立实例；需要实例名时使用 `sdk.Named[T]`。
- 构建期负责发现、校验、ONE/OPTIONAL/MANY 解析、拓扑排序、静态 wiring、编译和 pre-switch check；运行期不得重新做动态插件发现或图解析。

标准 Component package 约定为：

```go
type Dependencies struct {
    // consumed capabilities
}

type Exports struct {
    // provided capabilities
}

func New(
    ctx context.Context,
    cfg PluginConfig,
    deps Dependencies,
) (Exports, sdk.Cleanup, error)
```

`Dependencies` 和 `Exports` 必须是当前 Component package 中的导出具名 struct；字段必须是顶层、具名、导出字段，不能使用 embedded 或未导出字段。Composite Plugin 的所有 Component 接收同一个 root `Config` 类型。

## 不可随意改变的组合语义

- Dependency 表达式为 `Base | sdk.Optional[Expr] | []Expr | sdk.Named[Expr]`，最外层决定 ONE、OPTIONAL 或 MANY cardinality。
- ONE 必须恰好匹配一个 Provider；OPTIONAL 只允许零或一个；MANY 收集所有匹配项并允许为空。
- MANY 接受可赋值的 scalar 或 slice export；slice 按元素展开。内层 wrapper 保持完整类型形状。
- `sdk.Named[T]` 与普通 `T` 是不同 target type；同一 Named collection 的名称必须非空且唯一。
- Component creation 使用 deterministic Kahn topological sort，以 `(directPluginIndex, componentIndex)` 打破并列。
- MANY 顺序依次由 Provider Component creation order、Exports 字段声明顺序、export slice 元素顺序决定。该顺序也影响 Tool、Interceptor、Prompt Contributor 和 Build Identity。
- self-loop 使用正常候选匹配规则检测；跨 Component cycle 必须报告完整路径。
- `pipeline.Compose` 的第一个 interceptor 是最外层：before 最先执行，after 最后执行；允许 interceptor short-circuit。
- 对以上类型、顺序和生命周期语义的修改属于破坏性 Contract 变更，不能当作普通重构提交。

## SDK 编码约定

- SDK 是稳定、轻量、实现无关的公共契约层。优先增加小而明确的新 type/interface/capability，避免把具体 Provider、存储或 UI 实现放入 SDK。
- 所有可能阻塞的操作接收 `context.Context`；传入的 Context 拥有 cancellation/deadline authority。保留 `context.Canceled` 和 `context.DeadlineExceeded` 的 `errors.Is` 链。
- Capability 默认 concurrent-safe，除非领域 Contract 明确更窄的顺序：同一 Session 的 append/agent turn 有序，Interaction 的 `Ask`/`ReadLine` 串行，而不同 Session 或 request 可并发。
- aggregate input 对 callee 是 immutable-by-contract。返回后仍需持有可变输入时先复制；aggregate output 的所有权在返回时交给 caller。该规则递归适用于 slice、map、pointer 和 `json.RawMessage`。
- 使用普通 Go error chain：包装用 `%w`，多错误用 `errors.Join`，程序化分支使用所属 package 的 sentinel 或 typed error。
- Component `New` 必须可重复、可并发调用，每次创建独立状态；同步完成有界初始化，长期 goroutine 归实例所有。
- `Cleanup` 必须观察 `ctx.Done()`、停止并等待实例后台任务。构造同时返回 error 和非 nil Cleanup 时，Cleanup 仍需进入失败清理路径；清理顺序严格逆于创建顺序。
- 保持 package dependency 方向简单，避免循环：基础 primitives/config/pipeline 在下，领域 Contract 在上；`agent` 可以依赖 model/session，`model` 可以依赖 tool，但不要反向导入。
- 公共 struct 示例和调用使用 keyed literals。更改公开 struct、interface method、sentinel 或语义前，先评估源码兼容性和 semantic import major。
- 保持现有 Go doc 风格，所有 exported identifier 都应有准确注释；注释应描述 Contract 语义，而不只是复述名称。

特别注意以下领域 Contract：

- `config.Decode` 必须严格拒绝未知 TOML 字段；`StateDir` 只从 Plugin-scoped Context 获取路径。
- `httpx.Client.Do` 的显式 Context 覆盖 request Context，且实现不得修改原始 request。
- `filesystem.FS` 只接受 workspace-relative、`/` 分隔的安全路径，并必须防止 traversal 和 symlink 越界。
- model complete 与 streaming 使用独立 Runtime 和 interceptor chain；stream handler error 必须立即向上传递。
- session 的成功 append 对同一 Session 形成 total order，`Load` 按该顺序返回。
- interaction event 是 SDK 内的封闭集合；扩展它属于公共 Contract 设计变更。

## 开发与验证

根目录是 Builder Go module，`go.work` 还包含相邻的 SDK 和全部官方 Plugin。`go.work` 使用版本限定的本地 SDK replacement，避免在各 Plugin 的发布用 `go.mod` 中写入本机路径。跨模块验证可从根目录执行；SDK 尚未发布 `v0.1.0` 前，关闭 workspace 的独立 Plugin 验证会因无法下载该版本而失败：

```powershell
go test ./plugins/http-default/... ./plugins/filesystem-local/... ./plugins/session-jsonl/... ./plugins/tool-fs/... ./plugins/tool-ask/... ./plugins/tool-shell/... ./plugins/interceptor-approval/... ./plugins/tool-runtime/...
go vet ./plugins/http-default/... ./plugins/filesystem-local/... ./plugins/session-jsonl/... ./plugins/tool-fs/... ./plugins/tool-ask/... ./plugins/tool-shell/... ./plugins/interceptor-approval/... ./plugins/tool-runtime/...

Set-Location plugins/http-default # 其他 module 同理
$env:GOWORK = 'off'
go test ./...
```

- module 声明 Go 1.24 或更新版本；不要降低版本，因为公开 API 使用设计所需的泛型能力。
- `go test -race ./...` 是 README 指定的 conformance 命令。若本机 race runtime 本身无法启动，记录 toolchain、OS 和退出信息，并至少完成普通测试与 `go vet`；最终仍需在受支持环境补跑 race。
- 只对本次触碰的 Go 文件运行 `gofmt`，随后检查 diff。当前 Windows checkout 的 tracked 内容为 LF、工作树为 CRLF，直接对整个 module 执行格式化会制造无关的全仓换行 diff。
- 新增/修改公开 Contract 时，在外部测试包（如 `sdk_test`、`<package>_test`）补充行为或 compile assertion，模拟 Builder 生成代码和第三方组件的真实使用方式。
- 并发安全的独立测试优先调用 `t.Parallel()`；涉及顺序时显式记录并断言完整顺序。
- 错误测试使用 `errors.Is` 验证链，而不是依赖完整错误文本。

## Git 与变更范围

当前根目录 `ingot/` 和相邻的 `../sdk/` 各自包含 `.git`；更上层的 `D:\ai_dev_workspace\.git` 与本项目无关。查看状态、历史或 diff 时必须显式指定目标仓库，例如：

```powershell
git status --short
git diff --check
git -C ..\sdk status --short
```

不要修改或提交 `.idea/` 或 `../sdk/.idea/`。保留用户已有的未跟踪文件和无关改动，不要用 reset/checkout 清除它们。

`local/` 和 Plugin 实现属于 `ingot` Git 仓库，不属于 `../sdk/` Git 仓库。修改它们时需明确说明变更位置，不要误称它们已包含在 SDK commit 中。

## 提交前检查清单

1. 变更属于 SDK Contract、设计文档，还是未来 Builder/Runtime 实现？边界是否正确？
2. 是否保持 Context、错误链、并发、ownership、顺序与 Cleanup 语义？
3. 是否意外改变公开 API 或 ONE/OPTIONAL/MANY、Named、interceptor 行为？
4. 是否为新行为添加外部包测试、负例和必要的顺序/并发测试？
5. `go test ./...`、`go vet ./...` 和可用环境中的 `go test -race ./...` 是否通过？
6. 是否仅格式化触碰文件并检查 `git -C sdk diff --check`？
7. 相关 README、设计稿、schema 示例和版本描述是否保持一致？
