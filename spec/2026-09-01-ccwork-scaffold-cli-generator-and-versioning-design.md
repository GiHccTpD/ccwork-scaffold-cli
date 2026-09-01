# ccwork-scaffold-cli Generator 与脚手架版本治理方案

## 1. 文档信息

- 状态：已完成方案评审，待实施
- 日期：2026-09-01
- generator 仓库：`ccwork-scaffold-cli`
- scaffold 仓库：`ccwork-scaffold-go-http`
- generator 首发版本：`v0.0.1`
- 首个默认 scaffold 基准：`v0.2.0`

## 2. 背景与问题

当前使用 `ccwork-scaffold-go-http` 创建业务服务时，需要人工下载或复制一份脚手架，再逐项修改服务名、Go Module、目录、构建产物和配置。该方式存在以下问题：

- 创建步骤依赖人工经验，容易遗漏 import、目录名、Dockerfile、Makefile 或 Swagger 信息。
- 业务项目没有可靠记录自己来源于哪个 scaffold 版本。
- scaffold 后续修复或新增公共能力后，已派生项目缺少安全、可审计的升级路径。
- 当前业务应用版本与 scaffold 来源版本容易混淆。
- 直接复制整个仓库可能带入示例业务、IDE 文件、生成代码或其它不应进入生产项目的内容。

本方案引入独立的 `ccwork-scaffold-cli`，统一承担项目生成、版本识别、既有项目接管和受控升级。

## 3. 已核实的当前基线

截至 2026-09-01，当前 `ccwork-scaffold-go-http` master 具备以下能力：

- 使用 Cobra 作为服务命令入口。
- 已支持业务二进制 `--version` 和 HTTP `GET /version`。
- Makefile 与 Dockerfile 已支持通过 ldflags 注入业务构建版本、commit、工作树状态和构建时间。
- 当前 Go Module 要求 Go `1.25.0`。
- 当前 master 已通过 `go test ./... -count=1`、`go build ./cmd/scaffold` 和 `git diff --check`。
- 当前本地存在实验 tag `v1.0.0`，它位于 feature 分支而非 master，并包含已废弃方向的 `scaffold-sync` 实现。

同时已确认以下生产化问题需要在新基准中处理：

- Docker builder 仍使用 Go `1.22`，与 `go.mod` 的 Go `1.25.0` 不一致。
- 仓库当前跟踪了 `.idea` 文件，不能进入生成产物。
- 容器默认读取 `./configs/config.yaml`，但生产配置的注入和失败行为需要明确。
- HTTP 请求日志会完整读取 body，尚缺少大小限制、Content-Type 限制和敏感字段脱敏。
- pprof 和 Swagger 当前无条件注册在业务端口。
- `minimal` profile 删除示例 GORM 代码后，store 初始化和 query 依赖需要解耦，确保生成产物仍可编译。

## 4. 目标

### 4.1 核心目标

- 安装一个独立命令后即可生成可编译、可测试的 Go HTTP 服务项目。
- 新项目明确记录 scaffold 来源版本和精确 commit。
- 已有项目能够被只读识别或经校验后纳入版本治理。
- scaffold 升级采用声明式、可预览、可审计、冲突零写入的方式。
- generator、scaffold 和业务应用三个版本彼此独立。
- 默认产物具备明确的生产安全边界。

### 4.2 非目标

首版不实现以下能力：

- 不做任意源码三方合并。
- 不运行远程下发的 shell、PowerShell 或其它任意脚本。
- 不提供自动 downgrade。
- 不自动执行数据库 DDL/DML、GORM Gen、`go generate`、`go mod tidy`、`go get`、Makefile、Docker 或 Kubernetes 命令。
- 不支持在 monorepo 子目录中执行正式升级。
- 不采集使用遥测。
- 不通过 HTTP 默认暴露精确 scaffold 版本。
- 不把 generator 运行时代码复制到业务项目。

## 5. 仓库与职责边界

### 5.1 `ccwork-scaffold-go-http`

该仓库继续作为可直接编译、测试和运行的 golden project，不改造成充满占位符的纯模板目录。它负责：

- scaffold 源码和公共能力。
- `minimal`、`example` profile 定义。
- scaffold release 目录。
- 结构化渲染规则。
- 声明式升级迁移。
- 生成产物和升级链的发布前验证。

### 5.2 `ccwork-scaffold-cli`

该仓库独立维护 generator 工具，负责：

- `new`：生成新项目。
- `list`：展示支持的 scaffold 版本。
- `inspect`：识别项目来源、状态和升级能力。
- `adopt`：接管已有项目。
- `upgrade`：执行声明式向前升级。
- 下载、缓存、离线、认证适配、完整性校验和结构化输出。

独立建仓可以避免 generator 版本与 scaffold tag 共用同一套版本空间，也避免业务项目为升级工具额外引入依赖。

## 6. 版本模型

必须区分以下三种版本：

1. generator 版本：`ccwork-scaffold-cli` 自身版本，首发为 `v0.0.1`。
2. scaffold 版本：生成或升级所使用的框架版本，首个默认基准为 `v0.2.0`。
3. application 版本：业务服务自身发布版本，继续使用现有 ldflags、`--version` 和 `GET /version`。

### 6.1 scaffold 版本线

- 改造前的当前 master 先固定为 `v0.1.0`。
- 完成 generator 配套、profile、版本目录和生产阻断修复后发布 `v0.2.0`。
- `v0.2.0` 是首个允许默认创建新项目的正式基准。
- `v0.1.0` 只作为已有项目接管和 `v0.1.0 -> v0.2.0` 升级验证基线。
- feature 分支上的实验 `v1.0.0` 保持不可变，但标记为 legacy，不参与默认生成或升级目标解析。

### 6.2 SemVer 规则

- `PATCH`：兼容性修复，不要求人工修改业务代码。
- `MINOR`：向后兼容的新能力，允许提供自动声明式迁移。
- `MAJOR`：存在破坏性变化，必须提供明确升级说明和人工动作。
- 正式版本统一使用 `vX.Y.Z`。
- prerelease 默认不能用于生产生成，显式传入 `--allow-prerelease` 后才允许。
- 已发布 tag 禁止移动、覆盖、删除或改变所指 commit。

### 6.3 版本目录

scaffold 仓库维护机器可读的受支持版本目录，CLI 不通过“最大 SemVer”猜测默认版本。

示例：

```yaml
schemaVersion: 1
defaultVersion: v0.2.0

releases:
  - version: v0.1.0
    revision: "<full-commit>"
    status: deprecated
    creatable: false
    upgradeSource: true
    minGeneratorVersion: v0.0.1
    migrationSchemaVersion: 1

  - version: v0.2.0
    revision: "<full-commit>"
    status: active
    creatable: true
    upgradeSource: true
    minGeneratorVersion: v0.0.1
    migrationSchemaVersion: 1

legacy:
  - version: v1.0.0
    reason: experimental-feature-branch
```

状态语义：

- `active`：允许新建、接管和作为升级目标。
- `deprecated`：禁止新建，但允许作为已有项目的升级来源。
- `revoked`：禁止新建和作为升级目标，但允许已有项目识别并升级离开。
- `legacy`：不进入正式版本链，只保留历史说明。

## 7. CLI 命令设计

首版只提供以下六个命令：

```text
ccwork-scaffold-cli version
ccwork-scaffold-cli list
ccwork-scaffold-cli new
ccwork-scaffold-cli inspect
ccwork-scaffold-cli adopt
ccwork-scaffold-cli upgrade
```

不额外提供语义重叠的 `check`、`diff`、`doctor`、`sync` 或 `update`。

### 7.1 `new`

最小调用：

```bash
ccwork-scaffold-cli new \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service
```

规则：

- `name` 和 `module` 为必填参数。
- 输出目录默认使用 `name`。
- `profile` 默认使用 `minimal`。
- 端口等其它值从 profile 派生，并允许使用可选 flag 覆盖。
- 所有参数必须支持非交互 flags；终端环境可提供交互提示。
- `--scaffold-version` 可显式选择受支持版本。
- 不允许默认使用 master；只有显式 snapshot 模式才允许非 tag 来源，且不能标记为可升级生产项目。

目标目录安全规则：

- 目录不存在时创建。
- 目录存在但为空时允许生成。
- 目录非空时立即失败。
- 首版不提供覆盖已有目录的 `--force`。
- 全部内容先在临时目录渲染和校验，成功后再原子移动。
- 默认执行 `git init`，但不 commit、不 push；`--no-git-init` 可关闭。

默认不执行 `go test`、`go build` 或 `go mod tidy`。显式传入 `--verify` 时执行固定验证：

```text
go test ./...
go build ./...
git diff --check
```

### 7.2 `list`

展示：

- 默认版本。
- active/deprecated/revoked/legacy 状态。
- 是否允许创建。
- 是否可作为升级来源。
- 最低 generator 版本。

### 7.3 `inspect`

该命令只读，展示：

- 项目名、Go Module、profile。
- scaffold tag 和完整 commit。
- 创建和最近升级所用 generator 版本。
- 当前是否 verified、upgradeable 或 pending。
- 可用升级目标和阻断原因。

### 7.4 `adopt`

已有项目必须显式声明来源版本，不自动猜测：

```bash
ccwork-scaffold-cli adopt --from v0.1.0 --profile minimal
```

行为：

- 从 `go.mod` 解析 module，并校验显式项目参数。
- 下载对应 tag，按相同结构化规则渲染基线。
- 核对受管理文件指纹。
- 校验通过后才标记 `verification: verified` 和 `upgradeable: true`。
- 校验失败时不允许使用 `--force` 伪装为可升级。
- `--record-only` 只登记版本盘点信息，并写入 `verification: unverified`、`upgradeable: false`。
- `adopt` 不修改业务代码。

### 7.5 `upgrade`

正式升级要求显式目标：

```bash
ccwork-scaffold-cli upgrade --to v0.3.0 --dry-run
ccwork-scaffold-cli upgrade --to v0.3.0
```

便利模式必须显式使用：

```bash
ccwork-scaffold-cli upgrade --latest
```

`--latest` 根据版本目录的 `defaultVersion` 解析，输出中必须展示实际目标版本和完整迁移路径。`--to` 与 `--latest` 互斥。

## 8. Profile 设计

### 8.1 `minimal`

默认生产骨架保留：

- 服务启动和优雅退出。
- 配置、日志、DB、Redis、RPC。
- middleware、Prometheus。
- `/health` 和 `/version`。
- controller -> biz -> store 的最小注册点和可编译边界。

默认不包含：

- `user CRUD` 示例。
- 示例业务路由。
- 示例 GORM model/query。
- 示例 Swagger 业务接口。

### 8.2 `example`

用于学习和演示，保留当前 user CRUD、示例路由和生成代码。它不作为生产创建的默认 profile。

### 8.3 Profile 约束

- profile 写入 `.scaffold.yaml`。
- 项目创建后不可通过升级切换 profile。
- 每个声明式迁移必须声明适用 profile。
- profile 生成结果必须分别独立测试。

## 9. 结构化渲染

scaffold 仓库本身必须保持可编译。generator 不对整个仓库全局替换字符串 `scaffold`，只执行有边界的结构化操作：

- 修改 `go.mod` module 声明。
- 重写 Go import 前缀。
- 重命名 `cmd/scaffold` 和 `internal/scaffold`。
- 修改明确列出的 Makefile、Dockerfile、Swagger 标题和服务标识。
- 从 `name` 派生可执行文件名、目录名、内部包名和 metrics/log service name。
- 对名称、module 和目标路径执行格式、保留字、路径穿越和冲突校验。

生成产物不得包含：

- `.git` 来源历史。
- `.idea`、`.vscode` 等 IDE 私有文件。
- `config.yaml`、`.env` 或其它真实环境配置。
- build、coverage、日志和临时产物。
- Git remote 或任何认证信息。

## 10. 项目清单

`.scaffold.yaml` 必须提交到业务项目，且只保存项目身份和来源，不开放项目级自定义升级规则。

示例：

```yaml
schemaVersion: 1

project:
  name: order-service
  module: git.inspur.com/ccwork/service/order-service
  profile: minimal

scaffold:
  repository: https://git.inspur.com/ccwork/go/ccwork-scaffold-go-http.git
  version: v0.2.0
  revision: "<full-commit>"
  verification: verified
  upgradeable: true

generator:
  createdWith: v0.0.1
  lastUpgradedWith: v0.0.1
```

tag 供人阅读，完整 commit 是实际内容身份。CLI 每次使用 tag 时都必须重新解析并与记录的 commit 比较；tag 被移动时立即失败。

业务项目不能在清单中加入 `ignore`、任意替换、文件覆盖规则或执行命令，避免相同版本在不同项目中产生不同升级语义。

## 11. 声明式升级模型

### 11.1 基本原则

- 不保留现有实验 `scaffold-sync` 的三方合并机制。
- 升级只执行 CLI 支持的声明式白名单操作。
- 未声明的文件默认归业务项目所有，CLI 不读取、不修改、不删除。
- controller、biz、store、GORM 生成代码和业务文档默认受保护。
- 所有修改都必须带明确前置条件，如旧 hash、旧内容或目标不存在。
- 任一前置条件不满足即报告冲突，且不写入任何文件。

### 11.2 首批允许的操作

```text
add
replace-if-unmodified
replace-if-hash-matches
apply-exact-patch
rename-if-hash-matches
delete-if-unmodified
render-template
require-manual-action
```

禁止 `exec`、`shell`、`go run` 或下载后执行任意二进制。声明式能力不足时，应升级 CLI 或要求人工处理。

### 11.3 安全门槛

- `--dry-run` 允许在脏工作树运行，但只输出计划。
- 正式升级要求目标项目是独立 Git worktree，且工作树干净。
- `.scaffold.yaml` 所在目录必须是 Git 根目录。
- 首版不提供 `--force-dirty`。
- 所有变更先在临时目录计算和校验。
- 无冲突后一次性写入；中途失败自动恢复。
- CLI 不自动 `git add`、commit 或 push。
- 可选创建升级分支，但默认不创建。

### 11.4 只向前升级

- 首版不提供 downgrade。
- 执行中失败由 CLI 自动恢复。
- 升级成功后需要回退时使用 Git。
- 版本按目录声明的连续迁移链执行，不跨过缺失步骤。

### 11.5 人工动作与 pending 状态

迁移需要数据库、GORM Gen、依赖整理或其它人工动作时，CLI 只输出说明，不自动执行，并把项目置为 pending：

```yaml
pendingUpgrade:
  targetVersion: v0.3.0
  manualActions:
    - id: regenerate-gorm
      status: pending
```

用户完成并验证后显式确认：

```bash
ccwork-scaffold-cli upgrade --continue --ack regenerate-gorm
```

全部人工动作确认后才推进正式 scaffold 版本并清理 `pendingUpgrade`。pending 状态禁止开始其它升级。

## 12. 生产默认与安全边界

### 12.1 配置

- 镜像只包含 `configs/config.example.yaml`。
- 真实 `configs/config.yaml` 必须通过 Kubernetes Secret/ConfigMap 或 Docker volume 在运行时注入。
- 配置缺失或加载失败时立即退出。
- generator 不读取或复制本机真实配置。

### 12.2 请求日志

请求 body 允许默认记录，但必须满足：

- 设置最大采集字节数，超限只记录长度和截断状态。
- 只解析允许的文本 Content-Type。
- JSON 中 token、password、secret、Authorization、Cookie 等敏感键递归脱敏。
- multipart、二进制和未知类型不输出原始内容。
- 日志读取后恢复 body，不能影响下游 handler。
- 不在错误输出中泄漏完整敏感请求。

### 12.3 运维端点

- `/health` 默认开启。
- `/version` 默认开启，但不返回精确 scaffold 版本。
- 业务二进制 `--version` 可展示 applicationVersion 和 scaffoldVersion。
- Swagger 默认关闭，配置显式开启。
- pprof 默认关闭，配置显式开启。
- Prometheus 默认在独立 metrics 端口开启，支持关闭和限定监听地址。
- 开启 Swagger 或 pprof 时记录启动 WARN。

### 12.4 外部依赖

`minimal` 保持当前约定，启动时强制初始化 DB、Redis 和 RPC。任何关键依赖初始化失败都阻止服务启动。本期不增加隐式跳过或自动识别“未使用依赖”的逻辑。

## 13. 下载、缓存与认证

### 13.1 模板来源

- scaffold Git tag 是模板唯一源码来源。
- CLI 自动获取版本目录和对应不可变 tag。
- master 不能作为默认生产模板。
- 支持受控 repository 配置用于企业镜像或测试，但 URL 不得包含凭据。

### 13.2 缓存与离线

- 缓存按完整 commit 保存，不只按 tag 保存。
- 下载后校验 revision 和内容 SHA-256。
- `--offline` 时禁止网络访问。
- 缓存缺失时明确失败，不回退到其它版本。
- 缓存目录允许通过环境变量配置，便于 CI 预热。
- 缓存不得保存 token、用户名密码或带凭据 Git URL。

### 13.3 认证

generator 完全委托系统 Git 认证，支持 Git credential helper、SSH agent、`.netrc` 或 CI 标准认证环境。CLI 不提供 `--token`，不把认证信息写入清单、缓存、日志或 JSON。

当前 scaffold 本地 remote URL 含内嵌凭据；正式实施前必须在 Git 服务端轮换该凭据，并改用无凭据 URL与标准认证机制。

## 14. 输出、退出码和兼容性

### 14.1 输出格式

所有报告类命令支持：

```text
--output text
--output json
```

默认使用 text。JSON 不输出本机敏感绝对路径或 Git 凭据。

### 14.2 稳定退出码

- `0`：成功或无需升级。
- `1`：参数、网络、认证、清单或文件操作错误。
- `2`：存在文件冲突，需要人工处理。
- `3`：CLI 版本或迁移协议不兼容。
- `4`：目标 scaffold 版本不受支持。

### 14.3 协议兼容

- release 必须声明 `minGeneratorVersion`。
- release 必须声明 `migrationSchemaVersion`。
- 旧 CLI 遇到未知 schema 或操作时立即失败，不跳过、不降级解释、不修改项目。

## 15. 平台和发布完整性

### 15.1 首版平台

- Linux amd64/arm64。
- macOS amd64/arm64。
- Windows amd64。
- 核心实现使用纯 Go，但依赖系统 Git。
- 不以 Bash 或 PowerShell 脚本作为核心入口。
- Windows 无法表达的 Unix executable bit 变化必须明确提示。

支持以下安装方式：

- `go install`。
- GitLab Release 预编译二进制。

### 15.2 完整性

- scaffold 使用 annotated tag。
- GitLab 设置 protected tag，禁止覆盖和移动。
- release 目录记录完整 commit。
- 模板缓存校验 SHA-256。
- generator Release 发布 `checksums.txt`。
- GPG/Sigstore 暂不阻断首版，但协议预留签名字段。

## 16. 发布验证门槛

任何 scaffold 版本加入受支持目录前，CI 必须：

1. 生成 `minimal` 项目。
2. 生成 `example` 项目。
3. 使用不同的 name 和 module，检查不存在旧 module/import 残留。
4. 对两种产物执行 gofmt、`go test ./...`、`go build ./...` 和 `git diff --check`。
5. 扫描真实凭据、本地配置、IDE 文件、构建产物和 `.git` 泄漏。
6. 从上一受支持版本执行 `upgrade --dry-run`。
7. 执行正式升级并再次测试、构建。
8. 验证冲突零写入、失败恢复和 pending 状态。
9. 验证 Linux、macOS、Windows 的路径和换行行为。

未通过上述门槛的版本不能被设置为 active 或 default。

## 17. 本期范围与交付

本期只完成设计和仓库初始化，不提前实现 generator。交付内容如下：

- 完成 generator、版本治理、profile、升级、安全和发布规则的逐项评审。
- 确认独立仓库名称 `ccwork-scaffold-cli`。
- 在 Go 项目父目录初始化本地 Git 仓库。
- 在新仓库 `spec/` 中保存本方案。
- 记录当前 scaffold master 的已验证测试和构建基线。
- 明确当前实验 `v1.0.0` 与正式版本线的隔离方式。
- 明确当前 remote 内嵌凭据必须轮换的安全前置事项。

本期不包含：

- 不创建或推送远程 Git 仓库。
- 不创建 `v0.1.0` 或 `v0.2.0` tag。
- 不提交 Git commit。
- 不编写 generator 生产代码。
- 不修改当前 `ccwork-scaffold-go-http` 源码。

## 18. 后续实施计划

### 阶段 1：固定 scaffold `v0.1.0` 基线

- 轮换并移除当前 remote URL 中的内嵌凭据。
- 复核 master 工作树和远端 HEAD。
- 重跑全量测试、构建和 `git diff --check`。
- 创建 protected annotated tag `v0.1.0`。
- 记录完整 commit，作为 adopt 和首条迁移的来源基线。

完成标准：`v0.1.0` 可由 CLI 精确解析，tag 与 commit 固定，现有 master 基线可重复构建。

### 阶段 2：实现 generator CLI `v0.0.1`

- 建立 Go Module、Cobra 命令和统一错误模型。
- 实现 `version/list/new/inspect/adopt/upgrade`。
- 实现 text/JSON 输出和稳定退出码。
- 实现 Git 下载、无凭据 URL校验、按 commit 缓存和 offline。
- 实现结构化渲染、临时目录生成和目标目录安全检查。
- 实现 `.scaffold.yaml` 读写与 schema 校验。
- 实现 release 目录解析、tag + commit 校验和最低 CLI 版本检查。
- 实现声明式迁移引擎、dry-run、原子写入和失败恢复。
- 实现 pending/continue/ack 状态机。
- 完成 Linux、macOS、Windows 单元测试和集成测试。

完成标准：CLI 可以从本地或测试 Git 仓库生成项目、接管项目并完成无副作用的模拟升级。

### 阶段 3：改造 scaffold 并发布 `v0.2.0`

- 增加 release 目录、profile 和渲染规则。
- 实现可编译的 minimal 注册点，移除示例 user CRUD 和示例 GORM 代码依赖。
- 保持 example profile 的示例能力。
- 对齐 Docker Go 版本与 `go.mod`。
- 从生成范围移除 tracked IDE 文件和本地产物。
- 明确容器配置挂载和启动失败行为。
- 为请求 body 日志增加大小限制、类型白名单和敏感字段脱敏。
- 为 Swagger、pprof、Prometheus 增加生产默认配置和测试。
- 保持 DB、Redis、RPC 强制初始化。
- 增加 `v0.1.0 -> v0.2.0` 声明式迁移。
- 更新中文 README、配置示例、Swagger 说明和发布文档。

完成标准：两种 profile 均可生成、测试、构建；升级链通过；`v0.2.0` 加入 active 并成为 default。

### 阶段 4：发布与试点

- 发布 `ccwork-scaffold-cli v0.0.1` 多平台二进制和 checksums。
- 选择至少一个新项目验证 `new`。
- 选择至少一个未大幅修改的旧项目验证 `adopt`。
- 选择至少一个代表性业务项目验证 `v0.1.0 -> v0.2.0` dry-run。
- 人工复核迁移报告后再执行正式升级。
- 分开记录本地测试、CI、真实业务项目和真实外部依赖验证结果。

完成标准：试点项目能够明确识别版本，升级不覆盖业务文件，失败和冲突均不留下部分写入。

### 阶段 5：后续增强

以下内容不进入 `v0.0.1` / `v0.2.0`，需要独立评审：

- GPG 或 Sigstore 签名验证。
- 企业内部模板镜像和缓存预热服务。
- monorepo 子目录升级。
- 批量资产盘点和批量 dry-run。
- 更多 scaffold profile。
- 更丰富但仍受白名单限制的声明式迁移操作。
- 与 CI 平台集成的升级报告和审批流。

明确不计划恢复任意三方源码合并、远程脚本执行或隐式自动升级。

## 19. 推荐发布顺序

严格按以下顺序推进：

1. 轮换 Git 凭据并确认无凭据 remote。
2. 固定当前 master 为 `v0.1.0`。
3. 实现并测试 CLI `v0.0.1` 的基础协议和本地来源模式。
4. 改造 scaffold profile、release 目录和生产阻断项。
5. 使用候选 commit 验证 minimal/example 和升级链。
6. 发布 scaffold `v0.2.0`。
7. 更新版本目录，将 `v0.2.0` 设置为 active/default。
8. 发布 CLI `v0.0.1` 和 checksums。
9. 开展新项目与既有项目试点。

不得先发布 CLI 默认指向一个尚未完成验证的 scaffold tag，也不得把实验 `v1.0.0` 当作最新版自动选择。

## 20. 主要风险与未验证项

- 新 generator 远程仓库尚未创建，本期仅初始化本地仓库。
- `v0.1.0` 和 `v0.2.0` tag 尚未创建。
- 当前 feature `v1.0.0` 是否已被其它项目实际使用尚未完成组织级盘点。
- Windows 文件权限、换行和原子替换行为尚未实现验证。
- 真实 GitLab protected tag、Release、credential helper 和企业代理尚未现场验证。
- minimal profile 的 DB/store 解耦方案尚未编码验证。
- 请求 body 脱敏规则需要结合现有 API 字段补充测试集合。
- 真实 MySQL、PostgreSQL/HighGo、Gauss、DM、Redis、RPC、Consul、容器和 Kubernetes 尚未验证。

实施阶段必须把聚焦测试、全量测试、构建、静态检查、项目基线阻断和真实环境未验证项分开报告。

## 21. 最终验收标准

完整方案最终交付需同时满足：

- 用户只需安装一次 CLI，即可用一条 `new` 命令生成项目。
- 生成项目不存在 scaffold module/import、IDE 文件、真实配置或凭据残留。
- `minimal` 和 `example` 均能在声明 Go 版本下测试和构建。
- `.scaffold.yaml` 能可靠回答项目来源版本、commit、profile 和升级状态。
- tag 被移动、CLI 太旧、迁移 schema 未知、工作树脏或文件前置条件不满足时均安全失败。
- dry-run 与正式升级使用相同计划逻辑。
- 冲突或失败不留下部分写入。
- 业务文件默认不受 generator 管理。
- 人工动作未确认时不推进正式版本。
- 应用版本、scaffold 版本和 generator 版本始终清晰分离。
