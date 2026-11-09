package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newCIReferenceCommand 输出可供人工合并的引用入口，不覆盖存量流水线。
func (a *App) newCIReferenceCommand(repository *string) *cobra.Command {
	var version, project, localConfig string
	command := &cobra.Command{Use: "reference", Short: "输出固定版本的运行时引用，不写业务工程", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := a.FetchCIReference(cmd.Context(), *repository, version, project, localConfig)
		if err != nil {
			return err
		}
		return a.WriteReport(map[string]string{"configuration": string(data)}, string(data))
	}}
	command.Flags().StringVar(&version, "ci-version", "", "explicit published CI version with runtime support")
	command.Flags().StringVar(&project, "ci-project", defaultCISourceProject, "GitLab project path on the same instance")
	command.Flags().StringVar(&localConfig, "local-config", "", "project overrides YAML included after shared configuration; configure OPS_PIPELINES_READ_TOKEN there when cross-project archive rejects CI_JOB_TOKEN")
	return command
}

// FetchCIReference 仅从已校验的正式来源生成同 SHA 的配置和资产引用。
func (a *App) FetchCIReference(ctx context.Context, repository, version, project, localConfig string) ([]byte, error) {
	// 🔍1.验证 GitLab 项目与可选配置路径，避免输出含凭据或越界的引用
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`).MatchString(project) {
		return nil, fmt.Errorf("ci-project must be a GitLab project path")
	}
	for _, part := range strings.Split(project, "/") {
		if part == "." || part == ".." {
			return nil, fmt.Errorf("ci-project contains an unsafe component")
		}
	}
	if localConfig != "" {
		if err := ValidateRelativePath(localConfig); err != nil {
			return nil, fmt.Errorf("validate local CI configuration: %w", err)
		}
		if !strings.HasSuffix(localConfig, ".yml") && !strings.HasSuffix(localConfig, ".yaml") {
			return nil, fmt.Errorf("local CI configuration must be YAML")
		}
	}

	// 📦2.复用正式 catalog、annotated tag、完整 revision 和最低 CLI 校验
	installation, _, err := a.FetchCIRelease(ctx, repository, version, false)
	if err != nil {
		return nil, fmt.Errorf("fetch runtime CI release: %w", err)
	}
	source, _, err := a.FetchCICatalog(ctx, repository)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime Git source: %w", err)
	}
	kind, err := source.FetchGit(ctx, source.Options.Repository, "cat-file", "-t", installation.Revision+":runtime/go-security.yml")
	if err != nil || strings.TrimSpace(string(kind)) != "blob" {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("CI %s does not provide a runtime entry; publish a runtime-capable release first", version)}
	}

	// 📝3.模板与运行时下载固定同一 SHA，项目差异通过后置配置合并
	includes := []map[string]string{{"project": project, "ref": installation.Revision, "file": "/runtime/go-security.yml"}}
	if localConfig != "" {
		includes = append(includes, map[string]string{"local": "/" + localConfig})
	}
	config := struct {
		Include   []map[string]string `yaml:"include"`
		Variables map[string]string   `yaml:"variables"`
	}{Include: includes, Variables: map[string]string{"OPS_PIPELINES_PROJECT": project, "OPS_PIPELINES_REVISION": installation.Revision}}
	data, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("render runtime CI reference: %w", err)
	}
	// 📨4.附上共享资产读取凭据兼容提示：部分实例（如 GitLab 18.11.12-ee）跨项目 archive 对 CI_JOB_TOKEN 返回 404
	hint := "# 共享资产默认以 CI_JOB_TOKEN 拉取固定 revision 归档；若实例跨项目 archive 返回 404（GitLab 18.11.12-ee 已观测），\n" +
		"# 在项目 overrides（--local-config 指向的 YAML，如 .gitlab/ci-runtime-overrides.yml）的 variables 中配置：\n" +
		"#   OPS_PIPELINES_READ_TOKEN: \"$GITLAB_READ_TOKEN\"\n" +
		"# 引用既有只读凭据（如组级 Masked GITLAB_READ_TOKEN）；不要在 YAML 写入真实凭据，也不为 MR 放宽受保护变量，\n" +
		"# 最终以 fetch-ops-pipelines 任务实际成功为准。\n"
	return append([]byte(hint), data...), nil
}

// InspectCIReference 离线检查配置与资产 SHA 一致，真实权限和执行结果另行验收。
func InspectCIReference(dir string) (*CIReport, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".gitlab-ci.yml"))
	if err != nil {
		return nil, fmt.Errorf("read runtime CI configuration: %w", err)
	}
	var config struct {
		Include   []map[string]string `yaml:"include"`
		Variables map[string]string   `yaml:"variables"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse runtime CI configuration: %w", err)
	}
	var reference map[string]string
	for _, include := range config.Include {
		if include["file"] == "/runtime/go-security.yml" && include["project"] != "" {
			if reference != nil {
				return nil, fmt.Errorf("duplicate runtime CI references")
			}
			reference = include
		}
	}
	if reference == nil {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("CI installation record or runtime reference not found")}
	}
	revision := reference["ref"]
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) || config.Variables["OPS_PIPELINES_REVISION"] != revision || config.Variables["OPS_PIPELINES_PROJECT"] != reference["project"] {
		return nil, &ExitError{Code: ExitConflict, Err: fmt.Errorf("runtime CI template and assets must reference the same full SHA and project")}
	}
	requirements := []string{
		"仅核对本地引用；正式来源、合并配置、跨仓库权限及真实 Pipeline 尚需验收。",
		"共享资产默认以 CI_JOB_TOKEN 拉取；若实例跨项目 archive 返回 404，需在项目 overrides 配置 OPS_PIPELINES_READ_TOKEN 引用既有只读凭据，并以 fetch-ops-pipelines 任务实际成功为准。",
	}
	return &CIReport{Revision: revision, Status: "referenced", Changes: []CIChange{}, Requirements: requirements}, nil
}

// shouldSkipCIImplementation 识别运行时模式无需复制的共享实现与旧安装身份。
func shouldSkipCIImplementation(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == ".gitlab-ci.yml" || rel == ciInstallationPath || rel == ciCustomizationsPath {
		return true
	}
	for _, prefix := range []string{"scripts/security", "scripts/release", ".gitlab/security", ".gitlab/release"} {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}
