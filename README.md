# ccwork-scaffold-cli

`ccwork-scaffold` 用于从受支持的 `ccwork-scaffold-go-http` Git tag 生成 Go HTTP 服务，并维护业务项目的 scaffold 来源版本。

## 构建与使用

```bash
go build -o bin/ccwork-scaffold ./cmd/ccwork-scaffold

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
