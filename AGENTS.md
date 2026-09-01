# Repository Guidelines

## 项目结构与模块组织

本仓库是 `ccwork-scaffold-cli` 的 Go CLI 工具，使用 Go 1.25 和 Cobra。

- `main.go`：可执行程序入口。
- `internal/cli/`：命令树、参数模型、Git 来源与缓存、项目渲染、清单和升级逻辑。
- `internal/cli/cli_test.go`：渲染、版本校验、Git 生成和迁移冲突测试。
- `spec/`：生成器、版本目录和声明式升级协议的设计文档。
- `README.md`：用户用法、版本目录格式和验证命令。

新增功能优先放在现有 `internal/cli` 分层中；只有形成稳定公共 API 时才考虑调整包边界。

## 构建、测试与开发命令

```bash
go build -o bin/ccwork-scaffold-cli .
GOCACHE=/private/tmp/ccwork-scaffold-cli-go-cache go test ./...
go vet ./...
gofmt -w cmd internal
git diff --check
```

分别用于构建 CLI、运行全部测试、执行静态检查、格式化 Go 源码和检查空白错误。调试本地命令时，可使用 `go run . version`；涉及远程仓库的操作应优先使用本地测试仓库或 `--offline`。

## 编码风格与命名约定

遵循 `gofmt` 默认格式，使用 tabs 缩进；包名小写，导出类型和函数使用 PascalCase，局部变量使用 camelCase。函数名以动词开头，获取逻辑优先使用 `FetchXxx`。错误应保留上下文并使用 `%w` 包装；CLI 对外错误通过 `ExitError` 映射稳定退出码。保持校验、文件写入和外部 Git 调用的边界清晰，避免无必要的抽象。

## 测试指南

测试使用标准库 `testing`，测试函数命名为 `TestXxx`，临时文件使用 `t.TempDir()`。新增行为应覆盖成功路径、输入校验、路径安全和冲突时“不写入”的边界；优先在 `internal/cli/cli_test.go` 增加单元或端到端 seam 测试。提交前至少运行 `go test ./...`、`go vet ./...` 和 `git diff --check`。

## 提交与 Pull Request

当前仓库尚无 Git 提交历史，因此没有可核实的既有 commit 格式；建议使用简短、祈使式主题，例如 `add scaffold migration validation`，单个提交聚焦一个变更。PR 应说明目的、影响的命令或协议、测试命令及结果；涉及版本目录、迁移 schema 或生成产物时，附示例输入/输出并说明兼容性与回滚风险。

## 安全与配置注意事项

不要在仓库 URL、Go module、日志或测试 fixture 中提交凭据。正式 scaffold tag 必须是 annotated tag，并校验 tag 对应的完整 commit；缓存按 commit 保存。CLI 不执行远程脚本，不自动运行 DDL、`go generate`、`go mod tidy`、Git commit 或 push。生成和升级应保持 dry-run、冲突检测及干净 worktree 约束。
