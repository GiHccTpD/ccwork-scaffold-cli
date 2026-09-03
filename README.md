# ccwork-scaffold-cli

`ccwork-scaffold-cli` 用于从受支持的 `ccwork-scaffold-go-http` Git tag 生成 Go HTTP 服务，并维护业务项目的 scaffold 来源版本。

## 安装

要求本机已安装 Go 1.25.0 和 Git。首次使用私有模块时，先配置 Go 不通过公共代理和校验服务访问 `git.ccwork.com`：

```bash
go env -w GOPRIVATE=git.ccwork.com
go env -w GONOSUMDB=git.ccwork.com
```

安装最新版本：

```bash
go install github.com/GiHccTpD/ccwork-scaffold-cli@latest
```

正式发布 tag 后，生产环境建议固定版本，例如：

```bash
go install github.com/GiHccTpD/ccwork-scaffold-cli@v0.0.1
```

可执行文件安装到 `GOBIN`；未设置 `GOBIN` 时默认位于 `$(go env GOPATH)/bin`。将该目录加入 `PATH` 后即可验证：

```bash
ccwork-scaffold-cli version
```

仓库和 Go module 服务应优先提供 HTTPS。仅当内网服务确实只支持 HTTP 时，才额外配置 `GOINSECURE=git.ccwork.com`。

## 构建

```bash
go build -o bin/ccwork-scaffold-cli .
```

以下示例假定 `ccwork-scaffold-cli` 已加入 `PATH`。调试本地源码时，也可以把命令中的 `ccwork-scaffold-cli` 替换为 `go run .`。

## 命令示例

### 全局参数

全局参数可以写在子命令前或后，并由全部子命令继承：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--repository <path-or-url>` | `https://git.inspur.com/ccwork/go/ccwork-scaffold-go-http.git` | 指定 scaffold Git 仓库，可以是本地目录或不含凭据的 Git URL。`inspect` 和 `upgrade` 未显式指定时，优先使用项目清单记录的仓库。 |
| `--cache-dir <dir>` | 操作系统用户缓存目录下的 `ccwork-scaffold` | 指定 Git 仓库和按 commit 解压源码的缓存目录，适合 CI 预热或隔离测试缓存。 |
| `--offline` | `false` | 禁止 clone 等网络访问，只使用本地仓库和已有缓存；缓存缺失时直接失败。 |
| `--output <text\|json>` | `text` | 指定报告格式，当前用于 `version`、`list` 和 `inspect`；这些报告命令收到其它值时会报错。 |
| `--allow-prerelease` | `false` | 允许 `new` 或 `adopt` 使用包含预发布标识的 scaffold 版本，例如 `v0.3.0-rc.1`；默认拒绝预发布版本。 |
| `-h`、`--help` | - | 输出根命令或当前子命令的帮助信息，不执行业务操作。 |

### `version`：查看 generator 版本

`version` 没有专属参数，仅接受上述全局参数。默认输出当前 generator 版本。

```bash
ccwork-scaffold-cli version
```

报告类命令支持 JSON 输出：

```bash
ccwork-scaffold-cli --output json version
```

### `list`：查看可用 scaffold 版本

`list` 没有专属参数，仅接受上述全局参数。输出默认版本、各版本状态、是否允许创建、是否可作为升级来源以及最低 generator 版本。

使用默认远程仓库：

```bash
ccwork-scaffold-cli list
```

开发和测试时可以指定本地 scaffold 仓库：

```bash
ccwork-scaffold-cli \
  --repository /path/to/ccwork-scaffold-go-http \
  list
```

只允许读取已有缓存时，显式启用离线模式：

```bash
ccwork-scaffold-cli \
  --cache-dir /path/to/scaffold-cache \
  --offline \
  list
```

### `new`：生成新项目

| 参数 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--name <name>` | 是 | - | 项目名，同时作为默认输出目录名，并用于替换 scaffold 中的服务名和 Go package 名。 |
| `--module <module>` | 是 | - | 新项目的 Go module 路径，例如 `git.inspur.com/ccwork/service/order-service`。 |
| `--profile <minimal\|example>` | 否 | `minimal` | 选择生成 profile；`minimal` 用于生产最小骨架，`example` 保留示例业务代码。 |
| `--scaffold-version <version>` | 否 | 版本目录的 `defaultVersion` | 显式指定要使用的 scaffold 版本，例如 `v0.2.0`。目标版本必须处于 `active` 状态且允许创建。 |
| `--dir <dir>` | 否 | `--name` 的值 | 指定目标目录。目录必须不存在或为空，CLI 不会覆盖非空目录。 |
| `--no-git-init` | 否 | `false` | 不在生成项目中执行 `git init`。CLI 无论如何都不会自动 commit 或 push。 |
| `--verify` | 否 | `false` | 生成期间依次运行 `go test ./...`、`go build ./...` 和 `git diff --check`；任一步失败都不会发布目标目录。 |

使用版本目录中的默认版本和默认 `minimal` profile，输出目录默认为项目名：

```bash
ccwork-scaffold-cli new \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service
```

显式选择 scaffold 版本、profile 和输出目录，并在发布前执行固定验证：

```bash
ccwork-scaffold-cli new \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service \
  --profile example \
  --scaffold-version v0.2.0 \
  --dir ./services/order-service \
  --verify
```

默认会在生成项目中执行 `git init`。不需要初始化 Git 仓库时使用：

```bash
ccwork-scaffold-cli new \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service \
  --no-git-init
```

### `inspect`：查看项目 scaffold 信息

| 参数 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dir <dir>` | 否 | 当前目录 | 指定包含 `.scaffold.yaml` 的业务项目目录。该命令只读，不修改项目。 |

查看指定项目；不传 `--dir` 时检查当前目录：

```bash
ccwork-scaffold-cli inspect --dir ./order-service
```

以 JSON 输出项目、scaffold、generator 和 pending upgrade 信息：

```bash
ccwork-scaffold-cli --output json inspect --dir ./order-service
```

### `adopt`：接管已有项目

| 参数 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dir <dir>` | 否 | 当前目录 | 指定要接管的既有项目目录。 |
| `--from <version>` | 是 | - | 声明项目当前来源的 scaffold 版本；CLI 不会自动猜测。该版本必须存在于版本目录中。 |
| `--profile <minimal\|example>` | 否 | `minimal` | 声明既有项目使用的 profile。 |
| `--name <name>` | 否 | `--dir` 指向目录的名称 | 覆盖自动识别的项目名。 |
| `--module <module>` | 否 | 从 `go.mod` 读取 | 覆盖自动识别的 Go module 路径。 |
| `--record-only` | 否 | `false` | 只写入来源清单，不核对受管理文件；仍会校验声明的来源版本，清单会标记为 `unverified` 且不可升级。 |

来源版本必须通过 `--from` 显式声明，CLI 不会自动猜测。项目名默认取目录名，Go module 默认从 `go.mod` 读取：

```bash
ccwork-scaffold-cli adopt \
  --from v0.1.0 \
  --profile minimal \
  --dir ./legacy-service
```

只登记来源、不校验受管理文件时使用 `--record-only`；生成的项目清单会标记为不可升级：

```bash
ccwork-scaffold-cli adopt \
  --from v0.1.0 \
  --dir ./legacy-service \
  --record-only
```

目录名或 `go.mod` 不能表示实际项目信息时，可以显式覆盖：

```bash
ccwork-scaffold-cli adopt \
  --from v0.1.0 \
  --dir ./legacy-service \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service
```

### `upgrade`：执行声明式升级

| 参数 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dir <dir>` | 否 | 当前目录 | 指定包含 `.scaffold.yaml` 的待升级项目目录。 |
| `--to <version>` | 条件必填 | - | 指定目标 scaffold 版本。普通升级必须在 `--to` 和 `--latest` 中选择一个，二者互斥。 |
| `--latest` | 条件必填 | `false` | 使用版本目录的 `defaultVersion` 作为目标；不能与 `--to` 同时使用。 |
| `--dry-run` | 否 | `false` | 只生成并输出迁移计划，不写入项目文件或清单。建议正式升级前先执行。 |
| `--continue` | 否 | `false` | 继续处于 pending 状态的升级；必须同时传入 `--ack`，且不能与 `--to` 或 `--latest` 组合。 |
| `--ack <action-id>` | 使用 `--continue` 时必填 | - | 确认一个已经由人工完成并验证的 pending action，例如 `regenerate-gorm`。 |

先执行 dry-run 查看迁移计划，不写入文件：

```bash
ccwork-scaffold-cli upgrade \
  --to v0.2.0 \
  --dry-run \
  --dir ./order-service
```

确认计划后执行同一目标版本的正式升级：

```bash
ccwork-scaffold-cli upgrade \
  --to v0.2.0 \
  --dir ./order-service
```

也可以显式升级到版本目录的 `defaultVersion`；`--latest` 与 `--to` 不能同时使用：

```bash
ccwork-scaffold-cli upgrade --latest --dir ./order-service
```

迁移进入 pending 状态后，完成指定人工动作并显式确认，再继续升级：

```bash
ccwork-scaffold-cli upgrade \
  --continue \
  --ack regenerate-gorm \
  --dir ./order-service
```

正式升级要求项目位于独立且干净的 Git worktree。CLI 只执行声明式白名单操作，不自动运行 DDL、`go generate`、`go mod tidy`、Git commit 或 push。

### 内置帮助与命令补全

| 参数 | 说明 |
| --- | --- |
| `help [command]` | 查看根命令或指定子命令的帮助，例如 `help upgrade`。 |
| `completion <bash\|fish\|powershell\|zsh>` | 为指定 shell 输出补全脚本。 |
| `completion <shell> --no-descriptions` | 生成不包含命令描述的补全脚本。 |

查看根命令或指定命令帮助：

```bash
ccwork-scaffold-cli --help
ccwork-scaffold-cli help upgrade
```

Cobra 同时提供 Bash、Fish、PowerShell 和 Zsh 补全脚本。以当前 Zsh 会话为例：

```bash
source <(ccwork-scaffold-cli completion zsh)
```

## 使用约束

默认生成 `minimal` profile，目标目录必须不存在或为空；CLI 不覆盖非空目录、不执行远程脚本、不自动提交或推送 Git。

## scaffold 版本目录

来源仓库根目录的 `releases.yaml`（也兼容 `.scaffold/releases.yaml` 和 `release/releases.yaml`）是唯一版本目录来源：

```yaml
schemaVersion: 1
defaultVersion: v0.2.0
releases:
  - version: v0.2.0
    revision: "<full-commit>"
    status: active
    creatable: true
    upgradeSource: true
    minGeneratorVersion: v0.0.1
    migrationSchemaVersion: 1
```

正式 tag 必须是 annotated tag；CLI 按完整 commit 缓存源码，并拒绝 tag 与 catalog revision 不一致的情况。`--offline` 只使用已有缓存。

## 验证

```bash
GOCACHE=/private/tmp/ccwork-scaffold-cli-go-cache go test ./...
go vet ./...
git diff --check
```

`upgrade` 只接受声明式迁移白名单操作。冲突在内存计划阶段报告，正式写入要求项目位于独立且干净的 Git worktree。
