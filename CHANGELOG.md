# 更新日志

## v0.4.0 — 2026-11-11

- BREAKING CHANGE：new 默认 CI 模式改为 reference，必须明确选择包含 runtime 的 CI 正式版本；历史副本用途使用显式 --ci-mode copy。
- 新工程自动生成并 include 项目 overrides，保留来源已有配置；共享 jobs、规则、脚本和旧安装身份不复制。
- 保留模板已有的构建、Helm 等额外 runtime 模块及项目参数入口，验证所选版本完整支持，避免只生成安全入口而丢掉其他流程。
- 拒绝缺失的本地 include，离线 inspect 检查全部 runtime 模块的 project/SHA 一致性。
- 保持来源校验、冲突拒绝及发布历史隔离，提供两个 profile、旧模板、配置保留和不兼容版本拒绝的回归测试。

## v0.2.0 — 2026-10-28

- 增加受约束的 CI 更新，核验旧来源、hash 和权限，冲突停止，普通失败回滚。
- 增加人工定制记录及 inspect customized 状态，保留原版来源，不自动覆盖项目定制。
- 支持 CI release 资产路径，兼容现有 schema 1 安装记录。
- 新项目保留发布工具与示例，排除父脚手架日志、归档、catalog 和实际发布配置，覆盖两个 profile、旧来源和 overlay。
- 不自动执行部署、远端管理、提交或推送；实际镜像、权限和流水线须单独验收。
