package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createCIUpdateFixture 发布第二个本地 CI 版本，并保留完整旧来源供更新核验。
func createCIUpdateFixture(t *testing.T) (string, string, string) {
	t.Helper()
	source, target, cache := createCIFixture(t, nil)
	args := []string{"--cache-dir", cache, "ci", "--ci-repository", source}
	if out, err := executeCICommand(append(args, "init", "--dir", target, "--ci-version", "v0.1.0")...); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "install")
	payload := []byte("stages: [security, release]\nscan:\n  stage: security\n  script: echo updated\n")
	script := []byte("print('release')\n")
	files := []map[string]string{
		{"path": ".gitlab-ci.yml", "sha256": FetchSHA256(payload), "mode": "0644"},
		{"path": "scripts/release/release.py", "sha256": FetchSHA256(script), "mode": "0644"},
	}
	bundle := map[string]any{"schemaVersion": 1, "name": "go-security", "minGoVersion": "1.25.0", "toolchainGoVersion": "1.27.3", "files": files}
	data, _ := json.Marshal(bundle)
	writeCIFixtureFile(t, source, "bundles/go-security/.gitlab-ci.yml", payload)
	writeCIFixtureFile(t, source, "bundles/go-security/scripts/release/release.py", script)
	writeCIFixtureFile(t, source, "bundles/go-security/bundle.yaml", data)
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "release module")
	runCITestGit(t, source, "tag", "-a", "v0.2.0", "-m", "CI candidate")
	old := runCITestGit(t, source, "rev-parse", "v0.1.0^{commit}")
	revision := runCITestGit(t, source, "rev-parse", "HEAD")
	catalog := fmt.Sprintf(`{"schemaVersion":1,"defaultVersion":"v0.2.0","releases":[{"version":"v0.1.0","revision":%q,"status":"deprecated","minGeneratorVersion":"v0.0.1"},{"version":"v0.2.0","revision":%q,"status":"active","minGeneratorVersion":"v0.0.1"}]}`, old, revision)
	writeCIFixtureFile(t, source, "ci-releases.yaml", []byte(catalog))
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "catalog")
	return source, target, cache
}

// TestCIUpdateLifecycle 验证旧版本升级、预览零写入、配置保留及重复执行。
func TestCIUpdateLifecycle(t *testing.T) {
	source, target, cache := createCIUpdateFixture(t)
	writeCIFixtureFile(t, target, ".gitlab/release-policy.json", []byte("{\"project\":true}\n"))
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "project policy")
	before, _ := os.ReadFile(filepath.Join(target, ciInstallationPath))
	args := []string{"--cache-dir", cache, "--output", "json", "ci", "--ci-repository", source, "update", "--dir", target, "--ci-version", "v0.2.0"}
	if out, err := executeCICommand(append(args, "--dry-run")...); err != nil || !strings.Contains(out, "update") {
		t.Fatalf("preview: %v %s", err, out)
	}
	assertFileEquals(t, filepath.Join(target, ciInstallationPath), string(before))
	if out, err := executeCICommand(args...); err != nil {
		t.Fatalf("update: %v %s", err, out)
	}
	assertFileEquals(t, filepath.Join(target, ".gitlab/release-policy.json"), "{\"project\":true}\n")
	if out, err := executeCICommand("ci", "inspect", "--dir", target); err != nil || !strings.Contains(out, "complete") {
		t.Fatalf("inspect: %v %s", err, out)
	}
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "update")
	if out, err := executeCICommand(args...); err != nil {
		t.Fatalf("repeat: %v %s", err, out)
	}
	if status := runCITestGit(t, target, "status", "--porcelain"); status != "" {
		t.Fatalf("repeat changed files: %s", status)
	}
}

// TestCIUpdateRejectsDrift 验证缺失、定制、清单篡改和新增冲突整次不写入。
func TestCIUpdateRejectsDrift(t *testing.T) {
	for _, name := range []string{"modified", "missing", "mode", "new-conflict", "tampered-record", "dirty"} {
		t.Run(name, func(t *testing.T) {
			source, target, cache := createCIUpdateFixture(t)
			switch name {
			case "modified", "tampered-record":
				writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte("custom\n"))
				if name == "tampered-record" {
					installation, _ := FetchCIInstallation(target)
					installation.Bundle.Files[0].SHA256 = FetchSHA256([]byte("custom\n"))
					data, _ := json.Marshal(installation)
					writeCIFixtureFile(t, target, ciInstallationPath, data)
				}
			case "missing":
				if err := os.Remove(filepath.Join(target, ".gitlab-ci.yml")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(target, ".gitlab-ci.yml"), 0755); err != nil {
					t.Fatal(err)
				}
			case "new-conflict":
				writeCIFixtureFile(t, target, "scripts/release/release.py", []byte("custom script\n"))
			case "dirty":
				writeCIFixtureFile(t, target, "unrelated.txt", []byte("keep"))
			}
			if name != "dirty" {
				runCITestGit(t, target, "add", ".")
				runCITestGit(t, target, "commit", "-m", "local change")
			}
			before, _ := os.ReadFile(filepath.Join(target, ciInstallationPath))
			if out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "update", "--dir", target, "--ci-version", "v0.2.0"); err == nil {
				t.Fatalf("accepted %s: %s", name, out)
			}
			assertFileEquals(t, filepath.Join(target, ciInstallationPath), string(before))
			if name != "new-conflict" {
				if _, err := os.Stat(filepath.Join(target, "scripts/release/release.py")); !os.IsNotExist(err) {
					t.Fatal("failure wrote new payload")
				}
			}
		})
	}
}

// TestCIUpdateRollback 验证替换、新增混合失败时恢复旧内容和来源记录。
func TestCIUpdateRollback(t *testing.T) {
	dir := t.TempDir()
	writeCIFixtureFile(t, dir, ".gitlab-ci.yml", []byte("old"))
	expected := map[string]CIFile{".gitlab-ci.yml": {Path: ".gitlab-ci.yml", SHA256: FetchSHA256([]byte("old")), Mode: "0644"}}
	calls := 0
	err := ApplyCIUpdate(dir, map[string][]byte{".gitlab-ci.yml": []byte("new"), "scripts/release/a.py": []byte("script")}, map[string]os.FileMode{".gitlab-ci.yml": 0644, "scripts/release/a.py": 0644}, expected, func(file *os.File, data []byte) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("simulated write failure")
		}
		return WriteCIFile(file, data)
	})
	if err == nil {
		t.Fatal("accepted failed write")
	}
	assertFileEquals(t, filepath.Join(dir, ".gitlab-ci.yml"), "old")
	if _, err := os.Stat(filepath.Join(dir, "scripts")); !os.IsNotExist(err) {
		t.Fatal("rollback left created directory")
	}
}

// TestCIUpdateProtectsRacingFile 验证预检之后出现的新文件不会被覆盖。
func TestCIUpdateProtectsRacingFile(t *testing.T) {
	dir := t.TempDir()
	writeCIFixtureFile(t, dir, ".gitlab-ci.yml", []byte("racing"))
	err := ApplyCIUpdate(dir, map[string][]byte{".gitlab-ci.yml": []byte("new")}, map[string]os.FileMode{".gitlab-ci.yml": 0644}, map[string]CIFile{}, WriteCIFile)
	if err == nil {
		t.Fatal("overwrote racing file")
	}
	assertFileEquals(t, filepath.Join(dir, ".gitlab-ci.yml"), "racing")
}

// TestCICustomizationAcknowledgement 保留原版 hash，区分人工确认与后续漂移，并禁止自动覆盖。
func TestCICustomizationAcknowledgement(t *testing.T) {
	source, target, cache := createCIUpdateFixture(t)
	writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte("project pipeline\n"))
	before, _ := os.ReadFile(filepath.Join(target, ciInstallationPath))
	out, err := executeCICommand("ci", "customize", "--dir", target, "--path", ".gitlab-ci.yml", "--reason", "保留项目构建入口")
	if err != nil || !strings.Contains(out, "customized") {
		t.Fatalf("acknowledge: %v %s", err, out)
	}
	assertFileEquals(t, filepath.Join(target, ciInstallationPath), string(before))
	if out, err := executeCICommand("ci", "inspect", "--dir", target); err != nil || !strings.Contains(out, "customized") {
		t.Fatalf("inspect: %v %s", err, out)
	}
	runCITestGit(t, target, "add", ".")
	runCITestGit(t, target, "commit", "-m", "acknowledge customization")
	if _, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "update", "--dir", target, "--ci-version", "v0.2.0"); err == nil {
		t.Fatal("overwrote acknowledged customization")
	}
	writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte("later unreviewed change\n"))
	if out, err := executeCICommand("ci", "inspect", "--dir", target); err == nil || !strings.Contains(out, "modified") {
		t.Fatalf("unreviewed drift: %v %s", err, out)
	}
}
