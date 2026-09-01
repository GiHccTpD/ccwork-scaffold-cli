package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	generatorVersion       = "v0.0.1"
	defaultRepository      = "https://git.ccwork.com/ccwork/go/ccwork-scaffold-go-http.git"
	manifestFile           = ".scaffold.yaml"
	releaseSchemaVersion   = 1
	manifestSchemaVersion  = 1
	migrationSchemaVersion = 1
)

// ExitCode 是 CLI 对外稳定的退出码。
type ExitCode int

const (
	ExitOK           ExitCode = 0
	ExitGeneral      ExitCode = 1
	ExitConflict     ExitCode = 2
	ExitIncompatible ExitCode = 3
	ExitUnsupported  ExitCode = 4
)

// ExitError 将业务错误映射为稳定的 CLI 退出码。
type ExitError struct {
	Code ExitCode
	Err  error
}

// Error 返回带退出码上下文的错误文本。
func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// Unwrap 保留原始错误，便于调用方使用 errors.Is/As。
func (e *ExitError) Unwrap() error { return e.Err }

// AsExitError 将错误安全转换为 CLI 退出错误。
func AsExitError(err error, target **ExitError) bool {
	return errors.As(err, target)
}

// Options 保存命令执行期间共享的来源和输出选项。
type Options struct {
	Repository      string
	CacheDir        string
	Offline         bool
	Output          string
	AllowPrerelease bool
}

// ReleaseCatalog 描述 scaffold 仓库公开的版本目录。
type ReleaseCatalog struct {
	SchemaVersion  int       `yaml:"schemaVersion"`
	DefaultVersion string    `yaml:"defaultVersion"`
	Releases       []Release `yaml:"releases"`
	Legacy         []Legacy  `yaml:"legacy"`
}

// Release 描述一个不可变的 scaffold 版本。
type Release struct {
	Version                string `yaml:"version"`
	Revision               string `yaml:"revision"`
	Status                 string `yaml:"status"`
	Creatable              bool   `yaml:"creatable"`
	UpgradeSource          bool   `yaml:"upgradeSource"`
	MinGeneratorVersion    string `yaml:"minGeneratorVersion"`
	MigrationSchemaVersion int    `yaml:"migrationSchemaVersion"`
	MigrationPath          string `yaml:"migrationPath"`
}

// Legacy 描述不进入正式版本链的历史版本。
type Legacy struct {
	Version string `yaml:"version"`
	Reason  string `yaml:"reason"`
}

// Manifest 是业务项目内唯一受 CLI 管理的身份清单。
type Manifest struct {
	SchemaVersion  int             `yaml:"schemaVersion"`
	Project        Project         `yaml:"project"`
	Scaffold       Scaffold        `yaml:"scaffold"`
	Generator      Generator       `yaml:"generator"`
	PendingUpgrade *PendingUpgrade `yaml:"pendingUpgrade,omitempty"`
}

// Project 保存项目名称、module 和不可变 profile。
type Project struct {
	Name    string `yaml:"name"`
	Module  string `yaml:"module"`
	Profile string `yaml:"profile"`
}

// Scaffold 保存来源仓库、tag、commit 和验证状态。
type Scaffold struct {
	Repository   string `yaml:"repository"`
	Version      string `yaml:"version"`
	Revision     string `yaml:"revision"`
	Verification string `yaml:"verification"`
	Upgradeable  bool   `yaml:"upgradeable"`
}

// Generator 保存创建和最近升级所使用的 CLI 版本。
type Generator struct {
	CreatedWith      string `yaml:"createdWith"`
	LastUpgradedWith string `yaml:"lastUpgradedWith"`
}

// PendingUpgrade 保存等待人工动作确认的升级状态。
type PendingUpgrade struct {
	TargetVersion  string         `yaml:"targetVersion"`
	TargetRevision string         `yaml:"targetRevision"`
	ManualActions  []ManualAction `yaml:"manualActions"`
}

// ManualAction 描述一次必须由业务方完成的人工动作。
type ManualAction struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description,omitempty"`
	Status      string `yaml:"status"`
}

// Migration 描述一个版本之间的声明式迁移。
type Migration struct {
	SchemaVersion int         `yaml:"schemaVersion"`
	From          string      `yaml:"from"`
	To            string      `yaml:"to"`
	Profiles      []string    `yaml:"profiles"`
	Operations    []Operation `yaml:"operations"`
}

// Operation 是受白名单约束的迁移操作。
type Operation struct {
	Type        string `yaml:"type"`
	Path        string `yaml:"path"`
	FromPath    string `yaml:"fromPath"`
	Content     string `yaml:"content"`
	OldContent  string `yaml:"oldContent"`
	SHA256      string `yaml:"sha256"`
	ManualID    string `yaml:"manualId"`
	Description string `yaml:"description"`
}

var (
	projectNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	versionPattern     = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
)

// ValidateProject 校验生成项目的名称、module、profile 和目标目录。
func ValidateProject(name, module, profile string) error {
	if !projectNamePattern.MatchString(name) {
		return fmt.Errorf("invalid project name %q: use lowercase letters, digits and hyphens, starting with a letter", name)
	}
	if err := ValidateModule(module); err != nil {
		return err
	}
	if profile != "minimal" && profile != "example" {
		return fmt.Errorf("unsupported profile %q: must be minimal or example", profile)
	}
	return nil
}

// ValidateModule 校验 module 不包含凭据、路径穿越或非法空白。
func ValidateModule(module string) error {
	if module == "" || strings.ContainsAny(module, " \t\r\n") || strings.HasPrefix(module, "/") || strings.HasSuffix(module, "/") || strings.Contains(module, "..") {
		return fmt.Errorf("invalid Go module %q", module)
	}
	if strings.Contains(module, "://") || strings.Contains(module, "@") {
		return fmt.Errorf("invalid Go module %q: URL and credentials are not allowed", module)
	}
	for _, part := range strings.Split(module, "/") {
		if part == "" || strings.ContainsAny(part, `<>:"\\|?*`) {
			return fmt.Errorf("invalid Go module %q", module)
		}
	}
	return nil
}

// ValidateRepository 校验 Git 来源 URL 不携带用户名或密码。
func ValidateRepository(repository string) error {
	if repository == "" {
		return fmt.Errorf("repository is required")
	}
	if info, err := os.Stat(repository); err == nil && info.IsDir() {
		return nil
	}
	if strings.Contains(repository, "@") && strings.Contains(repository, ":") && !strings.HasPrefix(repository, "https://") && !strings.HasPrefix(repository, "http://") {
		return fmt.Errorf("repository must not contain embedded credentials")
	}
	if strings.Contains(repository, "://") {
		parsed, err := url.Parse(repository)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("invalid repository URL")
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword || parsed.Scheme != "ssh" {
				return fmt.Errorf("repository must not contain embedded credentials")
			}
		}
		return nil
	}
	if strings.HasPrefix(repository, "git@") {
		return fmt.Errorf("SSH scp URL with embedded user is not accepted; use an SSH URL or local path")
	}
	return fmt.Errorf("repository must be a local Git path or URL")
}

// ValidateVersion 校验正式版本使用 vX.Y.Z 的 SemVer 形式。
func ValidateVersion(version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid scaffold version %q", version)
	}
	return nil
}

// FetchManifest 从目标目录读取并校验项目清单。
func FetchManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", manifestFile, err)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", manifestFile, err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidateManifest 校验清单版本和受信字段，避免写入不可解释状态。
func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)}
	}
	if err := ValidateProject(manifest.Project.Name, manifest.Project.Module, manifest.Project.Profile); err != nil {
		return fmt.Errorf("invalid manifest project: %w", err)
	}
	if err := ValidateVersion(manifest.Scaffold.Version); err != nil {
		return fmt.Errorf("invalid manifest scaffold version: %w", err)
	}
	if manifest.Scaffold.Version == "" || manifest.Scaffold.Revision == "" || manifest.Scaffold.Repository == "" {
		return fmt.Errorf("manifest scaffold identity is incomplete")
	}
	if err := ValidateRepository(manifest.Scaffold.Repository); err != nil {
		return fmt.Errorf("invalid manifest repository: %w", err)
	}
	if manifest.Scaffold.Verification != "verified" && manifest.Scaffold.Verification != "unverified" {
		return fmt.Errorf("invalid manifest verification %q", manifest.Scaffold.Verification)
	}
	return nil
}

// WriteManifest 将清单以稳定 YAML 格式写入目标目录。
func WriteManifest(dir string, manifest Manifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", manifestFile, err)
	}
	path := filepath.Join(dir, manifestFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestFile, err)
	}
	return nil
}

// FetchSHA256 计算文件内容的 SHA-256，用于迁移前置条件和缓存身份。
func FetchSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ValidateRelativePath 防止迁移操作越出业务项目根目录。
func ValidateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("invalid relative path %q", path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes project root: %q", path)
	}
	return nil
}
