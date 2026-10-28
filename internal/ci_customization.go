package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const ciCustomizationsPath = ".gitlab/ci-customizations.yaml"

// CICustomization 显式保留来源 hash 与人工确认的项目内容，不改写来源清单。
type CICustomization struct {
	Path           string `yaml:"path"`
	SourceSHA256   string `yaml:"sourceSHA256"`
	AcceptedSHA256 string `yaml:"acceptedSHA256"`
	Mode           string `yaml:"mode"`
	Reason         string `yaml:"reason"`
}

// CICustomizations 将项目定制绑定到准确的安装版本，旧 CLI 仍会报告原版漂移。
type CICustomizations struct {
	SchemaVersion int               `yaml:"schemaVersion"`
	Revision      string            `yaml:"revision"`
	Files         []CICustomization `yaml:"files"`
}

// FetchCICustomizations 校验独立的人工确认记录；不将未确认修改当作定制成功。
func FetchCICustomizations(dir string, installation CIInstallation) (map[string]CICustomization, error) {
	name, err := ResolveCIPath(dir, ciCustomizationsPath)
	if err != nil {
		return nil, fmt.Errorf("resolve CI customizations: %w", err)
	}
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return map[string]CICustomization{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read CI customizations: %w", err)
	}
	var record CICustomizations
	if err := DecodeCIYAML(data, &record); err != nil {
		return nil, fmt.Errorf("decode CI customizations: %w", err)
	}
	if record.SchemaVersion != 1 || record.Revision != installation.Revision {
		return nil, fmt.Errorf("CI customizations do not match installed revision")
	}
	source := map[string]CIFile{}
	for _, file := range installation.Bundle.Files {
		source[file.Path] = file
	}
	result := map[string]CICustomization{}
	for _, file := range record.Files {
		original, exists := source[file.Path]
		if _, duplicate := result[file.Path]; !exists || duplicate || original.SHA256 != file.SourceSHA256 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(file.AcceptedSHA256) || (file.Mode != "0644" && file.Mode != "0755") || strings.TrimSpace(file.Reason) == "" {
			return nil, fmt.Errorf("invalid CI customization record")
		}
		result[file.Path] = file
	}
	return result, nil
}

// RecordCICustomization 仅在操作者显式指定路径和理由后记录当前定制；不修改原版来源。
func RecordCICustomization(dir, path, reason string) (*CIReport, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve CI customization root: %w", err)
	}
	installation, err := FetchCIInstallation(dir)
	if err != nil {
		return nil, fmt.Errorf("read customized installation: %w", err)
	}
	if installation == nil {
		return nil, fmt.Errorf("CI customization requires an existing installation")
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return nil, fmt.Errorf("customization requires a review reason of 1-500 bytes")
	}
	var original *CIFile
	for _, file := range installation.Bundle.Files {
		if file.Path == path {
			copy := file
			original = &copy
		}
	}
	if original == nil {
		return nil, fmt.Errorf("customization path is not an installed asset")
	}
	full, err := ResolveCIPath(dir, path)
	if err != nil {
		return nil, fmt.Errorf("resolve customized CI asset: %w", err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read customized CI asset: %w", err)
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("stat customized CI asset: %w", err)
	}
	mode := fmt.Sprintf("%04o", info.Mode().Perm())
	if mode != "0644" && mode != "0755" {
		return nil, fmt.Errorf("unsupported customized file permissions")
	}
	if FetchSHA256(data) == original.SHA256 && mode == original.Mode {
		return nil, fmt.Errorf("CI asset matches source; no customization to acknowledge")
	}
	customizations, err := FetchCICustomizations(dir, *installation)
	if err != nil {
		return nil, fmt.Errorf("read previous CI customization approvals: %w", err)
	}
	customizations[path] = CICustomization{path, original.SHA256, FetchSHA256(data), mode, reason}
	record := CICustomizations{SchemaVersion: 1, Revision: installation.Revision}
	for _, file := range installation.Bundle.Files {
		if custom, exists := customizations[file.Path]; exists {
			record.Files = append(record.Files, custom)
		}
	}
	payload, err := yaml.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode CI customizations: %w", err)
	}
	name, err := ResolveCIPath(dir, ciCustomizationsPath)
	if err != nil {
		return nil, fmt.Errorf("resolve CI customization output: %w", err)
	}
	if _, err := os.Lstat(name); os.IsNotExist(err) {
		if err := ApplyCIChanges(dir, map[string][]byte{ciCustomizationsPath: payload}, map[string]os.FileMode{ciCustomizationsPath: 0644}, WriteCIFile); err != nil {
			return nil, fmt.Errorf("create CI customizations: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat CI customizations: %w", err)
	} else if err := ReplaceCIFile(name, payload, 0644, WriteCIFile); err != nil {
		return nil, fmt.Errorf("save CI customizations: %w", err)
	}
	return InspectCI(dir)
}
