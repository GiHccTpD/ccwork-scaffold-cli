package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// App 保存命令的共享依赖和输出目标。
type App struct {
	Options Options
	Stdout  io.Writer
	Stderr  io.Writer
}

// NewCommand 构造完整的 ccwork-scaffold 命令树。
func NewCommand(stdout, stderr io.Writer) *cobra.Command {
	app := &App{Stdout: stdout, Stderr: stderr}
	root := &cobra.Command{
		Use:           "ccwork-scaffold",
		Short:         "ccwork Go HTTP scaffold generator",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&app.Options.Repository, "repository", defaultRepository, "scaffold Git repository path or URL")
	root.PersistentFlags().StringVar(&app.Options.CacheDir, "cache-dir", "", "scaffold cache directory")
	root.PersistentFlags().BoolVar(&app.Options.Offline, "offline", false, "use cache only and forbid network")
	root.PersistentFlags().StringVar(&app.Options.Output, "output", "text", "report format: text or json")
	root.PersistentFlags().BoolVar(&app.Options.AllowPrerelease, "allow-prerelease", false, "allow prerelease scaffold versions")

	root.AddCommand(app.newVersionCommand(), app.newListCommand(), app.newCommand(), app.newInspectCommand(), app.newAdoptCommand(), app.newUpgradeCommand())
	return root
}

// newVersionCommand 创建 generator 版本命令。
func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{Use: "version", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		version := FetchGeneratorVersion()
		return a.WriteReport(map[string]string{"generatorVersion": version}, "ccwork-scaffold "+version)
	}}
}

// newListCommand 创建 scaffold 版本目录命令。
func (a *App) newListCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		catalog, err := (GitSource{Options: a.Options}).FetchCatalog(cmd.Context())
		if err != nil {
			return err
		}
		return a.WriteReport(catalog, FormatCatalog(catalog))
	}}
}

// newCommand 创建新项目命令。
func (a *App) newCommand() *cobra.Command {
	var input RenderInput
	var version, dir string
	var noGitInit, verify bool
	command := &cobra.Command{Use: "new", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if input.Profile == "" {
			input.Profile = "minimal"
		}
		if dir == "" {
			dir = input.Name
		}
		if version == "" {
			catalog, err := (GitSource{Options: a.Options}).FetchCatalog(cmd.Context())
			if err != nil {
				return err
			}
			version = catalog.DefaultVersion
		}
		if err := ValidateProject(input.Name, input.Module, input.Profile); err != nil {
			return err
		}
		release, sourceDir, revision, err := a.FetchRelease(cmd.Context(), version)
		if err != nil {
			return err
		}
		if !release.Creatable || release.Status != "active" {
			return &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("scaffold version %s is not creatable", release.Version)}
		}
		return a.CreateProject(cmd.Context(), sourceDir, release.Version, revision, dir, input, noGitInit, verify)
	}}
	command.Flags().StringVar(&input.Name, "name", "", "project name")
	command.Flags().StringVar(&input.Module, "module", "", "Go module path")
	command.Flags().StringVar(&input.Profile, "profile", "minimal", "scaffold profile: minimal or example")
	command.Flags().StringVar(&version, "scaffold-version", "", "scaffold version")
	command.Flags().StringVar(&dir, "dir", "", "target directory, defaults to name")
	command.Flags().BoolVar(&noGitInit, "no-git-init", false, "do not initialize a Git repository")
	command.Flags().BoolVar(&verify, "verify", false, "run go test, go build and git diff --check")
	return command
}

// newInspectCommand 创建只读项目识别命令。
func (a *App) newInspectCommand() *cobra.Command {
	var dir string
	command := &cobra.Command{Use: "inspect", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			dir, _ = os.Getwd()
		}
		manifest, err := FetchManifest(dir)
		if err != nil {
			return err
		}
		report := map[string]any{"project": manifest.Project, "scaffold": manifest.Scaffold, "generator": manifest.Generator, "pendingUpgrade": manifest.PendingUpgrade}
		if manifest.PendingUpgrade == nil {
			if catalog, catalogErr := a.FetchManifestCatalog(cmd.Context(), manifest); catalogErr == nil {
				report["availableTargets"] = AvailableTargets(catalog, manifest.Scaffold.Version)
			}
		}
		return a.WriteReport(report, FormatInspect(manifest))
	}}
	command.Flags().StringVar(&dir, "dir", "", "project directory, defaults to current directory")
	return command
}

// newAdoptCommand 创建已有项目接管命令。
func (a *App) newAdoptCommand() *cobra.Command {
	var dir, from, profile, name, module string
	var recordOnly bool
	command := &cobra.Command{Use: "adopt", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if from == "" {
			return fmt.Errorf("--from is required; source version is never guessed")
		}
		if module == "" {
			var err error
			module, err = FetchModuleFromGoMod(dir)
			if err != nil {
				return err
			}
		}
		if name == "" {
			var err error
			name, err = FetchProjectName(dir)
			if err != nil {
				return err
			}
		}
		if profile == "" {
			profile = "minimal"
		}
		if err := ValidateProject(name, module, profile); err != nil {
			return err
		}
		_, _, revision, err := a.FetchRelease(cmd.Context(), from)
		if err != nil {
			return err
		}
		manifest := NewManifest(name, module, profile, a.Options.Repository, from, revision, FetchGeneratorVersion(), !recordOnly)
		if recordOnly {
			return WriteManifest(dir, manifest)
		}
		return a.VerifyAndAdopt(cmd.Context(), dir, manifest)
	}}
	command.Flags().StringVar(&dir, "dir", "", "project directory, defaults to current directory")
	command.Flags().StringVar(&from, "from", "", "existing scaffold version")
	command.Flags().StringVar(&profile, "profile", "minimal", "project profile")
	command.Flags().StringVar(&name, "name", "", "project name override")
	command.Flags().StringVar(&module, "module", "", "Go module override")
	command.Flags().BoolVar(&recordOnly, "record-only", false, "record unverified identity without checking files")
	return command
}

// newUpgradeCommand 创建声明式升级命令。
func (a *App) newUpgradeCommand() *cobra.Command {
	var dir, target, ack string
	var latest, dryRun, continueUpgrade bool
	command := &cobra.Command{Use: "upgrade", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if continueUpgrade {
			if target != "" || latest || ack == "" {
				return fmt.Errorf("--continue requires --ack and cannot combine with --to or --latest")
			}
			return a.ContinueUpgrade(cmd.Context(), dir, ack)
		}
		if (target == "") == !latest {
			return fmt.Errorf("exactly one of --to or --latest is required")
		}
		return a.Upgrade(cmd.Context(), dir, target, latest, dryRun)
	}}
	command.Flags().StringVar(&dir, "dir", "", "project directory, defaults to current directory")
	command.Flags().StringVar(&target, "to", "", "target scaffold version")
	command.Flags().BoolVar(&latest, "latest", false, "use catalog defaultVersion")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show changes without writing")
	command.Flags().BoolVar(&continueUpgrade, "continue", false, "continue a pending upgrade")
	command.Flags().StringVar(&ack, "ack", "", "acknowledge a pending manual action")
	return command
}

// FetchRelease 获取版本定义并解压对应的不可变源码。
func (a *App) FetchRelease(ctx context.Context, version string) (Release, string, string, error) {
	source := GitSource{Options: a.Options}
	catalog, err := source.FetchCatalog(ctx)
	if err != nil {
		return Release{}, "", "", err
	}
	release, err := FetchRelease(catalog, version)
	if err != nil {
		return Release{}, "", "", err
	}
	if release.MinGeneratorVersion != "" && CompareVersion(FetchGeneratorVersion(), release.MinGeneratorVersion) < 0 {
		return Release{}, "", "", &ExitError{Code: ExitIncompatible, Err: fmt.Errorf("scaffold %s requires generator %s", version, release.MinGeneratorVersion)}
	}
	if !a.Options.AllowPrerelease && strings.Contains(version, "-") {
		return Release{}, "", "", &ExitError{Code: ExitUnsupported, Err: fmt.Errorf("prerelease scaffold %s requires --allow-prerelease", version)}
	}
	dir, revision, err := source.FetchReleaseSource(ctx, release)
	return release, dir, revision, err
}

// FetchManifestCatalog 使用清单中的来源读取版本目录，避免本地仓库项目回退到默认远端。
func (a *App) FetchManifestCatalog(ctx context.Context, manifest Manifest) (ReleaseCatalog, error) {
	options := a.Options
	if options.Repository == defaultRepository {
		options.Repository = manifest.Scaffold.Repository
	}
	return (GitSource{Options: options}).FetchCatalog(ctx)
}

// FetchManifestSource 创建以项目清单来源为优先的 Git source。
func (a *App) FetchManifestSource(manifest Manifest) GitSource {
	options := a.Options
	if options.Repository == defaultRepository {
		options.Repository = manifest.Scaffold.Repository
	}
	return GitSource{Options: options}
}

// CreateProject 在临时目录中渲染、校验并原子落盘新项目。
func (a *App) CreateProject(ctx context.Context, sourceDir, version, revision, target string, input RenderInput, noGitInit, verify bool) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target directory: %w", err)
	}
	if err := ValidateEmptyTarget(target); err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create target parent: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, ".ccwork-scaffold-")
	if err != nil {
		return fmt.Errorf("create generation staging directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := RenderProject(sourceDir, tmpDir, input); err != nil {
		return err
	}
	manifest := NewManifest(input.Name, input.Module, input.Profile, a.Options.Repository, version, revision, FetchGeneratorVersion(), true)
	if err := WriteManifest(tmpDir, manifest); err != nil {
		return err
	}
	if verify || !noGitInit {
		if err := runCommand(ctx, "git", "-C", tmpDir, "init"); err != nil {
			return fmt.Errorf("initialize generated Git repository: %w", err)
		}
	}
	if verify {
		if err := VerifyGeneratedProject(ctx, tmpDir); err != nil {
			return err
		}
	}
	if noGitInit {
		if err := os.RemoveAll(filepath.Join(tmpDir, ".git")); err != nil {
			return fmt.Errorf("remove temporary Git metadata: %w", err)
		}
	}
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("replace empty target directory: %w", err)
		}
	}
	if err := os.Rename(tmpDir, target); err != nil {
		return fmt.Errorf("publish generated project: %w", err)
	}
	fmt.Fprintf(a.Stdout, "created %s from %s\n", target, revision)
	return nil
}

// ValidateEmptyTarget 确保生成不会覆盖非空目录或文件。
func ValidateEmptyTarget(target string) error {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect target directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("target %s is not a directory", target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("read target directory: %w", err)
	}
	if len(entries) > 0 {
		return &ExitError{Code: ExitConflict, Err: fmt.Errorf("target directory %s is not empty", target)}
	}
	return nil
}

// VerifyGeneratedProject 执行 new --verify 约定的固定检查。
func VerifyGeneratedProject(ctx context.Context, dir string) error {
	for _, args := range [][]string{{"test", "./..."}, {"build", "./..."}, {"diff", "--check"}} {
		if err := runCommandInDir(ctx, dir, args[0], args[1:]...); err != nil {
			return fmt.Errorf("verify generated project with git/go: %w", err)
		}
	}
	return nil
}

// runCommandInDir 执行不经过 shell 的目录限定命令。
func runCommandInDir(ctx context.Context, dir, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return nil
}

// NewManifest 创建新项目的默认来源清单。
func NewManifest(name, module, profile, repository, version, revision, generator string, verified bool) Manifest {
	if info, err := os.Stat(repository); err == nil && info.IsDir() {
		if absolute, absErr := filepath.Abs(repository); absErr == nil {
			repository = absolute
		}
	}
	verification := "unverified"
	if verified {
		verification = "verified"
	}
	return Manifest{SchemaVersion: manifestSchemaVersion, Project: Project{Name: name, Module: module, Profile: profile}, Scaffold: Scaffold{Repository: repository, Version: version, Revision: revision, Verification: verification, Upgradeable: verified}, Generator: Generator{CreatedWith: generator, LastUpgradedWith: generator}}
}

// WriteReport 按用户选择输出文本或 JSON 报告。
func (a *App) WriteReport(value any, text string) error {
	if a.Options.Output == "json" {
		return json.NewEncoder(a.Stdout).Encode(value)
	}
	if a.Options.Output != "text" {
		return fmt.Errorf("unsupported output format %q", a.Options.Output)
	}
	_, err := fmt.Fprintln(a.Stdout, text)
	return err
}

// FormatCatalog 将版本目录转换为稳定的文本报告。
func FormatCatalog(catalog ReleaseCatalog) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "default: %s\n", catalog.DefaultVersion)
	for _, release := range catalog.Releases {
		fmt.Fprintf(&builder, "%s\tstatus=%s\tcreatable=%t\tupgrade-source=%t\tmin-generator=%s\n", release.Version, release.Status, release.Creatable, release.UpgradeSource, release.MinGeneratorVersion)
	}
	for _, legacy := range catalog.Legacy {
		fmt.Fprintf(&builder, "%s\tstatus=legacy\treason=%s\n", legacy.Version, legacy.Reason)
	}
	return strings.TrimRight(builder.String(), "\n")
}

// FormatInspect 将项目清单转换为不包含绝对路径的文本报告。
func FormatInspect(manifest Manifest) string {
	return fmt.Sprintf("project=%s\nmodule=%s\nprofile=%s\nscaffold=%s\nrevision=%s\nverification=%s\nupgradeable=%t", manifest.Project.Name, manifest.Project.Module, manifest.Project.Profile, manifest.Scaffold.Version, manifest.Scaffold.Revision, manifest.Scaffold.Verification, manifest.Scaffold.Upgradeable)
}

// AvailableTargets 返回版本目录中可作为升级目标的版本。
func AvailableTargets(catalog ReleaseCatalog, current string) []string {
	result := make([]string, 0)
	for _, release := range catalog.Releases {
		if release.Status == "active" && release.Version != current && CompareVersion(release.Version, current) > 0 {
			result = append(result, release.Version)
		}
	}
	return result
}
