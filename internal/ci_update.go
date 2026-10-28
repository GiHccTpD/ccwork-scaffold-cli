package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"
)

// UpdateCI 核验旧来源和全部文件后升级 CI；项目配置不属于共享安装清单。
func (a *App) UpdateCI(ctx context.Context, repository, dir, version string, dryRun bool) (*CIReport, error) {
	// 🔍1.核验安装记录的真实来源，拒绝通过篡改 hash 接管定制文件
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve CI update root: %w", err)
	}
	current, err := FetchCIInstallation(dir)
	if err != nil {
		return nil, fmt.Errorf("read CI update source: %w", err)
	}
	if current == nil {
		return nil, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("CI update requires an existing installation; use ci init first")}
	}
	if repository == "" {
		repository = current.Repository
	}
	repository, err = NormalizeCIRepository(repository)
	if err != nil {
		return nil, fmt.Errorf("normalize CI update repository: %w", err)
	}
	if repository != current.Repository {
		return nil, &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI update cannot switch repositories")}
	}
	old, _, err := a.FetchCIRelease(ctx, repository, current.Version, true)
	if err != nil {
		return nil, fmt.Errorf("verify previous CI source: %w", err)
	}
	oldJSON, _ := json.Marshal(old)
	currentJSON, _ := json.Marshal(current)
	if !bytes.Equal(oldJSON, currentJSON) {
		return nil, &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI installation differs from its authoritative source")}
	}

	// 📋2.完整预检旧资产及目标文件，冲突时不产生任何项目写入
	next, sourceDir, err := a.FetchCIRelease(ctx, repository, version, false)
	if err != nil {
		return nil, fmt.Errorf("resolve target CI source: %w", err)
	}
	report := &CIReport{Version: version, Revision: next.Revision, Status: "planned", Changes: []CIChange{}}
	if err := ValidateCIProject(ctx, dir, next.Bundle); err != nil {
		return report, fmt.Errorf("validate CI update project: %w", err)
	}
	if inspected, err := InspectCI(dir); err != nil || inspected.Status != "complete" {
		if inspected != nil {
			report.Changes = inspected.Changes
		}
		return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI update requires unchanged installed files; merge project customizations manually")}
	}
	expected := map[string]CIFile{}
	for _, file := range old.Bundle.Files {
		expected[file.Path] = file
	}
	payload, modes := map[string][]byte{}, map[string]os.FileMode{}
	nextPaths := map[string]bool{}
	for _, file := range next.Bundle.Files {
		nextPaths[file.Path] = true
		name, err := ResolveCIPath(sourceDir, file.Path)
		if err != nil {
			return report, fmt.Errorf("resolve target CI payload: %w", err)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return report, fmt.Errorf("read target CI payload: %w", err)
		}
		if FetchSHA256(data) != file.SHA256 {
			return report, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("target CI checksum mismatch: %s", file.Path)}
		}
		target, err := ResolveCIPath(dir, file.Path)
		if err != nil {
			return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("resolve CI update destination: %w", err)}
		}
		status := "add"
		if previous, ok := expected[file.Path]; ok {
			status = "update"
			if previous == file {
				status = "same"
			}
		} else if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("new CI path already exists: %s", file.Path)}
		}
		report.Changes = append(report.Changes, CIChange{Path: file.Path, Status: status})
		if status != "same" {
			payload[file.Path] = data
			mode, _ := strconv.ParseUint(file.Mode, 8, 32)
			modes[file.Path] = os.FileMode(mode)
		}
	}
	for _, file := range old.Bundle.Files {
		if !nextPaths[file.Path] {
			return report, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("target removes %s; automatic deletion is not supported", file.Path)}
		}
	}
	manifest, err := yaml.Marshal(next)
	if err != nil {
		return report, fmt.Errorf("encode updated CI installation: %w", err)
	}
	manifestPath, err := ResolveCIPath(dir, ciInstallationPath)
	if err != nil {
		return report, fmt.Errorf("resolve CI installation destination: %w", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		return report, fmt.Errorf("snapshot CI installation: %w", err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		return report, fmt.Errorf("stat CI installation: %w", err)
	}
	expected[ciInstallationPath] = CIFile{Path: ciInstallationPath, SHA256: FetchSHA256(before), Mode: fmt.Sprintf("%04o", info.Mode().Perm())}
	nextJSON, _ := json.Marshal(next)
	if !bytes.Equal(oldJSON, nextJSON) {
		payload[ciInstallationPath], modes[ciInstallationPath] = manifest, 0644
		report.Changes = append(report.Changes, CIChange{Path: ciInstallationPath, Status: "update"})
	}
	if dryRun {
		return report, nil
	}

	// 💾3.只在干净工作树写入，并按事务恢复普通失败
	if err := ValidateCleanWorktree(ctx, dir); err != nil {
		return report, fmt.Errorf("validate CI update worktree: %w", err)
	}
	lockPath := filepath.Join(dir, ".gitlab", ".ci-update.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("acquire CI update lock (inspect stale locks manually): %w", err)}
	}
	defer os.Remove(lockPath)
	if err := lock.Close(); err != nil {
		return report, fmt.Errorf("close CI update lock: %w", err)
	}
	if inspected, err := InspectCI(dir); err != nil || inspected.Status != "complete" {
		return report, &ExitError{Code: ExitConflict, Err: fmt.Errorf("CI files changed while acquiring update lock")}
	}
	if err := ApplyCIUpdate(dir, payload, modes, expected, WriteCIFile); err != nil {
		return report, err
	}
	report.Status = "complete"
	return report, nil
}

// ApplyCIUpdate 原子替换单文件，安装记录最后更新；普通失败仅恢复仍由本次操作拥有的内容。
func ApplyCIUpdate(dir string, payload map[string][]byte, modes map[string]os.FileMode, expected map[string]CIFile, write func(*os.File, []byte) error) (result error) {
	// 📋1.对整个写集合重新预检，保留回滚快照
	type snapshot struct {
		data   []byte
		mode   os.FileMode
		exists bool
	}
	before := map[string]snapshot{}
	var names []string
	for name := range payload {
		if name != ciInstallationPath {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := payload[ciInstallationPath]; ok {
		names = append(names, ciInstallationPath)
	}
	for _, name := range names {
		full, err := ResolveCIPath(dir, name)
		if err != nil {
			return fmt.Errorf("revalidate CI update path: %w", err)
		}
		previous, exists := expected[name]
		data, err := os.ReadFile(full)
		if !exists {
			if !os.IsNotExist(err) {
				return fmt.Errorf("CI destination appeared after preview: %s", name)
			}
			before[name] = snapshot{}
			continue
		}
		if err != nil {
			return fmt.Errorf("read previous CI asset: %w", err)
		}
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Errorf("stat previous CI asset: %w", err)
		}
		mode, _ := strconv.ParseUint(previous.Mode, 8, 32)
		if FetchSHA256(data) != previous.SHA256 || (os.PathSeparator != '\\' && info.Mode().Perm() != os.FileMode(mode)) {
			return fmt.Errorf("CI asset changed after preview: %s", name)
		}
		before[name] = snapshot{data, info.Mode().Perm(), true}
	}

	// 🛟2.失败时恢复已写文件，保留并报告并发出现的非本次内容
	var applied, createdDirs []string
	defer func() {
		if result == nil {
			return
		}
		for i := len(applied) - 1; i >= 0; i-- {
			name := applied[i]
			full, err := ResolveCIPath(dir, name)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("resolve rollback path: %w", err))
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil || !bytes.Equal(data, payload[name]) {
				result = errors.Join(result, fmt.Errorf("rollback conflict; preserve concurrent change: %s", name))
				continue
			}
			old := before[name]
			if old.exists {
				err = ReplaceCIFile(full, old.data, old.mode, WriteCIFile)
			} else {
				err = os.Remove(full)
			}
			if err != nil {
				result = errors.Join(result, fmt.Errorf("restore CI asset %s: %w", name, err))
			}
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			if err := os.Remove(createdDirs[i]); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, fmt.Errorf("restore CI directory: %w", err))
			}
		}
	}()

	// 💾3.顺序写入，每次替换前再次核对内容与路径
	for _, name := range names {
		full, err := ResolveCIPath(dir, name)
		if err != nil {
			return fmt.Errorf("resolve CI write path: %w", err)
		}
		var missing []string
		for parent := filepath.Dir(full); parent != dir; parent = filepath.Dir(parent) {
			if _, err := os.Lstat(parent); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect CI update parent: %w", err)
			}
			missing = append(missing, parent)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if err := os.Mkdir(missing[i], 0755); err != nil {
				return fmt.Errorf("create CI update directory: %w", err)
			}
			createdDirs = append(createdDirs, missing[i])
		}
		old := before[name]
		if old.exists {
			data, err := os.ReadFile(full)
			if err != nil || !bytes.Equal(data, old.data) {
				return fmt.Errorf("CI replacement changed after preflight: %s", name)
			}
			if err := ReplaceCIFile(full, payload[name], modes[name], write); err != nil {
				return fmt.Errorf("replace CI asset %s: %w", name, err)
			}
		} else {
			if err := ApplyCIChanges(dir, map[string][]byte{name: payload[name]}, map[string]os.FileMode{name: modes[name]}, write); err != nil {
				return fmt.Errorf("add CI asset %s: %w", name, err)
			}
		}
		applied = append(applied, name)
	}
	return nil
}

// ReplaceCIFile 在同目录临时文件完整写入后替换目标，不暴露部分内容。
func ReplaceCIFile(name string, data []byte, mode os.FileMode, write func(*os.File, []byte) error) error {
	file, err := os.CreateTemp(filepath.Dir(name), ".ci-replace-*")
	if err != nil {
		return fmt.Errorf("create CI replacement: %w", err)
	}
	defer os.Remove(file.Name())
	writeErr := write(file, data)
	modeErr := file.Chmod(mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("prepare CI replacement: %w", err)
	}
	if err := os.Rename(file.Name(), name); err != nil {
		return fmt.Errorf("commit CI replacement: %w", err)
	}
	return nil
}
