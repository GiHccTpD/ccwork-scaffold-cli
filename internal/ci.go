package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const defaultCIRepository = "https://git.inspur.com/ccwork/go/ops-pipelines.git"
const ciInstallationPath = ".gitlab/ci-installation.yaml"
const ciBundleDirectory = "bundles/go-security"

// CIFile 描述经过版本化清单授权的安装文件。
type CIFile struct {
	Path   string `yaml:"path" json:"path"`
	SHA256 string `yaml:"sha256" json:"sha256"`
	Mode   string `yaml:"mode" json:"mode"`
}

// CIBundle 将安装范围和扫描工具链固定到一个可校验的资产包。
type CIBundle struct {
	SchemaVersion      int      `yaml:"schemaVersion" json:"schemaVersion"`
	Name               string   `yaml:"name" json:"name"`
	MinGoVersion       string   `yaml:"minGoVersion" json:"minGoVersion"`
	ToolchainGoVersion string   `yaml:"toolchainGoVersion" json:"toolchainGoVersion"`
	Files              []CIFile `yaml:"files" json:"files"`
}

// CIRelease 保存独立 CI 版本，不复用 scaffold migration 协议。
type CIRelease struct {
	Version             string `yaml:"version" json:"version"`
	Revision            string `yaml:"revision" json:"revision"`
	Status              string `yaml:"status" json:"status"`
	MinGeneratorVersion string `yaml:"minGeneratorVersion" json:"minGeneratorVersion"`
}

// CICatalog 提供 CI 版本发现和最低安装器要求。
type CICatalog struct {
	SchemaVersion  int         `yaml:"schemaVersion" json:"schemaVersion"`
	DefaultVersion string      `yaml:"defaultVersion" json:"defaultVersion"`
	Releases       []CIRelease `yaml:"releases" json:"releases"`
}

// CIInstallation 保存业务项目中的来源记录，不包含凭据或时间戳。
type CIInstallation struct {
	SchemaVersion int      `yaml:"schemaVersion" json:"schemaVersion"`
	Repository    string   `yaml:"repository" json:"repository"`
	Version       string   `yaml:"version" json:"version"`
	Revision      string   `yaml:"revision" json:"revision"`
	Bundle        CIBundle `yaml:"bundle" json:"bundle"`
}

// CIChange 表达预检或查询所得的文件状态。
type CIChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// CIReport 区分文件安装结果与真实外部环境验收。
type CIReport struct {
	Version      string     `json:"version,omitempty"`
	Revision     string     `json:"revision,omitempty"`
	Status       string     `json:"status"`
	Changes      []CIChange `json:"changes"`
	Requirements []string   `json:"requirements,omitempty"`
	Problem      string     `json:"problem,omitempty"`
}

// newCICommand 构造独立来源的 CI 命令，不改变 scaffold 参数。
func (a *App) newCICommand() *cobra.Command {
	// 🧭1.注册独立来源的版本查询
	var repository string
	command := &cobra.Command{Use: "ci", Short: "安装和检查版本化 GitLab CI"}
	command.PersistentFlags().StringVar(&repository, "ci-repository", defaultCIRepository, "CI Git repository path or credential-free URL")
	command.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, catalog, err := a.FetchCICatalog(cmd.Context(), repository)
		if err != nil {
			return err
		}
		var lines []string
		for _, release := range catalog.Releases {
			lines = append(lines, fmt.Sprintf("%s %s revision=%s minCLI=%s", release.Version, release.Status, release.Revision, release.MinGeneratorVersion))
		}
		return a.WriteReport(catalog, strings.Join(lines, "\n"))
	}})

	// 📦2.注册预览和首次安装
	var dir, version string
	var dryRun bool
	initCommand := &cobra.Command{Use: "init", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		report, err := a.InstallCI(cmd.Context(), repository, dir, version, dryRun)
		report = BuildCIFailureReport(report, err, version)
		if report != nil {
			if outputErr := a.WriteCIReport(*report); outputErr != nil {
				return outputErr
			}
		}
		return err
	}}
	initCommand.Flags().StringVar(&dir, "dir", ".", "Git worktree root")
	initCommand.Flags().StringVar(&version, "ci-version", "", "explicit CI version")
	initCommand.Flags().BoolVar(&dryRun, "dry-run", false, "preview without writing project files")

	// 🔍3.注册离线安装检查
	var inspectDir string
	inspectCommand := &cobra.Command{Use: "inspect", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		report, err := InspectCI(inspectDir)
		report = BuildCIFailureReport(report, err, "")
		if report != nil {
			if outputErr := a.WriteCIReport(*report); outputErr != nil {
				return outputErr
			}
		}
		return err
	}}
	inspectCommand.Flags().StringVar(&inspectDir, "dir", ".", "project directory")
	command.AddCommand(initCommand, inspectCommand)
	return command
}

// BuildCIFailureReport 让预检失败也具有 text/JSON 报告，不改变错误退出码。
func BuildCIFailureReport(report *CIReport, err error, version string) *CIReport {
	if err == nil {
		return report
	}
	if report == nil {
		report = &CIReport{Version: version, Changes: []CIChange{}}
	}
	report.Problem = err.Error()
	if report.Status == "drifted" {
		return report
	}
	report.Status = "error"
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.Code {
		case ExitConflict:
			report.Status = "conflict"
		case ExitIncompatible:
			report.Status = "incompatible"
		case ExitUnsupported:
			report.Status = "unsupported"
		}
	}
	return report
}

// WriteCIReport 输出具有稳定状态字段的报告及接入要求。
func (a *App) WriteCIReport(report CIReport) error {
	var lines []string
	lines = append(lines, fmt.Sprintf("CI %s %s", report.Version, report.Status))
	for _, change := range report.Changes {
		lines = append(lines, change.Status+" "+change.Path)
	}
	if report.Problem != "" {
		lines = append(lines, report.Problem)
	}
	lines = append(lines, report.Requirements...)
	return a.WriteReport(report, strings.Join(lines, "\n"))
}

// NormalizeCIRepository 固定本地来源绝对路径，并拒绝 URL 内的查询凭据。
func NormalizeCIRepository(repository string) (string, error) {
	if err := ValidateRepository(repository); err != nil {
		return "", fmt.Errorf("validate CI repository: %w", err)
	}
	if info, err := os.Stat(repository); err == nil && info.IsDir() {
		absolute, err := filepath.Abs(repository)
		if err != nil {
			return "", fmt.Errorf("resolve CI repository: %w", err)
		}
		return absolute, nil
	}
	parsed, err := url.Parse(repository)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("CI repository URL must not contain query or fragment")
	}
	return repository, nil
}

// FetchCICatalog 复用 Git 认证与缓存，在线刷新版本目录，离线只使用缓存。
func (a *App) FetchCICatalog(ctx context.Context, repository string) (GitSource, CICatalog, error) {
	// 🗂️1.隔离 CI 来源与缓存
	normalized, err := NormalizeCIRepository(repository)
	if err != nil {
		return GitSource{}, CICatalog{}, err
	}
	repository = normalized
	options := a.Options
	options.Repository = repository
	source := GitSource{Options: options}
	base, err := source.FetchCacheDir()
	if err != nil {
		return source, CICatalog{}, fmt.Errorf("resolve CI cache: %w", err)
	}
	options.CacheDir = filepath.Join(base, "ci", FetchSHA256([]byte(repository)))
	source.Options = options
	local := false
	if info, statErr := os.Stat(repository); statErr == nil && info.IsDir() {
		local = true
	}
	var repo string
	if !local && options.Offline {
		repo = filepath.Join(options.CacheDir, "repositories", FetchSHA256([]byte(repository)))
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			return source, CICatalog{}, fmt.Errorf("offline CI repository cache miss: %w", err)
		}
	} else {
		repo, err = source.FetchRepository(ctx)
		if err != nil {
			return source, CICatalog{}, fmt.Errorf("fetch CI repository: %w", err)
		}
	}

	// 🔄2.在线刷新目录，离线复用本机来源
	catalogRef := "HEAD"
	if !local {
		catalogRef = "refs/remotes/origin/HEAD"
		if !options.Offline {
			if _, err := source.FetchGit(ctx, repo, "fetch", "--prune", "origin"); err != nil {
				return source, CICatalog{}, fmt.Errorf("refresh CI repository: %w", err)
			}
		}
	}

	// 🔍3.读取并校验独立版本目录
	data, err := source.FetchGit(ctx, repo, "show", catalogRef+":ci-releases.yaml")
	if err != nil {
		return source, CICatalog{}, fmt.Errorf("read CI catalog: %w", err)
	}
	var catalog CICatalog
	if err := DecodeCIYAML(data, &catalog); err != nil {
		return source, catalog, fmt.Errorf("parse CI catalog: %w", err)
	}
	if err := ValidateCICatalog(catalog); err != nil {
		return source, catalog, err
	}
	source.Options.Repository = repo
	return source, catalog, nil
}

// DecodeCIYAML 拒绝未知字段、重复键及多个文档，避免清单歧义。
func DecodeCIYAML(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode CI YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("CI YAML must contain exactly one document")
	}
	return nil
}

// ValidateCICatalog 校验完整 revision、版本唯一性与默认版本。
func ValidateCICatalog(catalog CICatalog) error {
	if catalog.SchemaVersion != 1 {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported CI catalog schema %d", catalog.SchemaVersion)}
	}
	if len(catalog.Releases) == 0 {
		return fmt.Errorf("CI catalog has no releases")
	}
	seen := map[string]bool{}
	for _, release := range catalog.Releases {
		if err := ValidateVersion(release.Version); err != nil {
			return fmt.Errorf("validate CI version: %w", err)
		}
		if err := ValidateVersion(release.MinGeneratorVersion); err != nil {
			return fmt.Errorf("validate minimum CLI version: %w", err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(release.Revision) {
			return fmt.Errorf("CI release requires full commit SHA")
		}
		if seen[release.Version] {
			return fmt.Errorf("duplicate CI version %s", release.Version)
		}
		seen[release.Version] = true
		if release.Status != "active" && release.Status != "deprecated" && release.Status != "revoked" {
			return fmt.Errorf("invalid CI release status %s", release.Status)
		}
	}
	if !seen[catalog.DefaultVersion] {
		return fmt.Errorf("CI default version is not listed")
	}
	return nil
}

// ValidateCIBundle 限制资产写入范围，并拒绝大小写或父子路径冲突。
func ValidateCIBundle(bundle CIBundle) error {
	if bundle.SchemaVersion != 1 || bundle.Name != "go-security" {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported CI bundle schema or name")}
	}
	if !ValidateCIGoVersion(bundle.MinGoVersion) || !ValidateCIGoVersion(bundle.ToolchainGoVersion) || CompareVersion("v"+bundle.ToolchainGoVersion, "v"+bundle.MinGoVersion) < 0 {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("invalid CI Go toolchain requirements")}
	}
	if len(bundle.Files) == 0 {
		return fmt.Errorf("CI bundle is empty")
	}
	seen := map[string]bool{}
	for _, file := range bundle.Files {
		if err := ValidateCIPath(file.Path); err != nil {
			return fmt.Errorf("validate bundle path: %w", err)
		}
		lower := strings.ToLower(file.Path)
		if seen[lower] {
			return fmt.Errorf("ambiguous CI path %s", file.Path)
		}
		seen[lower] = true
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(file.SHA256) || (file.Mode != "0644" && file.Mode != "0755") {
			return fmt.Errorf("invalid CI hash or mode for %s", file.Path)
		}
	}
	if !seen[".gitlab-ci.yml"] {
		return fmt.Errorf("CI bundle must include .gitlab-ci.yml")
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("CI file-parent collision at %s", parent)
			}
		}
	}
	return nil
}

// ValidateCIPath 仅允许安装 CI 入口、规则、清单与专用脚本。
func ValidateCIPath(name string) error {
	if name != path.Clean(name) || strings.ContainsAny(name, "\\:\x00<>\"|?*") || strings.HasPrefix(name, "/") {
		return fmt.Errorf("invalid CI path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`).MatchString(stem) {
			return fmt.Errorf("reserved CI path component %q", part)
		}
		for _, r := range part {
			if r < 32 {
				return fmt.Errorf("control character in CI path")
			}
		}
		if part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("ambiguous CI path %q", name)
		}
	}
	if name == ".gitlab-ci.yml" || name == ".gitlab/schedules.yml" || strings.HasPrefix(name, ".gitlab/security/") || strings.HasPrefix(name, "scripts/security/") {
		return nil
	}
	return fmt.Errorf("path outside CI installation scope: %s", name)
}

// ResolveCIPath 验证目标所有父组件，拒绝符号链接和大小写别名。
func ResolveCIPath(root, name string) (string, error) {
	if name != ciInstallationPath {
		if err := ValidateCIPath(name); err != nil {
			return "", err
		}
	}
	current := root
	parts := strings.Split(name, "/")
	for index, part := range parts {
		entries, err := os.ReadDir(current)
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect CI parent %s: %w", current, err)
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), part) && entry.Name() != part {
				return "", fmt.Errorf("case-ambiguous CI destination %s", name)
			}
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect CI path %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symbolic link in CI path %s", name)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("non-directory CI parent %s", name)
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("non-regular CI destination %s", name)
		}
	}
	return current, nil
}

// ValidateCIGoVersion 判断受支持的稳定 Go 版本格式。
func ValidateCIGoVersion(version string) bool {
	return regexp.MustCompile(`^1\.[0-9]+(?:\.[0-9]+)?$`).MatchString(version)
}

// ValidateCIProject 核对根 module 和流水线固定工具链，避免安装必然失败的配置。
func ValidateCIProject(ctx context.Context, dir string, bundle CIBundle) error {
	root, err := runGitInDir(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("inspect CI target Git root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve Git root: %w", err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve target root: %w", err)
	}
	if resolvedDir != resolvedRoot {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI target must be Git worktree root")}
	}
	info, err := os.Lstat(filepath.Join(dir, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("CI target requires a regular root go.mod")}
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return fmt.Errorf("read target go.mod: %w", err)
	}
	if !regexp.MustCompile(`(?m)^module\s+\S+`).Match(data) {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("go.mod module directive is required")}
	}
	matches := regexp.MustCompile(`(?m)^go\s+(1\.[0-9]+(?:\.[0-9]+)?)\s*(?://[^\n]*)?$`).FindSubmatch(data)
	if len(matches) != 2 {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("go.mod requires a supported stable Go directive")}
	}
	required := string(matches[1])
	if toolchain := regexp.MustCompile(`(?m)^toolchain\s+go(1\.[0-9]+(?:\.[0-9]+)?)\s*(?://[^\n]*)?$`).FindSubmatch(data); len(toolchain) == 2 && CompareVersion("v"+string(toolchain[1]), "v"+required) > 0 {
		required = string(toolchain[1])
	}
	if CompareVersion("v"+required, "v"+bundle.ToolchainGoVersion) > 0 {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("project Go %s exceeds fixed CI toolchain %s", required, bundle.ToolchainGoVersion)}
	}
	return nil
}

// FetchCIInstallation 读取并校验本地安装来源，防止查询遍历任意文件。
func FetchCIInstallation(dir string) (*CIInstallation, error) {
	name, err := ResolveCIPath(dir, ciInstallationPath)
	if err != nil {
		return nil, fmt.Errorf("resolve CI installation: %w", err)
	}
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read CI installation: %w", err)
	}
	var installation CIInstallation
	if err := DecodeCIYAML(data, &installation); err != nil {
		return nil, fmt.Errorf("parse CI installation: %w", err)
	}
	if installation.SchemaVersion != 1 {
		return nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported CI installation schema %d", installation.SchemaVersion)}
	}
	if !filepath.IsAbs(installation.Repository) {
		if _, err := NormalizeCIRepository(installation.Repository); err != nil {
			return nil, fmt.Errorf("validate installed CI repository: %w", err)
		}
	} else if strings.ContainsRune(installation.Repository, 0) {
		return nil, fmt.Errorf("invalid installed CI source path")
	}
	if err := ValidateVersion(installation.Version); err != nil {
		return nil, fmt.Errorf("validate installed CI version: %w", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(installation.Revision) {
		return nil, fmt.Errorf("invalid installed CI revision")
	}
	if err := ValidateCIBundle(installation.Bundle); err != nil {
		return nil, fmt.Errorf("validate installed CI bundle: %w", err)
	}
	return &installation, nil
}

// InstallCI 在完整预检后补装资产；不接管 scaffold，也不执行外部管理动作。
func (a *App) InstallCI(ctx context.Context, repository, dir, version string, dryRun bool) (*CIReport, error) {
	// 🔍1.锁定显式版本及已校验的资产来源
	if version == "" {
		return nil, fmt.Errorf("--ci-version is required")
	}
	if err := ValidateVersion(version); err != nil {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("validate CI version: %w", err)}
	}
	repository, err := NormalizeCIRepository(repository)
	if err != nil {
		return nil, err
	}
	source, catalog, err := a.FetchCICatalog(ctx, repository)
	if err != nil {
		return nil, err
	}
	var selected *CIRelease
	for _, release := range catalog.Releases {
		if release.Version == version {
			value := release
			selected = &value
			break
		}
	}
	if selected == nil || selected.Status != "active" {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("CI version %s is not active", version)}
	}
	if !a.Options.AllowPrerelease && strings.Contains(version, "-") {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("prerelease CI requires --allow-prerelease")}
	}
	if CompareVersion(FetchGeneratorVersion(), selected.MinGeneratorVersion) < 0 {
		return nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("CI %s requires CLI %s", version, selected.MinGeneratorVersion)}
	}
	sourceDir, revision, err := source.FetchReleaseSource(ctx, Release{Version: version, Revision: selected.Revision})
	if err != nil {
		return nil, fmt.Errorf("fetch CI release: %w", err)
	}
	bundleDir := filepath.Join(sourceDir, ciBundleDirectory)
	manifestData, err := source.FetchGit(ctx, source.Options.Repository, "show", "refs/tags/"+version+":"+ciBundleDirectory+"/bundle.yaml")
	if err != nil {
		return nil, fmt.Errorf("read CI bundle: %w", err)
	}
	var bundle CIBundle
	if err := DecodeCIYAML(manifestData, &bundle); err != nil {
		return nil, fmt.Errorf("parse CI bundle: %w", err)
	}
	if err := ValidateCIBundle(bundle); err != nil {
		return nil, err
	}

	// 🛡️2.检查项目兼容性及所有文件冲突，预览阶段不写入项目
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve CI target: %w", err)
	}
	report := &CIReport{Version: version, Revision: revision, Status: "planned", Changes: []CIChange{}, Requirements: []string{"接入前准备 go-security Runner、固定工具镜像、私有依赖凭据和离线漏洞库。", "配置通知服务和注册凭据；后续 push 会注册 Webhook，默认分支 push 会同步 Schedule。文件安装不代表真实流水线验收通过。"}}
	if err := ValidateCIProject(ctx, dir, bundle); err != nil {
		report.Status = "incompatible"
		return report, err
	}
	current, err := FetchCIInstallation(dir)
	if err != nil {
		return report, &ExitError{Code: ExitConflict, Err: err}
	}
	installation := CIInstallation{SchemaVersion: 1, Repository: repository, Version: version, Revision: revision, Bundle: bundle}
	if current != nil {
		oldJSON, _ := json.Marshal(current)
		newJSON, _ := json.Marshal(installation)
		if !bytes.Equal(oldJSON, newJSON) {
			report.Status = "conflict"
			return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("different CI installation exists; CI update is not supported")}
		}
	}
	payload := map[string][]byte{}
	modes := map[string]os.FileMode{}
	conflict := false
	for _, file := range bundle.Files {
		sourcePath, pathErr := ResolveCIPath(bundleDir, file.Path)
		if pathErr != nil {
			return report, fmt.Errorf("resolve bundle file: %w", pathErr)
		}
		data, readErr := os.ReadFile(sourcePath)
		if readErr != nil {
			return report, fmt.Errorf("read bundle file %s: %w", file.Path, readErr)
		}
		if FetchSHA256(data) != file.SHA256 {
			return report, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("bundle checksum mismatch: %s", file.Path)}
		}
		targetPath, pathErr := ResolveCIPath(dir, file.Path)
		if pathErr != nil {
			return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("resolve destination: %w", pathErr)}
		}
		existing, readErr := os.ReadFile(targetPath)
		status := "add"
		if readErr == nil {
			info, statErr := os.Stat(targetPath)
			if statErr != nil {
				return report, fmt.Errorf("stat destination: %w", statErr)
			}
			mode, _ := strconv.ParseUint(file.Mode, 8, 32)
			if bytes.Equal(existing, data) && (info.Mode().Perm() == os.FileMode(mode) || os.PathSeparator == '\\') {
				status = "same"
			} else {
				status = "conflict"
				conflict = true
			}
		} else if !os.IsNotExist(readErr) {
			return report, fmt.Errorf("read destination: %w", readErr)
		}
		report.Changes = append(report.Changes, CIChange{Path: file.Path, Status: status})
		if status == "add" {
			payload[file.Path] = data
			mode, _ := strconv.ParseUint(file.Mode, 8, 32)
			modes[file.Path] = os.FileMode(mode)
		}
	}
	if conflict {
		report.Status = "conflict"
		return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI file conflicts; no files written")}
	}
	if current == nil {
		data, err := yaml.Marshal(installation)
		if err != nil {
			return report, fmt.Errorf("encode CI installation: %w", err)
		}
		payload[ciInstallationPath] = data
		modes[ciInstallationPath] = 0644
		report.Changes = append(report.Changes, CIChange{Path: ciInstallationPath, Status: "add"})
	}
	if dryRun {
		return report, nil
	}

	// 💾3.在干净工作树安装，失败时回滚本次新增文件和目录
	if err := ValidateCleanWorktree(ctx, dir); err != nil {
		return report, fmt.Errorf("validate CI worktree: %w", err)
	}
	if err := ApplyCIChanges(dir, payload, modes, WriteCIFile); err != nil {
		return report, err
	}
	report.Status = "complete"
	return report, nil
}

// WriteCIFile 写入已独占创建的文件，不重新打开路径。
func WriteCIFile(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write CI file: %w", err)
	}
	return nil
}

// ApplyCIChanges 安装新增文件；普通失败回滚，恢复失败保留完整错误链。
func ApplyCIChanges(dir string, payload map[string][]byte, modes map[string]os.FileMode, write func(*os.File, []byte) error) (result error) {
	var names []string
	for name := range payload {
		names = append(names, name)
	}
	sort.Strings(names)
	var createdFiles, createdDirs []string
	defer func() {
		if result == nil {
			return
		}
		for index := len(createdFiles) - 1; index >= 0; index-- {
			if err := os.Remove(createdFiles[index]); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, fmt.Errorf("rollback CI file: %w", err))
			}
		}
		for index := len(createdDirs) - 1; index >= 0; index-- {
			if err := os.Remove(createdDirs[index]); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, fmt.Errorf("rollback CI directory: %w", err))
			}
		}
	}()
	for _, name := range names {
		full, err := ResolveCIPath(dir, name)
		if err != nil {
			return fmt.Errorf("revalidate CI destination: %w", err)
		}
		var missing []string
		for parent := filepath.Dir(full); parent != dir; parent = filepath.Dir(parent) {
			if _, err := os.Lstat(parent); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect CI directory: %w", err)
			}
			missing = append(missing, parent)
		}
		for index := len(missing) - 1; index >= 0; index-- {
			if err := os.Mkdir(missing[index], 0755); err != nil {
				return fmt.Errorf("create CI directory: %w", err)
			}
			createdDirs = append(createdDirs, missing[index])
		}
		// 独占创建并直接写入同一文件，回滚只删除本次拥有的文件。
		file, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, modes[name])
		if err != nil {
			return fmt.Errorf("create CI destination %s: %w", name, err)
		}
		createdFiles = append(createdFiles, full)
		writeErr := write(file, payload[name])
		modeErr := file.Chmod(modes[name])
		closeErr := file.Close()
		if err := errors.Join(writeErr, modeErr, closeErr); err != nil {
			return fmt.Errorf("install CI file %s (rolling back): %w", name, err)
		}
	}
	return nil
}

// InspectCI 离线核对安装文件；漂移通过退出码和报告共同表达。
func InspectCI(dir string) (*CIReport, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve CI inspection directory: %w", err)
	}
	installation, err := FetchCIInstallation(dir)
	if err != nil {
		return nil, err
	}
	if installation == nil {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("CI installation record not found")}
	}
	report := &CIReport{Version: installation.Version, Revision: installation.Revision, Status: "complete", Changes: []CIChange{}}
	for _, file := range installation.Bundle.Files {
		status := "same"
		name, pathErr := ResolveCIPath(dir, file.Path)
		if pathErr != nil {
			status = "modified"
		} else {
			data, readErr := os.ReadFile(name)
			if os.IsNotExist(readErr) {
				status = "missing"
			} else if readErr != nil {
				return report, fmt.Errorf("read installed CI file: %w", readErr)
			} else {
				info, statErr := os.Stat(name)
				if statErr != nil {
					return report, fmt.Errorf("stat installed CI file: %w", statErr)
				}
				mode, _ := strconv.ParseUint(file.Mode, 8, 32)
				if FetchSHA256(data) != file.SHA256 || (info.Mode().Perm() != os.FileMode(mode) && os.PathSeparator != '\\') {
					status = "modified"
				}
			}
		}
		if status != "same" {
			report.Status = "drifted"
		}
		report.Changes = append(report.Changes, CIChange{Path: file.Path, Status: status})
	}
	if report.Status != "complete" {
		return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("installed CI files have drifted")}
	}
	return report, nil
}
