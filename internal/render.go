package internal

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if shouldSkipSource(rel) {
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
		data = rewriteSource(rel, data, input)
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
			return fmt.Errorf("create generated parent for %s: %w", outputRel, err)
		}
		if err := os.WriteFile(outputPath, data, 0o644); err != nil {
			return fmt.Errorf("write generated file %s: %w", outputRel, err)
		}
		return nil
	})
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
func rewriteSource(rel string, data []byte, input RenderInput) []byte {
	text := string(data)
	module := input.Module
	normalizedRel := filepath.ToSlash(rel)
	switch {
	case normalizedRel == "go.mod":
		text = strings.Replace(text, "module scaffold\n", "module "+module+"\n", 1)
	case strings.HasSuffix(normalizedRel, ".go"):
		text = strings.ReplaceAll(text, `"scaffold/`, `"`+module+`/`)
		text = rewriteServiceIdentity(text, input.Name)
	case strings.HasSuffix(normalizedRel, ".md"):
		text = strings.Replace(text, "# scaffold", "# "+input.Name, 1)
		text = strings.ReplaceAll(text, "`scaffold`", "`"+module+"`")
		text = strings.ReplaceAll(text, "./build/scaffold", "./build/"+FetchModuleBaseName(module))
		text = strings.ReplaceAll(text, "默认 APP_NAME 由 go.mod 推导为 scaffold", "默认 APP_NAME 由 go.mod 推导为 "+FetchModuleBaseName(module))
		text = strings.ReplaceAll(text, "APP_NAME=scaffold", "APP_NAME="+input.Name)
		text = strings.ReplaceAll(text, "scaffold:v", input.Name+":v")
		text = rewriteServiceIdentity(text, input.Name)
	case strings.HasPrefix(normalizedRel, "docs/"):
		text = rewriteServiceIdentity(text, input.Name)
	}
	return []byte(text)
}

// rewriteServiceIdentity 渲染模板中明确约定的服务名称、路由和 Swagger 元数据。
func rewriteServiceIdentity(text, name string) string {
	text = strings.ReplaceAll(text, `AppName   = "scaffold"`, `AppName   = "`+name+`"`)
	text = strings.ReplaceAll(text, `Short:        "scaffold"`, `Short:        "`+name+`"`)
	text = strings.ReplaceAll(text, `NewPrometheusMetrics("scaffold"`, `NewPrometheusMetrics("`+name+`"`)
	text = strings.ReplaceAll(text, "Shutting down scaffold ...", "Shutting down "+name+" ...")
	text = strings.ReplaceAll(text, "/ccwork/scaffold", "/ccwork/"+name)
	text = strings.ReplaceAll(text, "Scaffold API", name+" API")
	return strings.ReplaceAll(text, "Scaffold HTTP service API", name+" HTTP service API")
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
