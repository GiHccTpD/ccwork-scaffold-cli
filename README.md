# ccwork-scaffold-cli

`ccwork-scaffold` 用于从受支持的 `ccwork-scaffold-go-http` Git tag 生成 Go HTTP 服务，并维护业务项目的 scaffold 来源版本。

## 安装

要求本机已安装 Go 1.25.0 和 Git。首次使用私有模块时，先配置 Go 不通过公共代理和校验服务访问 `git.ccwork.com`：

```bash
go env -w GOPRIVATE=git.ccwork.com
go env -w GONOSUMDB=git.ccwork.com
```

安装最新版本：

```bash
go install git.ccwork.com/ccwork/go/ccwork-scaffold-cli/cmd@latest
```

正式发布 tag 后，生产环境建议固定版本，例如：

```bash
go install git.ccwork.com/ccwork/go/ccwork-scaffold-cli/cmd@v0.0.1
```

可执行文件安装到 `GOBIN`；未设置 `GOBIN` 时默认位于 `$(go env GOPATH)/bin`。将该目录加入 `PATH` 后即可验证：

```bash
ccwork-scaffold version
```

仓库和 Go module 服务应优先提供 HTTPS。仅当内网服务确实只支持 HTTP 时，才额外配置 `GOINSECURE=git.ccwork.com`。

## 构建与使用

```bash
go build -o bin/ccwork-scaffold ./cmd

ccwork-scaffold version
ccwork-scaffold list --repository /path/to/ccwork-scaffold-go-http
ccwork-scaffold new \
  --name order-service \
  --module git.inspur.com/ccwork/service/order-service \
  --repository /path/to/ccwork-scaffold-go-http
ccwork-scaffold inspect --dir ./order-service
ccwork-scaffold adopt --from v0.1.0 --dir ./legacy-service
ccwork-scaffold upgrade --to v0.2.0 --dry-run --dir ./order-service
```

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
