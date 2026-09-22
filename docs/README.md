# Core documentation / Core 文档目录

Core owns build-time composition and management of Images, Runtimes and
Processes. Concrete plugin behavior is documented in the
[plugins repository](https://github.com/ingot-agent/plugins/blob/main/docs/README.md).

## Current user and contributor references

- [Project overview](../README.md) / [中文概览](README.zh.md)
- [Usage and CLI reference](USAGE.md) / [使用与命令参考](USAGE.zh.md)
- [Upgrading, backup, and recovery / 升级、备份与恢复](UPGRADING.md)
- [Current architecture and source navigation / 当前架构与源码导航](ARCHITECTURE.md)
- [Core release procedure / Core 发布流程](../RELEASE.md)
- [Security reporting / 安全报告](../SECURITY.md)
- [Current file formats and constructor contract](FILE_FORMATS.md)
- [Contributing](../CONTRIBUTING.md) / [贡献指南](CONTRIBUTING.zh.md)
- [Architecture decisions](adr/README.md)

Use these references with the checked-in source and tests. A released binary
or plugin module should be read with documentation from its own release tag.
Design milestone numbers do not imply published module versions.

## Documentation ownership / 文档归属

| Repository | Scope |
|---|---|
| Core (this repository) | Builder, generic manifest/recipe/lock formats, graph wiring, CLI, installation, Images, Runtimes, Processes, Collections and ADRs |
| [Plugins](https://github.com/ingot-agent/plugins/blob/main/docs/README.md) | Official plugin usage, configuration, tools, provider adapters, browser API, implementation designs and release process |
| [SDK](https://github.com/ingot-agent/sdk) | Optional public agent contracts, ownership/concurrency semantics and SDK history |
| [ABI](https://github.com/ingot-agent/ingot-abi) | Fixed Component wrappers and runtime-owned host contracts |

2026-09-22 文档迁移将 16 份插件设计和子代理方案移入 plugins，将 SDK/ABI
设计移入对应仓库。Core 中的旧文件已删除，文档只由所属仓库维护。
完整插件映射见 [design-history](https://github.com/ingot-agent/plugins/blob/main/docs/design-history/README.md)。

## Core design history

以下文档保留设计背景和当时的范围；文中的旧命令、配置或构造函数不是当前使用
规范。当前字段请查 [FILE_FORMATS.md](FILE_FORMATS.md)，命令请查使用指南，
架构决策请查 ADR 与对应实现。

- [Architecture v0.3](ingot_架构设计_v0.3.md)
- [Image / Runtime / Process proposal](ingot_M2_image_runtime_process_设计方案.md)
- [Core installation and update design](ingot_Core_安装与更新机制_v0.1.md)
- [Plugin manifest proposal](ingot.plugin.toml_设计方案_v0.1.md)
- [Builder configuration v0.1](ingot_builder.toml_v0.1_设计方案.md)
- [Desired recipe proposal](ingot_plugins.toml_v0.1_设计方案.md)
- [Resolution lock proposal](ingot_plugins.lock_v0.1_设计方案.md)
- [Original milestone roadmap](../ingot-roadmap_runtime-composition-and-plugin-ecosystem.md)

新提案在完成实现、验证及当前使用文档更新之前，不应标记为已发布功能。
