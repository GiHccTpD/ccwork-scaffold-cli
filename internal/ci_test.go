package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCIFixtureFile 建立测试文件及父目录。
func writeCIFixtureFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// runCITestGit 执行不访问外部服务的测试 Git 命令。
func runCITestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := runGitInDir(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// createCIFixture 创建具有独立 CI 发布目录的本地来源和干净老项目。
func createCIFixture(t *testing.T, mutate func(map[string]any)) (string, string, string) {
	t.Helper()
	source := t.TempDir()
	target := t.TempDir()
	cache := t.TempDir()
	payload := []byte("stages: [security]\nscan:\n  stage: security\n  script: echo scan\n")
	bundle := map[string]any{"schemaVersion": 1, "name": "go-security", "minGoVersion": "1.25.0", "toolchainGoVersion": "1.27.3", "files": []map[string]string{{"path": ".gitlab-ci.yml", "sha256": FetchSHA256(payload), "mode": "0644"}}}
	if mutate != nil {
		mutate(bundle)
	}
	data, _ := json.Marshal(bundle)
	writeCIFixtureFile(t, source, "bundles/go-security/.gitlab-ci.yml", payload)
	writeCIFixtureFile(t, source, "bundles/go-security/bundle.yaml", data)
	for _, dir := range []string{source, target} {
		runCITestGit(t, dir, "init", "-b", "main")
		runCITestGit(t, dir, "config", "user.email", "ci-test@example.invalid")
		runCITestGit(t, dir, "config", "user.name", "CI test")
	}
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "bundle")
	runCITestGit(t, source, "tag", "-a", "v0.1.0", "-m", "CI release")
	revision := runCITestGit(t, source, "rev-parse", "HEAD")
	catalog := map[string]any{"schemaVersion": 1, "defaultVersion": "v0.1.0", "releases": []map[string]string{{"version": "v0.1.0", "revision": revision, "status": "active", "minGeneratorVersion": "v0.0.1"}}}
	data, _ = json.Marshal(catalog)
	writeCIFixtureFile(t, source, "ci-releases.yaml", data)
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "catalog")
	writeCIFixtureFile(t, target, "go.mod", []byte("module example.invalid/legacy\n\ngo 1.25.0\n"))
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "legacy")
	return source, target, cache
}

// executeCICommand 通过真实命令树执行 CI 命令并收集输出。
func executeCICommand(args ...string) (string, error) {
	var output bytes.Buffer
	command := NewCommand(&output, &output)
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return output.String(), err
}

// TestCIInstallLifecycle 验证预览零写入、安装、离线查询与重复执行。
func TestCIInstallLifecycle(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	args := []string{"--cache-dir", cache, "--output", "json", "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"}
	if out, err := executeCICommand(append(args, "--dry-run")...); err != nil {
		t.Fatalf("dry-run: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(target, ".gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote payload")
	}
	if out, err := executeCICommand(args...); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	manifest := filepath.Join(target, ".gitlab/ci-installation.yaml")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "install CI")
	if out, err := executeCICommand(append([]string{"--offline"}, args...)...); err != nil {
		t.Fatalf("repeat: %v %s", err, out)
	}
	after, _ := os.ReadFile(manifest)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat changed installation")
	}
	if status := runCITestGit(t, target, "status", "--porcelain"); status != "" {
		t.Fatalf("repeat changed files: %s", status)
	}
	if out, err := executeCICommand("--offline", "ci", "inspect", "--dir", target); err != nil || !strings.Contains(out, "complete") {
		t.Fatalf("inspect: %v %s", err, out)
	}
	writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte("custom pipeline"))
	if out, err := executeCICommand("ci", "inspect", "--dir", target); err == nil || !strings.Contains(out, "modified") {
		t.Fatalf("drift: %v %s", err, out)
	}
}

// TestCIInstallRejectsUnsafeInputs 验证冲突、安全和兼容错误不写入项目。
func TestCIInstallRejectsUnsafeInputs(t *testing.T) {
	cases := []string{"conflict", "dirty", "symlink-parent", "symlink-file", "bad-hash", "escape", "case-collision", "file-parent-collision", "future-schema", "toolchain", "go-version", "missing-module", "wrong-root"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			source, target, cache := createCIFixture(t, func(bundle map[string]any) {
				files := bundle["files"].([]map[string]string)
				switch name {
				case "bad-hash":
					files[0]["sha256"] = strings.Repeat("0", 64)
				case "escape":
					files[0]["path"] = "../escaped"
				case "case-collision":
					bundle["files"] = append(files, map[string]string{"path": ".GITLAB-CI.YML", "sha256": files[0]["sha256"], "mode": "0644"})
				case "file-parent-collision":
					bundle["files"] = append(files, map[string]string{"path": ".gitlab-ci.yml/child", "sha256": files[0]["sha256"], "mode": "0644"})
				case "future-schema":
					bundle["schemaVersion"] = 2
				case "toolchain":
					bundle["toolchainGoVersion"] = "1.24.0"
				}
			})
			switch name {
			case "conflict":
				writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte("custom"))
				runCITestGit(t, target, "add", ".")
				runCITestGit(t, target, "commit", "-m", "custom")
			case "dirty":
				writeCIFixtureFile(t, target, "notes.txt", []byte("unrelated"))
			case "symlink-parent":
				if err := os.Symlink(t.TempDir(), filepath.Join(target, ".gitlab")); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				if err := os.Symlink(filepath.Join(source, "ci-releases.yaml"), filepath.Join(target, ".gitlab-ci.yml")); err != nil {
					t.Fatal(err)
				}
			case "go-version":
				writeCIFixtureFile(t, target, "go.mod", []byte("module example.invalid/legacy\n\ngo 1.99.0\n"))
			case "missing-module":
				if err := os.Remove(filepath.Join(target, "go.mod")); err != nil {
					t.Fatal(err)
				}
			case "wrong-root":
				target = filepath.Join(target, "nested")
				writeCIFixtureFile(t, target, "go.mod", []byte("module example.invalid/nested\ngo 1.25.0\n"))
			}
			if out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"); err == nil {
				t.Fatalf("accepted %s: %s", name, out)
			}
			if _, err := os.Stat(filepath.Join(target, ".gitlab/ci-installation.yaml")); !os.IsNotExist(err) {
				t.Fatal("failure wrote installation")
			}
		})
	}
}

// TestCICommandsVersionAndSource 验证独立版本目录、必要参数及不同版本拒绝。
func TestCICommandsVersionAndSource(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	prefix := []string{"--cache-dir", cache, "ci", "--ci-repository", source}
	if out, err := executeCICommand(append(prefix, "list")...); err != nil || !strings.Contains(out, "v0.1.0") {
		t.Fatalf("list: %v %s", err, out)
	}
	if _, err := executeCICommand(append(prefix, "init", "--dir", target)...); err == nil {
		t.Fatal("missing version accepted")
	}
	if _, err := executeCICommand(append(prefix, "init", "--dir", target, "--ci-version", "v9.0.0")...); err == nil {
		t.Fatal("unknown version accepted")
	}
}

// TestCIWriteRollback 验证中途写入失败时清理本次文件，保护已有内容。
func TestCIWriteRollback(t *testing.T) {
	dir := t.TempDir()
	writeCIFixtureFile(t, dir, "notes.txt", []byte("keep"))
	calls := 0
	err := ApplyCIChanges(dir, map[string][]byte{".gitlab-ci.yml": []byte("scan"), "scripts/security/a.sh": []byte("script")}, map[string]os.FileMode{".gitlab-ci.yml": 0644, "scripts/security/a.sh": 0644}, func(file *os.File, data []byte) error {
		calls++
		if calls == 2 {
			_, _ = file.Write([]byte("partial"))
			return fmt.Errorf("simulated disk error")
		}
		return WriteCIFile(file, data)
	})
	if err == nil {
		t.Fatal("write failure accepted")
	}
	for _, name := range []string{".gitlab-ci.yml", "scripts"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("rollback left %s", name)
		}
	}
	assertFileEquals(t, filepath.Join(dir, "notes.txt"), "keep")
}

// TestCIExclusiveWritePreservesRacingFile 验证写入前出现的文件不会被覆盖或回滚删除。
func TestCIExclusiveWritePreservesRacingFile(t *testing.T) {
	dir := t.TempDir()
	writeCIFixtureFile(t, dir, ".gitlab-ci.yml", []byte("keep"))
	if err := ApplyCIChanges(dir, map[string][]byte{".gitlab-ci.yml": []byte("overwrite")}, map[string]os.FileMode{".gitlab-ci.yml": 0644}, WriteCIFile); err == nil {
		t.Fatal("overwrite accepted")
	}
	assertFileEquals(t, filepath.Join(dir, ".gitlab-ci.yml"), "keep")
}

// TestCIReleaseValidation 验证来源身份、发布状态和安装器兼容门槛。
func TestCIReleaseValidation(t *testing.T) {
	for _, name := range []string{"revoked", "deprecated", "revision-mismatch", "lightweight", "minimum-cli", "bad-catalog-schema", "go-toolchain", "invalid-module", "different-installation", "missing-installed-file", "wrong-mode", "case-alias", "bad-bundle-schema"} {
		t.Run(name, func(t *testing.T) {
			source, target, cache := createCIFixture(t, nil)
			catalogPath := filepath.Join(source, "ci-releases.yaml")
			data, _ := os.ReadFile(catalogPath)
			var catalog map[string]any
			if err := json.Unmarshal(data, &catalog); err != nil {
				t.Fatal(err)
			}
			release := catalog["releases"].([]any)[0].(map[string]any)
			switch name {
			case "revoked", "deprecated":
				release["status"] = name
			case "revision-mismatch":
				release["revision"] = strings.Repeat("0", 40)
			case "minimum-cli":
				release["minGeneratorVersion"] = "v99.0.0"
			case "bad-catalog-schema":
				catalog["schemaVersion"] = 2
			case "lightweight":
				runCITestGit(t, source, "tag", "-d", "v0.1.0")
				runCITestGit(t, source, "tag", "v0.1.0")
			case "go-toolchain":
				writeCIFixtureFile(t, target, "go.mod", []byte("module example.invalid/legacy\ngo 1.25.0\ntoolchain go1.99.0\n"))
			case "invalid-module":
				writeCIFixtureFile(t, target, "go.mod", []byte("go 1.25.0\n"))
			case "case-alias":
				writeCIFixtureFile(t, target, ".GITLAB-CI.YML", []byte("keep"))
			case "bad-bundle-schema":
				p := filepath.Join(source, "bundles/go-security/bundle.yaml")
				data, _ := os.ReadFile(p)
				var b map[string]any
				_ = json.Unmarshal(data, &b)
				b["schemaVersion"] = 2
				data, _ = json.Marshal(b)
				writeCIFixtureFile(t, source, "bundles/go-security/bundle.yaml", data)
				runCITestGit(t, source, "add", ".")
				runCITestGit(t, source, "commit", "-m", "bad bundle")
				runCITestGit(t, source, "tag", "-d", "v0.1.0")
				runCITestGit(t, source, "tag", "-a", "v0.1.0", "-m", "bad bundle")
				release["revision"] = runCITestGit(t, source, "rev-parse", "HEAD")
			}
			data, _ = json.Marshal(catalog)
			writeCIFixtureFile(t, source, "ci-releases.yaml", data)
			if runCITestGit(t, source, "status", "--porcelain") != "" {
				runCITestGit(t, source, "add", ".")
				runCITestGit(t, source, "commit", "-m", "update catalog")
			}
			args := []string{"--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"}
			if name == "different-installation" || name == "missing-installed-file" || name == "wrong-mode" {
				if out, err := executeCICommand(args...); err != nil {
					t.Fatalf("setup: %v %s", err, out)
				}
				if name == "missing-installed-file" {
					if err := os.Remove(filepath.Join(target, ".gitlab-ci.yml")); err != nil {
						t.Fatal(err)
					}
				} else if name == "wrong-mode" {
					if err := os.Chmod(filepath.Join(target, ".gitlab-ci.yml"), 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					manifestPath := filepath.Join(target, ".gitlab/ci-installation.yaml")
					data, _ := os.ReadFile(manifestPath)
					data = bytes.Replace(data, []byte("version: v0.1.0"), []byte("version: v0.2.0"), 1)
					writeCIFixtureFile(t, target, ".gitlab/ci-installation.yaml", data)
				}
				if name != "different-installation" {
					if out, err := executeCICommand("ci", "inspect", "--dir", target); err == nil || (!strings.Contains(out, "missing") && !strings.Contains(out, "modified")) {
						t.Fatalf("inspect drift: %v %s", err, out)
					}
					return
				}
			}
			if out, err := executeCICommand(append(args, "--dry-run")...); err == nil {
				t.Fatalf("accepted %s: %s", name, out)
			}
		})
	}
}

// TestCIReleaseIgnoresSameNamedBranch 验证归档读取已经校验的 tag，而不是同名分支。
func TestCIReleaseIgnoresSameNamedBranch(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	runCITestGit(t, source, "switch", "-c", "v0.1.0")
	payload := []byte("shadow branch payload")
	writeCIFixtureFile(t, source, "bundles/go-security/.gitlab-ci.yml", payload)
	p := filepath.Join(source, "bundles/go-security/bundle.yaml")
	data, _ := os.ReadFile(p)
	var bundle map[string]any
	_ = json.Unmarshal(data, &bundle)
	bundle["files"].([]any)[0].(map[string]any)["sha256"] = FetchSHA256(payload)
	data, _ = json.Marshal(bundle)
	writeCIFixtureFile(t, source, "bundles/go-security/bundle.yaml", data)
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "shadow branch")
	runCITestGit(t, source, "switch", "main")
	if out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(target, ".gitlab-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(data, payload) {
		t.Fatal("installed branch content instead of verified tag")
	}
}

// TestCICacheTamperingDoesNotRedefineBundle 验证缓存内清单和内容同时被修改也不能替代 tag 清单。
func TestCICacheTamperingDoesNotRedefineBundle(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	args := []string{"--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"}
	if out, err := executeCICommand(append(args, "--dry-run")...); err != nil {
		t.Fatalf("prepare: %v %s", err, out)
	}
	revision := runCITestGit(t, source, "rev-parse", "refs/tags/v0.1.0^{commit}")
	cached := filepath.Join(cache, "ci", FetchSHA256([]byte(source)), "sources", revision, "bundles/go-security")
	data := []byte("tampered payload")
	writeCIFixtureFile(t, cached, ".gitlab-ci.yml", data)
	p := filepath.Join(cached, "bundle.yaml")
	original, _ := os.ReadFile(p)
	var bundle map[string]any
	_ = json.Unmarshal(original, &bundle)
	bundle["files"].([]any)[0].(map[string]any)["sha256"] = FetchSHA256(data)
	original, _ = json.Marshal(bundle)
	writeCIFixtureFile(t, cached, "bundle.yaml", original)
	if out, err := executeCICommand(args...); err == nil {
		t.Fatalf("accepted modified cache: %s", out)
	}
	if _, err := os.Stat(filepath.Join(target, ".gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("cache failure wrote payload")
	}
}

// TestCIIncompatibleJSONReport 验证版本不兼容仍输出机器可读状态并保持退出码。
func TestCIIncompatibleJSONReport(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	p := filepath.Join(source, "ci-releases.yaml")
	data, _ := os.ReadFile(p)
	data = bytes.ReplaceAll(data, []byte("v0.0.1"), []byte("v99.0.0"))
	writeCIFixtureFile(t, source, "ci-releases.yaml", data)
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "minimum CLI")
	out, err := executeCICommand("--output", "json", "--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0", "--dry-run")
	var report map[string]any
	if err == nil || json.Unmarshal([]byte(out), &report) != nil || report["status"] != "incompatible" {
		t.Fatalf("missing incompatible JSON: %v %s", err, out)
	}
	var exitErr *ExitError
	if !AsExitError(err, &exitErr) || exitErr.Code != ExitIncompatible {
		t.Fatalf("wrong exit error %v", err)
	}
}

// TestCIScaffoldProfiles 对实际消费仓库的两个 profile 验证生成后 hash 和身份不变。
func TestCIScaffoldProfiles(t *testing.T) {
	source := os.Getenv("CI_SCAFFOLD_SOURCE")
	if source == "" {
		t.Skip("set CI_SCAFFOLD_SOURCE for cross-repository profile acceptance")
	}
	for _, profile := range []string{"minimal", "example"} {
		t.Run(profile, func(t *testing.T) {
			target := t.TempDir()
			if err := RenderProject(source, target, RenderInput{Name: "legacy-service", Module: "example.invalid/legacy", Profile: profile}); err != nil {
				t.Fatal(err)
			}
			if report, err := InspectCI(target); err != nil || report.Status != "complete" {
				t.Fatalf("generated CI differs: %v %+v", err, report)
			}
			installation, err := FetchCIInstallation(target)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := FetchProfile(source, profile)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range installation.Bundle.Files {
				owner, ok := FetchPathOwner(metadata, file.Path)
				if !ok || owner != "project" {
					t.Fatalf("%s owned by %s", file.Path, owner)
				}
			}
		})
	}
}

// TestCILegacyGoDirectiveUsesNewerRunner 验证老项目的语言版本不被安装器自身 Go 版本错误限制。
func TestCILegacyGoDirectiveUsesNewerRunner(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	writeCIFixtureFile(t, target, "go.mod", []byte("module example.invalid/legacy\n\ngo 1.18\n"))
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "old Go directive")
	if out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "init", "--dir", target, "--ci-version", "v0.1.0"); err != nil {
		t.Fatalf("compatible legacy module rejected: %v %s", err, out)
	}
}

// TestCIPathsArePortable 验证不同平台对路径解释一致，拒绝设备名与控制字符。
func TestCIPathsArePortable(t *testing.T) {
	for _, name := range []string{"scripts/security/CON", "scripts/security/aux.txt", "scripts/security/a:b", "scripts/security/a?b", "scripts/security/a\nb", "scripts/security/a.", "scripts/security/a ", "scripts/security/../outside", "/scripts/security/a", "scripts\\security\\a"} {
		if err := ValidateCIPath(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

// TestCILocalSourceCanDisappearAfterInstall 验证本地来源使用绝对路径记录，inspect 不依赖来源继续存在。
func TestCILocalSourceCanDisappearAfterInstall(t *testing.T) {
	source, target, cache := createCIFixture(t, nil)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", relative, "init", "--dir", target, "--ci-version", "v0.1.0"); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	record, recordErr := FetchCIInstallation(target)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if !filepath.IsAbs(record.Repository) || record.Repository != source {
		t.Fatal("source was not normalized")
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if out, err := executeCICommand("--offline", "ci", "inspect", "--dir", target); err != nil {
		t.Fatalf("offline inspection depends on source: %v %s", err, out)
	}
}
