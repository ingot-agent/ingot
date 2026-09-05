# Ingot Web UI

Vue 3 / TypeScript / Vite / Tailwind CSS 4 单页工作区。使用 Pinia、Vue Router hash 路由、Vue I18n、Reka UI、Lucide、markdown-it 和 highlight.js。后端及生产启动方式见 [app.backend](../../plugins/app-webui/README.md)。

## 开发与构建

推荐 Node 24。在本目录执行：

```sh
npm ci
npm run dev
```

Vite 监听 `127.0.0.1:5173`，将 `/api` 代理到 `http://127.0.0.1:7316`。先启动已组合的 Web 后端，或使用下方测试服务。其他后端地址使用 `INGOT_API_URL=http://127.0.0.1:PORT npm run dev`。

```sh
npm run lint
npm test
npm run build
```

`build` 先做 Vue/TypeScript 类型检查，再写入 `../../plugins/app-webui/app/webdist/`。源代码、lockfile 和这个嵌入目录一起提交；Go 用户无需安装 Node。`npm run check:dist` 重建并检查该目录的 Git 状态，供干净 checkout 的 CI 检查产物是否过期；本地尚未提交产物时出现差异是预期的。

## 浏览器回归

```sh
npx playwright install chromium
npm run build
npm run test:e2e
```

Playwright 启动两个真实 Go HTTP/SSE 服务（`17316` 流式、`17317` Run-only），使用仅存在于 Go 测试代码中的确定性 SDK adapters，不访问模型、不需要凭据。覆盖会话生命周期、审批与自由文本、跨标签页同步、运行中历史阻塞、取消后的部分输出、附件历史、Operation、过期 cursor 和中英文/深色/移动布局。失败时保留 trace、截图及 HTML 报告。

使用已有 Chromium 时可指定 `INGOT_TEST_CHROMIUM=/absolute/path/to/chrome npm run test:e2e`。手动预览测试数据：

```sh
INGOT_WEBUI_FIXTURE_ADDR=127.0.0.1:7316 node scripts/fixture.mjs
```

打开 `http://127.0.0.1:7316/`；输入含 `approve`、`ask`、`hold`、`fail` 分别触发审批、自由输入、等待取消和失败。其他输入返回固定文本。这不是生产 Agent，数据只存在于进程内；重新构建嵌入文件后需要重启测试服务。

## 状态边界

- `api.ts` / `sse.ts`：JSON 命令与 fetch SSE，错误不会触发自动重试执行。
- `state.ts` / `stores/runtime.ts`：快照 + cursor 引导、revision 去重、请求代际保护、权威历史替换，以及有限的当前连接执行记录。
- `forms.ts`：Interaction 字段转换与 Operation 简单 Schema 判断；复杂结构或不安全的大整数保留在 JSON 模式提交，完整校验交给后端。
- `components/` / `views/`：会话、执行详情、审批、附件和 Operation；用户文本不作为原始 HTML 执行。

Web invocation ID 与 SDK Turn ID 不可互换。Observation 是只读事实，不用它推断 Web 请求是否已成功；终态以 Web invocation 事件、规范结果和 History 协调。刷新/重连不恢复过去的推理、用量或详细 trace，也不做 Checkpoint/Resume。
