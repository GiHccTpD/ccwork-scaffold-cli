package internal

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderInput 是生成项目所需的结构化输入。
type RenderInput struct {
	Name    string
	Module  string
	Profile string
}

// RenderProject 将 scaffold 源码复制并按明确文件规则渲染到目标目录。
func RenderProject(source, target string, input RenderInput) error {
	if err := ValidateProject(input.Name, input.Module, input.Profile); err != nil {
		return err
	}
	profile, err := FetchProfile(source, input.Profile)
	if err != nil {
		return err
	}
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk scaffold source: %w", walkErr)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("resolve scaffold path: %w", err)
		}
		if rel == "." {
			return nil
		}
		if shouldSkipSource(rel) || shouldExcludeProfilePath(profile, rel) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		outputRel := renameSourcePath(rel, input.Name)
		outputPath := filepath.Join(target, outputRel)
		if entry.IsDir() {
			return os.MkdirAll(outputPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("scaffold source contains unsupported file %s", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read scaffold file %s: %w", rel, err)
		}
		data = rewriteSource(rel, data, input, profile)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("create generated parent for %s: %w", outputRel, err)
		}
		if err := os.WriteFile(outputPath, data, 0o644); err != nil {
			return fmt.Errorf("write generated file %s: %w", outputRel, err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := applyProfileOverlays(source, target, input, profile); err != nil {
		return err
	}
	return nil
}

// FetchProfile 读取 release 中指定 profile；历史 release 没有 profile 元数据时保留旧渲染行为。
func FetchProfile(source, name string) (*Profile, error) {
	path := filepath.Join(source, ".scaffold", "profiles", name+".yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profile %s: %w", name, err)
	}
	var profile Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("parse profile %s: %w", name, err)
	}
	if err := ValidateProfile(profile, name); err != nil {
		return nil, err
	}
	return &profile, nil
}

// ValidateProfile 校验 profile 的 schema、路径和所有权，避免不受控文件写入。
func ValidateProfile(profile Profile, expectedName string) error {
	if profile.SchemaVersion != profileSchemaVersion {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported profile schema version %d", profile.SchemaVersion)}
	}
	if profile.Name != expectedName {
		return fmt.Errorf("profile name %q does not match requested profile %q", profile.Name, expectedName)
	}
	for _, excluded := range profile.Exclude {
		if err := ValidateProfilePath(excluded); err != nil {
			return fmt.Errorf("invalid profile exclude path: %w", err)
		}
	}
	for _, overlay := range profile.Overlays {
		if err := ValidateRelativePath(overlay.Source); err != nil {
			return fmt.Errorf("invalid profile overlay source: %w", err)
		}
		if err := ValidateRelativePath(overlay.Target); err != nil {
			return fmt.Errorf("invalid profile overlay target: %w", err)
		}
	}
	for _, rule := range profile.Ownership {
		if err := ValidateProfilePath(rule.Path); err != nil {
			return fmt.Errorf("invalid profile ownership path: %w", err)
		}
		if rule.Owner != "scaffold" && rule.Owner != "project" && rule.Owner != "generated" {
			return fmt.Errorf("unsupported profile ownership %q", rule.Owner)
		}
	}
	return nil
}

// ValidateProfilePath 校验可指向单文件或目录前缀的 profile 相对路径。
func ValidateProfilePath(value string) error {
	trimmed := strings.TrimSuffix(value, "/")
	return ValidateRelativePath(trimmed)
}

// shouldExcludeProfilePath 返回指定路径是否由 profile 排除。
func shouldExcludeProfilePath(profile *Profile, rel string) bool {
	if profile == nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, excluded := range profile.Exclude {
		excluded = strings.TrimSuffix(filepath.ToSlash(excluded), "/")
		if rel == excluded || strings.HasPrefix(rel, excluded+"/") {
			return true
		}
	}
	return false
}

// FetchPathOwner 返回与路径最长匹配的 profile 所有权规则。
func FetchPathOwner(profile *Profile, rel string) (string, bool) {
	if profile == nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	owner := ""
	matchedLength := -1
	for _, rule := range profile.Ownership {
		prefix := strings.TrimSuffix(filepath.ToSlash(rule.Path), "/")
		if rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
			continue
		}
		if len(prefix) > matchedLength {
			owner = rule.Owner
			matchedLength = len(prefix)
		}
	}
	return owner, matchedLength >= 0
}

// applyProfileOverlays 渲染 profile 声明的模板文件，并在复制完成后覆盖目标路径。
func applyProfileOverlays(source, target string, input RenderInput, profile *Profile) error {
	if profile == nil {
		return nil
	}
	for _, overlay := range profile.Overlays {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(overlay.Source)))
		if err != nil {
			return fmt.Errorf("read profile overlay %s: %w", overlay.Source, err)
		}
		data = rewriteSource(overlay.Target, data, input, profile)
		output := filepath.Join(target, filepath.FromSlash(overlay.Target))
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return fmt.Errorf("create profile overlay parent for %s: %w", overlay.Target, err)
		}
		if err := os.WriteFile(output, data, 0o644); err != nil {
			return fmt.Errorf("write profile overlay %s: %w", overlay.Target, err)
		}
	}
	return nil
}

// shouldSkipSource 排除来源仓库历史、私有配置和本地产物。
func shouldSkipSource(rel string) bool {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		switch part {
		case ".git", ".idea", ".vscode", ".scaffold", "build", "dist", "bin", "logs", "tmp", "temp":
			return true
		}
	}
	base := filepath.Base(rel)
	if base == "AGENTS.md" || base == manifestFile || base == "config.yaml" || base == ".env" || strings.HasSuffix(base, ".log") || strings.HasSuffix(base, ".test") {
		return true
	}
	if rel == "configs/config.yaml" || strings.HasPrefix(base, "coverage.") {
		return true
	}
	return false
}

// renameSourcePath 仅保留 Windows 构建脚本的兼容命名。
func renameSourcePath(rel, name string) string {
	rel = filepath.ToSlash(rel)
	if rel == "scripts/scaffold.bat" {
		return filepath.FromSlash("scripts/" + name + ".bat")
	}
	return filepath.FromSlash(rel)
}

// rewriteSource 只替换已知 module 和服务身份，不做全仓库盲替换。
func rewriteSource(rel string, data []byte, input RenderInput, profile *Profile) []byte {
	text := string(data)
	module := input.Module
	normalizedRel := filepath.ToSlash(rel)
	switch {
	case normalizedRel == "go.mod":
		text = strings.Replace(text, "module scaffold\n", "module "+module+"\n", 1)
	case strings.HasSuffix(normalizedRel, ".go"):
		text = strings.ReplaceAll(text, `"scaffold/`, `"`+module+`/`)
		if profile == nil || isProjectIdentityPath(profile, normalizedRel) {
			text = rewriteServiceIdentity(text, input.Name)
		}
	case strings.HasSuffix(normalizedRel, ".md"):
		text = strings.Replace(text, "# scaffold", "# "+input.Name, 1)
		text = strings.ReplaceAll(text, "`scaffold`", "`"+module+"`")
		text = strings.ReplaceAll(text, "./build/scaffold", "./build/"+FetchModuleBaseName(module))
		text = strings.ReplaceAll(text, "默认 APP_NAME 由 go.mod 推导为 scaffold", "默认 APP_NAME 由 go.mod 推导为 "+FetchModuleBaseName(module))
		text = strings.ReplaceAll(text, "APP_NAME=scaffold", "APP_NAME="+input.Name)
		text = strings.ReplaceAll(text, "scaffold:v", input.Name+":v")
		if profile == nil || isProjectIdentityPath(profile, normalizedRel) {
			text = rewriteServiceIdentity(text, input.Name)
		}
	case strings.HasPrefix(normalizedRel, "docs/"):
		if profile == nil || isProjectIdentityPath(profile, normalizedRel) {
			text = rewriteServiceIdentity(text, input.Name)
		}
	}
	return []byte(text)
}

// isProjectIdentityPath 限制 V2 服务身份渲染在 profile 明确允许的项目或生成路径内。
func isProjectIdentityPath(profile *Profile, rel string) bool {
	owner, ok := FetchPathOwner(profile, rel)
	return ok && (owner == "project" || owner == "generated")
}

// rewriteServiceIdentity 渲染模板中明确约定的服务名称、路由和 Swagger 元数据。
func rewriteServiceIdentity(text, name string) string {
	text = strings.ReplaceAll(text, `AppName   = "scaffold"`, `AppName   = "`+name+`"`)
	text = strings.ReplaceAll(text, `Short:        "scaffold"`, `Short:        "`+name+`"`)
	text = strings.ReplaceAll(text, `NewPrometheusMetrics("scaffold"`, `NewPrometheusMetrics("`+name+`"`)
	text = strings.ReplaceAll(text, "Shutting down scaffold ...", "Shutting down "+name+" ...")
	text = strings.ReplaceAll(text, "/ccwork/scaffold", "/ccwork/"+name)
	text = strings.ReplaceAll(text, "Scaffold API", name+" API")
	text = strings.ReplaceAll(text, "Scaffold HTTP service API", name+" HTTP service API")
	text = strings.ReplaceAll(text, `Name: "scaffold"`, `Name: "`+name+`"`)
	text = strings.ReplaceAll(text, `Summary: "scaffold"`, `Summary: "`+name+`"`)
	return strings.ReplaceAll(text, `MetricsNamespace: "scaffold"`, `MetricsNamespace: "`+name+`"`)
}

// FetchModuleBaseName 返回 Go module 路径最后一段，作为默认构建产物名称。
func FetchModuleBaseName(module string) string {
	return path.Base(module)
}

// FetchModuleFromGoMod 读取业务项目 go.mod 的 module 声明。
func FetchModuleFromGoMod(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			if err := ValidateModule(fields[1]); err != nil {
				return "", err
			}
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("module declaration not found in go.mod")
}

// FetchProjectName 从目标目录名推导生成时使用的项目名称。
func FetchProjectName(dir string) (string, error) {
	name := filepath.Base(filepath.Clean(dir))
	if err := ValidateProject(name, "example.invalid/project", "example"); err != nil {
		return "", fmt.Errorf("derive project name from directory: %w", err)
	}
	return name, nil
}
