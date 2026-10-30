package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// createRuntimeCIFixture 为运行时引用建立独立的本地正式来源 fixture。
func createRuntimeCIFixture(t *testing.T) (string, string, string) {
	t.Helper()
	source, target, cache := createCIFixture(t, nil)
	writeCIFixtureFile(t, source, "runtime/go-security.yml", []byte("include:\n  - local: /bundles/go-security/.gitlab-ci.yml\n"))
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "runtime fixture")
	runCITestGit(t, source, "tag", "-a", "v0.2.0", "-m", "runtime fixture only")
	revision := runCITestGit(t, source, "rev-parse", "HEAD")
	catalog := CICatalog{SchemaVersion: 1, DefaultVersion: "v0.2.0", Releases: []CIRelease{{Version: "v0.2.0", Revision: revision, Status: "active", MinGeneratorVersion: "v0.0.1"}}}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	writeCIFixtureFile(t, source, "ci-releases.yaml", data)
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "runtime catalog fixture")
	return source, target, cache
}

// TestCIReferencePinsSourceWithoutWriting 验证引用固定真实 SHA，并且不安装共享副本。
func TestCIReferencePinsSourceWithoutWriting(t *testing.T) {
	source, target, cache := createRuntimeCIFixture(t)
	args := []string{"--cache-dir", cache, "ci", "--ci-repository", source, "reference", "--ci-version", "v0.2.0", "--ci-project", "group/ops-pipelines", "--local-config", ".gitlab/ci-runtime-overrides.yml"}
	out, err := executeCICommand(args...)
	if err != nil {
		t.Fatalf("reference: %v %s", err, out)
	}
	var config struct {
		Include   []map[string]string `yaml:"include"`
		Variables map[string]string   `yaml:"variables"`
	}
	if err := yaml.Unmarshal([]byte(out), &config); err != nil {
		t.Fatal(err)
	}
	revision := runCITestGit(t, source, "rev-parse", "v0.2.0^{commit}")
	if len(config.Include) != 2 || config.Include[0]["ref"] != revision || config.Include[0]["file"] != "/runtime/go-security.yml" || config.Include[0]["project"] != "group/ops-pipelines" || config.Include[1]["local"] != "/.gitlab/ci-runtime-overrides.yml" {
		t.Fatalf("unexpected includes: %+v", config.Include)
	}
	if config.Variables["OPS_PIPELINES_REVISION"] != revision || config.Variables["OPS_PIPELINES_PROJECT"] != "group/ops-pipelines" {
		t.Fatalf("unpinned assets: %+v", config.Variables)
	}
	if _, err := os.Stat(filepath.Join(target, ".gitlab-ci.yml")); !os.IsNotExist(err) {
		t.Fatal("reference wrote target project")
	}
	if out, err := executeCICommand(append([]string{"--offline"}, args...)...); err != nil || !strings.Contains(out, revision) {
		t.Fatalf("offline reference: %v %s", err, out)
	}
	for _, project := range []string{"../ops", "https://user:token@example.invalid/ops", "group/../ops", "ops"} {
		if _, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "reference", "--ci-version", "v0.2.0", "--ci-project", project); err == nil {
			t.Fatalf("unsafe project accepted: %s", project)
		}
	}
}

// TestCIReferenceRejectsVersionWithoutRuntime 防止把已发布旧资产或本地候选冒充运行时正式版本。
func TestCIReferenceRejectsVersionWithoutRuntime(t *testing.T) {
	source, _, cache := createCIFixture(t, nil)
	_, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "reference", "--ci-version", "v0.1.0", "--ci-project", "group/ops-pipelines")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("missing runtime not rejected: %v", err)
	}
}

// TestRenderReferenceExcludesSharedCopies 验证两个 profile 的运行时模式只保留项目配置。
func TestRenderReferenceExcludesSharedCopies(t *testing.T) {
	for _, profile := range []string{"minimal", "example"} {
		t.Run(profile, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			shared := []string{".gitlab-ci.yml", ".gitlab/ci-installation.yaml", ".gitlab/ci-customizations.yaml", "scripts/security/run.sh", "scripts/release/release.py", ".gitlab/security/rules.yml", ".gitlab/release/example.json"}
			for _, name := range append(shared, ".gitlab/schedules.yml", "scripts/build.sh") {
				writeCIFixtureFile(t, source, name, []byte("fixture"))
			}
			input := RenderInput{Name: "runtime-service", Module: "example.invalid/runtime", Profile: profile, CIReference: []byte("include: []\n")}
			if err := RenderProject(source, target, input); err != nil {
				t.Fatal(err)
			}
			for _, name := range shared[1:] {
				if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
					t.Fatalf("copied shared asset %s", name)
				}
			}
			assertFileEquals(t, filepath.Join(target, ".gitlab-ci.yml"), "include: []\n")
			assertFileEquals(t, filepath.Join(target, ".gitlab/schedules.yml"), "fixture")
			assertFileEquals(t, filepath.Join(target, "scripts/build.sh"), "fixture")
		})
	}
}

// TestInspectCIReference 检查离线引用一致性，但不将其等同于真实 Runner 验收。
func TestInspectCIReference(t *testing.T) {
	source, target, cache := createRuntimeCIFixture(t)
	out, err := executeCICommand("--cache-dir", cache, "ci", "--ci-repository", source, "reference", "--ci-version", "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte(out))
	report, err := InspectCI(target)
	if err != nil || report.Status != "referenced" {
		t.Fatalf("reference inspection: %+v %v", report, err)
	}
	writeCIFixtureFile(t, target, ".gitlab-ci.yml", []byte(strings.Replace(out, "OPS_PIPELINES_REVISION:", "DIFFERENT_REVISION:", 1)))
	if _, err := InspectCI(target); err == nil {
		t.Fatal("missing runtime SHA accepted")
	}
}

// TestCommandNewWithCIReference 验证真实 new 命令选择运行时接入并在缺少正式版本时零写入。
func TestCommandNewWithCIReference(t *testing.T) {
	ci, _, cache := createRuntimeCIFixture(t)
	source := t.TempDir()
	writeTestFile(t, source, "go.mod", "module scaffold\n\ngo 1.25.0\n")
	writeTestFile(t, source, ".gitlab-ci.yml", "legacy copy")
	writeTestFile(t, source, "scripts/security/run.sh", "legacy copy")
	for _, args := range [][]string{{"init"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "fixture"}, {"add", "."}, {"commit", "-m", "scaffold fixture"}, {"tag", "-a", "v0.3.0", "-m", "fixture"}} {
		runCITestGit(t, source, args...)
	}
	revision := runCITestGit(t, source, "rev-parse", "HEAD")
	writeTestFile(t, source, "releases.yaml", "schemaVersion: 1\ndefaultVersion: v0.3.0\nreleases:\n  - version: v0.3.0\n    revision: "+revision+"\n    status: active\n    creatable: true\n    upgradeSource: true\n    minGeneratorVersion: v0.0.1\n    migrationSchemaVersion: 1\n")
	runCITestGit(t, source, "add", ".")
	runCITestGit(t, source, "commit", "-m", "catalog fixture")
	for _, profile := range []string{"minimal", "example"} {
		target := filepath.Join(t.TempDir(), "runtime-service")
		args := []string{"--repository", source, "--cache-dir", cache, "new", "--name", "runtime-service", "--module", "example.invalid/runtime", "--dir", target, "--profile", profile, "--no-git-init", "--ci-mode", "reference", "--ci-version", "v0.2.0", "--ci-repository", ci}
		if out, err := executeCICommand(args...); err != nil {
			t.Fatalf("new reference: %v %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(target, "scripts/security")); !os.IsNotExist(err) {
			t.Fatal("new command copied CI scripts")
		}
		if report, err := InspectCI(target); err != nil || report.Status != "referenced" {
			t.Fatalf("generated reference: %+v %v", report, err)
		}
	}
}
