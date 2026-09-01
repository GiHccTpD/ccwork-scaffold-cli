package internal

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var catalogPaths = []string{"releases.yaml", ".scaffold/releases.yaml", "release/releases.yaml"}

// GitSource 管理一个可由本地路径或无凭据 URL 得到的 Git 仓库。
type GitSource struct {
	Options Options
}

// FetchCatalog 从仓库当前 HEAD 读取版本目录。
func (s GitSource) FetchCatalog(ctx context.Context) (ReleaseCatalog, error) {
	repo, err := s.FetchRepository(ctx)
	if err != nil {
		return ReleaseCatalog{}, err
	}
	var lastErr error
	for _, path := range catalogPaths {
		data, err := s.FetchGit(ctx, repo, "show", "HEAD:"+path)
		if err == nil {
			var catalog ReleaseCatalog
			if err := yamlUnmarshal(data, &catalog); err != nil {
				return ReleaseCatalog{}, fmt.Errorf("parse release catalog %s: %w", path, err)
			}
			if err := ValidateCatalog(catalog); err != nil {
				return ReleaseCatalog{}, err
			}
			return catalog, nil
		}
		lastErr = err
	}
	return ReleaseCatalog{}, fmt.Errorf("release catalog not found in repository: %w", lastErr)
}

// ValidateCatalog 校验版本目录 schema、defaultVersion 和 release 唯一性。
func ValidateCatalog(catalog ReleaseCatalog) error {
	if catalog.SchemaVersion != releaseSchemaVersion {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("unsupported release catalog schema version %d", catalog.SchemaVersion)}
	}
	if catalog.DefaultVersion == "" {
		return fmt.Errorf("release catalog defaultVersion is required")
	}
	seen := make(map[string]struct{}, len(catalog.Releases))
	for _, release := range catalog.Releases {
		if err := ValidateVersion(release.Version); err != nil || release.Revision == "" || release.Status == "" {
			if err != nil {
				return err
			}
			return fmt.Errorf("release catalog contains incomplete release")
		}
		if release.Status != "active" && release.Status != "deprecated" && release.Status != "revoked" {
			return fmt.Errorf("release %s has unsupported status %q", release.Version, release.Status)
		}
		if _, ok := seen[release.Version]; ok {
			return fmt.Errorf("release catalog contains duplicate version %q", release.Version)
		}
		seen[release.Version] = struct{}{}
	}
	if _, ok := seen[catalog.DefaultVersion]; !ok {
		return fmt.Errorf("release catalog defaultVersion %q is not listed", catalog.DefaultVersion)
	}
	return nil
}

// FetchRelease 返回 catalog 中的精确版本定义。
func FetchRelease(catalog ReleaseCatalog, version string) (Release, error) {
	if err := ValidateVersion(version); err != nil {
		return Release{}, &ExitError{Code: ExitUnsupported, Err: err}
	}
	for _, release := range catalog.Releases {
		if release.Version == version {
			return release, nil
		}
	}
	return Release{}, &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("scaffold version %q is not supported", version)}
}

// FetchReleaseSource 校验 tag 与 revision 后把源码解压到不可变缓存目录。
func (s GitSource) FetchReleaseSource(ctx context.Context, release Release) (string, string, error) {
	repo, err := s.FetchRepository(ctx)
	if err != nil {
		return "", "", err
	}
	revisionBytes, err := s.FetchGit(ctx, repo, "rev-parse", "refs/tags/"+release.Version+"^{commit}")
	if err != nil {
		return "", "", &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("resolve scaffold tag %s: %w", release.Version, err)}
	}
	revision := strings.TrimSpace(string(revisionBytes))
	if release.Revision != "" && !strings.HasPrefix(release.Revision, "<") && revision != release.Revision {
		return "", "", &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("tag %s revision mismatch: catalog=%s actual=%s", release.Version, release.Revision, revision)}
	}
	if err := s.ValidateAnnotatedTag(ctx, repo, release.Version); err != nil {
		return "", "", err
	}

	cacheDir, err := s.FetchCacheDir()
	if err != nil {
		return "", "", err
	}
	sourceDir := filepath.Join(cacheDir, "sources", revision)
	if info, err := os.Stat(sourceDir); err == nil && info.IsDir() {
		return sourceDir, revision, nil
	}
	if s.Options.Offline {
		return "", "", fmt.Errorf("offline cache miss for scaffold revision %s", revision)
	}
	sourceCacheDir := filepath.Join(cacheDir, "sources")
	if err := os.MkdirAll(sourceCacheDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create source cache directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(sourceCacheDir, ".staging-")
	if err != nil {
		return "", "", fmt.Errorf("create source cache staging directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	archiveData, err := s.FetchGit(ctx, repo, "archive", "--format=tar", release.Version)
	if err != nil {
		return "", "", fmt.Errorf("archive scaffold %s: %w", release.Version, err)
	}
	if err := ExtractTar(archiveData, tmpDir); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpDir, sourceDir); err != nil {
		if !os.IsExist(err) {
			return "", "", fmt.Errorf("store source cache: %w", err)
		}
	}
	return sourceDir, revision, nil
}

// FetchMigration 从目标 scaffold tag 读取对应的声明式迁移文件。
func (s GitSource) FetchMigration(ctx context.Context, from, to string, release Release) (Migration, error) {
	repo, err := s.FetchRepository(ctx)
	if err != nil {
		return Migration{}, err
	}
	paths := []string{
		fmt.Sprintf(".scaffold/migrations/%s-to-%s.yaml", from, to),
		fmt.Sprintf("migrations/%s-to-%s.yaml", from, to),
	}
	if release.MigrationPath != "" {
		paths = append([]string{release.MigrationPath}, paths...)
	}
	var lastErr error
	for _, path := range paths {
		data, fetchErr := s.FetchGit(ctx, repo, "show", release.Version+":"+path)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		var migration Migration
		if err := yaml.Unmarshal(data, &migration); err != nil {
			return Migration{}, fmt.Errorf("parse migration %s: %w", path, err)
		}
		return migration, nil
	}
	return Migration{}, &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("migration %s -> %s not found: %w", from, to, lastErr)}
}

// ValidateAnnotatedTag 确保正式 scaffold 版本使用不可变 annotated tag。
func (s GitSource) ValidateAnnotatedTag(ctx context.Context, repo, version string) error {
	tagType, err := s.FetchGit(ctx, repo, "cat-file", "-t", "refs/tags/"+version)
	if err != nil {
		return &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("inspect scaffold tag %s: %w", version, err)}
	}
	if strings.TrimSpace(string(tagType)) != "tag" {
		return &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("scaffold tag %s is not annotated", version)}
	}
	return nil
}

// FetchRepository 返回可供 Git 命令使用的本地仓库路径。
func (s GitSource) FetchRepository(ctx context.Context) (string, error) {
	repository := s.Options.Repository
	if repository == "" {
		repository = defaultRepository
	}
	if err := ValidateRepository(repository); err != nil {
		return "", err
	}
	if info, err := os.Stat(repository); err == nil && info.IsDir() {
		return filepath.Abs(repository)
	}
	if s.Options.Offline {
		return "", fmt.Errorf("offline mode does not permit cloning repository %s", repository)
	}
	cacheDir, err := s.FetchCacheDir()
	if err != nil {
		return "", err
	}
	cloneDir := filepath.Join(cacheDir, "repositories", FetchSHA256([]byte(repository)))
	if info, err := os.Stat(filepath.Join(cloneDir, ".git")); err == nil && info.IsDir() {
		return cloneDir, nil
	}
	if err := os.MkdirAll(filepath.Dir(cloneDir), 0o755); err != nil {
		return "", fmt.Errorf("create repository cache: %w", err)
	}
	if err := runCommand(ctx, "git", "clone", "--filter=blob:none", "--no-checkout", repository, cloneDir); err != nil {
		return "", fmt.Errorf("clone scaffold repository: %w", err)
	}
	return cloneDir, nil
}

// FetchCacheDir 返回显式配置或系统缓存目录，不保存认证信息。
func (s GitSource) FetchCacheDir() (string, error) {
	if s.Options.CacheDir != "" {
		if err := os.MkdirAll(s.Options.CacheDir, 0o755); err != nil {
			return "", fmt.Errorf("create cache directory: %w", err)
		}
		return s.Options.CacheDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	cacheDir := filepath.Join(base, "ccwork-scaffold")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}
	return cacheDir, nil
}

// FetchGit 执行受控 Git 子命令并返回标准输出。
func (s GitSource) FetchGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", repo}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return output, nil
}

// ExtractTar 安全解压 git archive，拒绝绝对路径、路径穿越和符号链接。
func ExtractTar(data []byte, target string) error {
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read scaffold archive: %w", err)
		}
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader || header.Typeflag == tar.TypeGNULongName || header.Typeflag == tar.TypeGNULongLink {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("scaffold archive contains unsupported entry %q", header.Name)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("scaffold archive path escapes root: %q", header.Name)
		}
		path := filepath.Join(target, name)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return fmt.Errorf("create archived directory %s: %w", name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create archived parent %s: %w", name, err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("create archived file %s: %w", name, err)
		}
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("extract archived file %s: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close archived file %s: %w", name, closeErr)
		}
	}
}

// runCommand 执行不经过 shell 的系统命令。
func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return nil
}

// yamlUnmarshal 延迟集中封装 YAML 解析，保持 Git 层错误上下文一致。
func yamlUnmarshal(data []byte, target any) error {
	return yaml.Unmarshal(data, target)
}
