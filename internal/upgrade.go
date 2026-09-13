package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// UpgradeReport 描述一次 dry-run 或正式升级的结果。
type UpgradeReport struct {
	From          string         `json:"from" yaml:"from"`
	To            string         `json:"to" yaml:"to"`
	Changes       []PlanChange   `json:"changes" yaml:"changes"`
	ManualActions []ManualAction `json:"manualActions,omitempty" yaml:"manualActions,omitempty"`
	DryRun        bool           `json:"dryRun" yaml:"dryRun"`
}

// PlanChange 描述一个将被新增、修改、重命名或删除的文件。
type PlanChange struct {
	Action string `json:"action" yaml:"action"`
	Path   string `json:"path" yaml:"path"`
	From   string `json:"from,omitempty" yaml:"from,omitempty"`
}

// Upgrade 读取清单、计算迁移计划并在无冲突时一次性写入。
func (a *App) Upgrade(ctx context.Context, dir, target string, latest, dryRun bool) error {
	manifest, err := FetchManifest(dir)
	if err != nil {
		return err
	}
	if manifest.PendingUpgrade != nil {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("project has pending upgrade to %s; continue it before starting another upgrade", manifest.PendingUpgrade.TargetVersion)}
	}
	source := a.FetchManifestSource(manifest)
	catalog, err := source.FetchCatalog(ctx)
	if err != nil {
		return err
	}
	if latest {
		target = catalog.DefaultVersion
	}
	targetRelease, err := FetchRelease(catalog, target)
	if err != nil {
		return err
	}
	if targetRelease.Status != "active" {
		return &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("scaffold version %s is not an active upgrade target", target)}
	}
	if targetRelease.MigrationSchemaVersion > migrationSchemaVersion {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("scaffold %s requires migration schema version %d", target, targetRelease.MigrationSchemaVersion)}
	}
	if !manifest.Scaffold.Upgradeable {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("project is not upgradeable: verification=%s", manifest.Scaffold.Verification)}
	}
	if CompareVersion(target, manifest.Scaffold.Version) <= 0 {
		return a.WriteReport(UpgradeReport{From: manifest.Scaffold.Version, To: target, DryRun: dryRun}, "no upgrade needed")
	}
	targetSourceDir, targetRevision, err := source.FetchReleaseSource(ctx, targetRelease)
	if err != nil {
		return err
	}
	migration, err := source.FetchMigration(ctx, manifest.Scaffold.Version, target, targetRelease)
	if err != nil {
		return err
	}
	var report UpgradeReport
	var changes map[string]*[]byte
	var pending []ManualAction
	switch migration.SchemaVersion {
	case 1:
		report, changes, pending, err = BuildMigrationPlan(dir, manifest, migration)
	case 2:
		currentRelease, releaseErr := FetchRelease(catalog, manifest.Scaffold.Version)
		if releaseErr != nil {
			return releaseErr
		}
		currentSourceDir, _, sourceErr := source.FetchReleaseSource(ctx, currentRelease)
		if sourceErr != nil {
			return sourceErr
		}
		oldGeneratedDir, oldCleanup, renderErr := FetchRenderedRelease(currentSourceDir, manifest.Project)
		if renderErr != nil {
			return renderErr
		}
		defer oldCleanup()
		newGeneratedDir, newCleanup, renderErr := FetchRenderedRelease(targetSourceDir, manifest.Project)
		if renderErr != nil {
			return renderErr
		}
		defer newCleanup()
		profile, profileErr := FetchProfile(targetSourceDir, manifest.Project.Profile)
		if profileErr != nil {
			return profileErr
		}
		report, changes, pending, err = BuildManagedMigrationPlan(dir, manifest, migration, oldGeneratedDir, newGeneratedDir, profile)
	default:
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported migration schema version %d", migration.SchemaVersion)}
	}
	if err != nil {
		return err
	}
	report.To = target
	report.DryRun = dryRun
	report.ManualActions = pending
	if dryRun {
		return a.WriteReport(report, FormatUpgrade(report))
	}
	if err := ValidateCleanWorktree(ctx, dir); err != nil {
		return err
	}
	if len(pending) > 0 {
		manifest.PendingUpgrade = &PendingUpgrade{TargetVersion: target, TargetRevision: targetRevision, ManualActions: pending}
		if err := ApplyChanges(dir, changes, manifest); err != nil {
			return err
		}
		return a.WriteReport(report, FormatUpgrade(report))
	}
	manifest.Scaffold.Version = target
	manifest.Scaffold.Revision = targetRevision
	manifest.Generator.LastUpgradedWith = FetchGeneratorVersion()
	if err := ApplyChanges(dir, changes, manifest); err != nil {
		return err
	}
	return a.WriteReport(report, FormatUpgrade(report))
}

// ContinueUpgrade 确认一个人工动作并在全部动作完成后推进版本。
func (a *App) ContinueUpgrade(ctx context.Context, dir, ack string) error {
	manifest, err := FetchManifest(dir)
	if err != nil {
		return err
	}
	if manifest.PendingUpgrade == nil {
		return fmt.Errorf("project has no pending upgrade")
	}
	if err := ValidateCleanWorktree(ctx, dir); err != nil {
		return err
	}
	remaining := make([]ManualAction, 0, len(manifest.PendingUpgrade.ManualActions))
	found := false
	for _, action := range manifest.PendingUpgrade.ManualActions {
		if action.ID == ack {
			found = true
			continue
		}
		remaining = append(remaining, action)
	}
	if !found {
		return fmt.Errorf("pending manual action %q not found", ack)
	}
	if len(remaining) > 0 {
		manifest.PendingUpgrade.ManualActions = remaining
		if err := WriteManifest(dir, manifest); err != nil {
			return err
		}
		return a.WriteReport(manifest.PendingUpgrade, "acknowledged "+ack)
	}
	source := a.FetchManifestSource(manifest)
	catalog, err := source.FetchCatalog(ctx)
	if err != nil {
		return err
	}
	release, err := FetchRelease(catalog, manifest.PendingUpgrade.TargetVersion)
	if err != nil {
		return err
	}
	_, revision, err := source.FetchReleaseSource(ctx, release)
	if err != nil {
		return err
	}
	manifest.Scaffold.Version = release.Version
	manifest.Scaffold.Revision = revision
	manifest.Generator.LastUpgradedWith = FetchGeneratorVersion()
	manifest.PendingUpgrade = nil
	if err := WriteManifest(dir, manifest); err != nil {
		return err
	}
	return a.WriteReport(manifest.Scaffold, "upgrade completed to "+release.Version)
}

// VerifyAndAdopt 对既有项目执行基线指纹核对，成功后才允许升级。
func (a *App) VerifyAndAdopt(ctx context.Context, dir string, manifest Manifest) error {
	release, sourceDir, revision, err := a.FetchRelease(ctx, manifest.Scaffold.Version)
	if err != nil {
		return err
	}
	if !release.UpgradeSource {
		return &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("scaffold version %s cannot be used as an adoption source", release.Version)}
	}
	stage, err := os.MkdirTemp("", ".ccwork-adopt-")
	if err != nil {
		return fmt.Errorf("create adoption staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := RenderProject(sourceDir, stage, RenderInput{Name: manifest.Project.Name, Module: manifest.Project.Module, Profile: manifest.Project.Profile}); err != nil {
		return err
	}
	profile, err := FetchProfile(sourceDir, manifest.Project.Profile)
	if err != nil {
		return err
	}
	if err := CompareOwnedGeneratedFiles(dir, stage, profile); err != nil {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("adoption verification failed: %w", err)}
	}
	manifest.Scaffold.Revision = revision
	manifest.Scaffold.Verification = "verified"
	manifest.Scaffold.Upgradeable = true
	if err := WriteManifest(dir, manifest); err != nil {
		return err
	}
	return a.WriteReport(manifest.Scaffold, "adopted "+release.Version)
}

// FetchRenderedRelease 将不可变 release 按项目身份渲染到临时目录，供 schema v2 计算基线。
func FetchRenderedRelease(sourceDir string, project Project) (string, func(), error) {
	stage, err := os.MkdirTemp("", ".ccwork-scaffold-render-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create rendered release staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	if err := RenderProject(sourceDir, stage, RenderInput{Name: project.Name, Module: project.Module, Profile: project.Profile}); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return stage, cleanup, nil
}

// BuildMigrationPlan 只读计算迁移结果，遇到首个冲突时不写入任何文件。
func BuildMigrationPlan(dir string, manifest Manifest, migration Migration) (UpgradeReport, map[string]*[]byte, []ManualAction, error) {
	if migration.SchemaVersion != 1 {
		return UpgradeReport{}, nil, nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported migration schema version %d", migration.SchemaVersion)}
	}
	if migration.From != manifest.Scaffold.Version {
		return UpgradeReport{}, nil, nil, fmt.Errorf("migration source %s does not match project version %s", migration.From, manifest.Scaffold.Version)
	}
	if migration.To == "" {
		return UpgradeReport{}, nil, nil, fmt.Errorf("migration target is required")
	}
	if len(migration.Profiles) > 0 && !slices.Contains(migration.Profiles, manifest.Project.Profile) {
		return UpgradeReport{}, nil, nil, fmt.Errorf("migration %s -> %s does not support profile %s", migration.From, migration.To, manifest.Project.Profile)
	}
	changes := make(map[string]*[]byte)
	report := UpgradeReport{From: migration.From, To: migration.To}
	pending := make([]ManualAction, 0)
	for _, operation := range migration.Operations {
		change, pendingAction, err := ApplyOperation(dir, changes, manifest.Project, operation)
		if err != nil {
			if exitErr := asExit(err); exitErr != nil {
				return UpgradeReport{}, nil, nil, err
			}
			return UpgradeReport{}, nil, nil, &ExitError{Code: ExitConflict, Err: err}
		}
		if pendingAction != nil {
			pending = append(pending, *pendingAction)
			continue
		}
		if change != nil {
			report.Changes = append(report.Changes, *change)
		}
	}
	return report, changes, pending, nil
}

// BuildManagedMigrationPlan 以旧、新渲染基线为前置条件，计算 schema v2 的稳定文件迁移。
func BuildManagedMigrationPlan(projectDir string, manifest Manifest, migration Migration, oldGeneratedDir, newGeneratedDir string, targetProfile *Profile) (UpgradeReport, map[string]*[]byte, []ManualAction, error) {
	if migration.SchemaVersion != 2 {
		return UpgradeReport{}, nil, nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported managed migration schema version %d", migration.SchemaVersion)}
	}
	if migration.From != manifest.Scaffold.Version || migration.To == "" {
		return UpgradeReport{}, nil, nil, fmt.Errorf("migration source or target does not match project version")
	}
	if len(migration.Profiles) > 0 && !slices.Contains(migration.Profiles, manifest.Project.Profile) {
		return UpgradeReport{}, nil, nil, fmt.Errorf("migration %s -> %s does not support profile %s", migration.From, migration.To, manifest.Project.Profile)
	}
	report := UpgradeReport{From: migration.From, To: migration.To}
	changes := make(map[string]*[]byte)
	pending := make([]ManualAction, 0)
	for _, operation := range migration.Operations {
		change, action, err := BuildManagedOperation(projectDir, oldGeneratedDir, newGeneratedDir, changes, targetProfile, operation)
		if err != nil {
			if asExit(err) != nil {
				return UpgradeReport{}, nil, nil, err
			}
			return UpgradeReport{}, nil, nil, &ExitError{Code: ExitConflict, Err: err}
		}
		if change != nil {
			report.Changes = append(report.Changes, *change)
		}
		if action != nil {
			pending = append(pending, *action)
		}
	}
	return report, changes, pending, nil
}

// BuildManagedOperation 只允许迁移文件明确声明且由新 profile 管理的稳定路径。
func BuildManagedOperation(projectDir, oldGeneratedDir, newGeneratedDir string, changes map[string]*[]byte, targetProfile *Profile, operation Operation) (*PlanChange, *ManualAction, error) {
	if operation.Type == "require-manual-action" {
		if operation.ManualID == "" {
			return nil, nil, fmt.Errorf("manual action id is required")
		}
		return nil, &ManualAction{ID: operation.ManualID, Description: operation.Description, Status: "pending"}, nil
	}
	if err := ValidateRelativePath(operation.Path); err != nil {
		return nil, nil, err
	}
	if err := ValidateManagedPath(targetProfile, operation.Path); err != nil {
		return nil, nil, err
	}
	path := filepath.FromSlash(operation.Path)
	newData, newExists, err := FetchPlannedFile(newGeneratedDir, nil, path)
	if err != nil {
		return nil, nil, fmt.Errorf("read new generated file %s: %w", operation.Path, err)
	}
	currentData, currentExists, err := FetchPlannedFile(projectDir, changes, path)
	if err != nil {
		return nil, nil, err
	}
	switch operation.Type {
	case "sync-managed-file":
		oldData, oldExists, err := FetchPlannedFile(oldGeneratedDir, nil, path)
		if err != nil {
			return nil, nil, fmt.Errorf("read old generated file %s: %w", operation.Path, err)
		}
		if !oldExists || !newExists || !currentExists || FetchSHA256(currentData) != FetchSHA256(oldData) {
			return nil, nil, fmt.Errorf("sync conflicts because %s was modified or has no generated baseline", operation.Path)
		}
		content := append([]byte(nil), newData...)
		changes[path] = &content
		return &PlanChange{Action: "replace", Path: operation.Path}, nil, nil
	case "add-managed-file":
		if !newExists || currentExists {
			return nil, nil, fmt.Errorf("add conflicts because %s already exists or has no target baseline", operation.Path)
		}
		content := append([]byte(nil), newData...)
		changes[path] = &content
		return &PlanChange{Action: "add", Path: operation.Path}, nil, nil
	case "move-managed-file":
		if err := ValidateRelativePath(operation.FromPath); err != nil {
			return nil, nil, err
		}
		fromPath := filepath.FromSlash(operation.FromPath)
		oldData, oldExists, err := FetchPlannedFile(oldGeneratedDir, nil, fromPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read old generated file %s: %w", operation.FromPath, err)
		}
		actualData, actualExists, err := FetchPlannedFile(projectDir, changes, fromPath)
		if err != nil {
			return nil, nil, err
		}
		if !oldExists || !newExists || !actualExists || currentExists || FetchSHA256(actualData) != FetchSHA256(oldData) {
			return nil, nil, fmt.Errorf("move conflicts for %s -> %s", operation.FromPath, operation.Path)
		}
		changes[fromPath] = nil
		content := append([]byte(nil), newData...)
		changes[path] = &content
		return &PlanChange{Action: "rename", Path: operation.Path, From: operation.FromPath}, nil, nil
	default:
		return nil, nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported managed migration operation %q", operation.Type)}
	}
}

// ValidateManagedPath 拒绝 migration 写入 project-owned、generated 或未声明的路径。
func ValidateManagedPath(profile *Profile, path string) error {
	owner, declared := FetchPathOwner(profile, path)
	if !declared || owner != "scaffold" {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("migration path %s is not scaffold-owned", path)}
	}
	return nil
}

// ApplyOperation 在内存计划中执行单个白名单迁移操作。
func ApplyOperation(dir string, changes map[string]*[]byte, project Project, operation Operation) (*PlanChange, *ManualAction, error) {
	if operation.Type == "require-manual-action" {
		if operation.ManualID == "" {
			return nil, nil, fmt.Errorf("manual action id is required")
		}
		return nil, &ManualAction{ID: operation.ManualID, Description: operation.Description, Status: "pending"}, nil
	}
	if err := ValidateRelativePath(operation.Path); err != nil {
		return nil, nil, err
	}
	path := filepath.FromSlash(operation.Path)
	current, exists, err := FetchPlannedFile(dir, changes, path)
	if err != nil {
		return nil, nil, err
	}
	switch operation.Type {
	case "add":
		if exists {
			return nil, nil, fmt.Errorf("add conflicts because %s already exists", operation.Path)
		}
		content := []byte(operation.Content)
		changes[path] = &content
		return &PlanChange{Action: "add", Path: operation.Path}, nil, nil
	case "replace-if-unmodified", "replace-if-hash-matches", "render-template":
		if !exists {
			return nil, nil, fmt.Errorf("replace conflicts because %s does not exist", operation.Path)
		}
		if operation.Type == "replace-if-unmodified" && operation.OldContent != string(current) {
			return nil, nil, fmt.Errorf("replace conflicts because %s was modified", operation.Path)
		}
		if operation.Type == "replace-if-hash-matches" && !strings.EqualFold(operation.SHA256, FetchSHA256(current)) {
			return nil, nil, fmt.Errorf("replace conflicts because %s hash does not match", operation.Path)
		}
		content := []byte(operation.Content)
		if operation.Type == "render-template" {
			content = []byte(RenderMigrationTemplate(operation.Content, project))
		}
		changes[path] = &content
		return &PlanChange{Action: "replace", Path: operation.Path}, nil, nil
	case "delete-if-unmodified":
		if !exists || operation.OldContent != string(current) {
			return nil, nil, fmt.Errorf("delete conflicts because %s was modified or is absent", operation.Path)
		}
		changes[path] = nil
		return &PlanChange{Action: "delete", Path: operation.Path}, nil, nil
	case "rename-if-hash-matches":
		if operation.FromPath == "" {
			return nil, nil, fmt.Errorf("rename source path is required")
		}
		if err := ValidateRelativePath(operation.FromPath); err != nil {
			return nil, nil, err
		}
		fromPath := filepath.FromSlash(operation.FromPath)
		fromData, fromExists, err := FetchPlannedFile(dir, changes, fromPath)
		if err != nil {
			return nil, nil, err
		}
		if !fromExists || !strings.EqualFold(operation.SHA256, FetchSHA256(fromData)) || exists {
			return nil, nil, fmt.Errorf("rename conflicts for %s -> %s", operation.FromPath, operation.Path)
		}
		changes[fromPath] = nil
		content := append([]byte(nil), fromData...)
		changes[path] = &content
		return &PlanChange{Action: "rename", Path: operation.Path, From: operation.FromPath}, nil, nil
	case "apply-exact-patch":
		return nil, nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("apply-exact-patch is not supported by this CLI build")}
	default:
		return nil, nil, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported migration operation %q", operation.Type)}
	}
}

// FetchPlannedFile 获取迁移计划中最新的文件状态。
func FetchPlannedFile(dir string, changes map[string]*[]byte, path string) ([]byte, bool, error) {
	if value, ok := changes[path]; ok {
		if value == nil {
			return nil, false, nil
		}
		return append([]byte(nil), (*value)...), true, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, path))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read migration file %s: %w", path, err)
	}
	return data, true, nil
}

// ApplyChanges 将已通过冲突检查的计划写入，并在失败时恢复原始文件。
func ApplyChanges(dir string, changes map[string]*[]byte, manifest Manifest) error {
	type originalState struct {
		data   []byte
		mode   os.FileMode
		exists bool
	}
	original := make(map[string]originalState, len(changes)+1)
	manifestData, err := marshalManifest(manifest)
	if err != nil {
		return err
	}
	changes[manifestFile] = &manifestData
	for path := range changes {
		data, readErr := os.ReadFile(filepath.Join(dir, path))
		info, statErr := os.Stat(filepath.Join(dir, path))
		if readErr != nil && !os.IsNotExist(readErr) {
			return fmt.Errorf("backup migration file %s: %w", path, readErr)
		}
		original[path] = originalState{data: data, mode: infoMode(info), exists: statErr == nil}
	}
	restore := func() {
		for path, state := range original {
			fullPath := filepath.Join(dir, path)
			if !state.exists {
				_ = os.Remove(fullPath)
				continue
			}
			_ = os.MkdirAll(filepath.Dir(fullPath), 0o755)
			_ = os.WriteFile(fullPath, state.data, state.mode)
		}
	}
	for path, value := range changes {
		fullPath := filepath.Join(dir, path)
		var writeErr error
		if value == nil {
			writeErr = os.Remove(fullPath)
			if os.IsNotExist(writeErr) {
				writeErr = nil
			}
		} else {
			writeErr = os.MkdirAll(filepath.Dir(fullPath), 0o755)
			if writeErr == nil {
				writeErr = os.WriteFile(fullPath, *value, 0o644)
			}
		}
		if writeErr != nil {
			restore()
			return fmt.Errorf("apply migration file %s: %w", path, writeErr)
		}
	}
	return nil
}

// CompareGeneratedFiles 核对渲染基线和业务项目的所有受管理文件。
func CompareGeneratedFiles(projectDir, generatedDir string) error {
	return CompareOwnedGeneratedFiles(projectDir, generatedDir, nil)
}

// CompareOwnedGeneratedFiles 只核对 profile 声明为 scaffold-owned 的渲染基线文件。
func CompareOwnedGeneratedFiles(projectDir, generatedDir string, profile *Profile) error {
	var mismatch string
	err := filepath.Walk(generatedDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(generatedDir, path)
		if err != nil || rel == "." || rel == manifestFile || info.IsDir() {
			return nil
		}
		if profile != nil {
			owner, declared := FetchPathOwner(profile, filepath.ToSlash(rel))
			if !declared || owner != "scaffold" {
				return nil
			}
		}
		generated, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join(projectDir, rel))
		if err != nil || FetchSHA256(actual) != FetchSHA256(generated) {
			mismatch = filepath.ToSlash(rel)
			return fmt.Errorf("managed file mismatch: %s", mismatch)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// ValidateCleanWorktree 确保正式升级只在独立且干净的 Git 根目录执行。
func ValidateCleanWorktree(ctx context.Context, dir string) error {
	root, err := runGitInDir(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("project is not a Git repository: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil || filepath.Clean(root) != filepath.Clean(absDir) {
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		resolvedDir, dirErr := filepath.EvalSymlinks(absDir)
		if rootErr != nil || dirErr != nil || filepath.Clean(resolvedRoot) != filepath.Clean(resolvedDir) {
			return &ExitError{Code: ExitConflict, Err: fmt.Errorf("formal upgrade requires project root to be the Git worktree root")}
		}
	}
	status, err := runGitInDir(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("working tree is not clean")}
	}
	return nil
}

// runGitInDir 执行目录限定的 Git 命令并返回标准输出。
func runGitInDir(ctx context.Context, dir string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := runOutput(ctx, "git", commandArgs...)
	return strings.TrimSpace(output), err
}

// runOutput 执行不经过 shell 的命令并返回合并输出。
func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// marshalManifest 在原子升级计划中序列化项目清单。
func marshalManifest(manifest Manifest) ([]byte, error) {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", manifestFile, err)
	}
	return data, nil
}

// RenderMigrationTemplate 渲染迁移允许的三个项目字段，不执行模板代码。
func RenderMigrationTemplate(content string, project Project) string {
	content = strings.ReplaceAll(content, "{{ .Project.Name }}", project.Name)
	content = strings.ReplaceAll(content, "{{ .Project.Module }}", project.Module)
	return strings.ReplaceAll(content, "{{ .Project.Profile }}", project.Profile)
}

// CompareVersion 按 SemVer 的 major、minor、patch 和 prerelease 顺序比较版本。
func CompareVersion(left, right string) int {
	left = strings.TrimPrefix(left, "v")
	right = strings.TrimPrefix(right, "v")
	leftParts, leftPre := splitVersion(left)
	rightParts, rightPre := splitVersion(right)
	for index := range leftParts {
		if leftParts[index] != rightParts[index] {
			if leftParts[index] < rightParts[index] {
				return -1
			}
			return 1
		}
	}
	if leftPre == rightPre {
		return 0
	}
	if leftPre == "" {
		return 1
	}
	if rightPre == "" {
		return -1
	}
	return strings.Compare(leftPre, rightPre)
}

// splitVersion 分割最多三段数字版本和 prerelease 字符串。
func splitVersion(value string) ([3]int, string) {
	base, pre, _ := strings.Cut(value, "-")
	var result [3]int
	for index, item := range strings.Split(base, ".") {
		if index >= len(result) {
			break
		}
		result[index], _ = strconv.Atoi(item)
	}
	return result, pre
}

// FormatUpgrade 将升级计划转换为稳定文本。
func FormatUpgrade(report UpgradeReport) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s -> %s", report.From, report.To)
	if report.DryRun {
		builder.WriteString(" (dry-run)")
	}
	for _, change := range report.Changes {
		fmt.Fprintf(&builder, "\n%s %s", change.Action, change.Path)
	}
	for _, action := range report.ManualActions {
		fmt.Fprintf(&builder, "\nmanual %s: %s", action.ID, action.Description)
	}
	return builder.String()
}

// infoMode 返回原始文件模式，缺失时使用普通文件权限。
func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0o644
	}
	return info.Mode().Perm()
}

// asExit 判断错误是否为稳定退出错误。
func asExit(err error) *ExitError {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr
	}
	return nil
}
