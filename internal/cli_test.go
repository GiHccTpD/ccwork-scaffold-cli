package internal

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// TestRenderProject verifies V2 paths, module imports and service identity rendering.
func TestRenderProject(t *testing.T) {
	source := t.TempDir()
	files := map[string]string{
		"go.mod":                      "module scaffold\n\ngo 1.25.0\n",
		"main.go":                     "package main\n\nimport \"scaffold/internal/app\"\n\n// @title Scaffold API\n// @description Scaffold HTTP service API\nfunc main() { app.NewApp() }\n",
		"internal/app/app.go":         "package app\n\nfunc NewApp() {\n\tcommand := Command{\n\t\tUse: AppName,\n\t\tShort:        \"scaffold\",\n\t}\n\tpm := metrics.NewPrometheusMetrics(\"scaffold\", metrics.WithPort(configs.FetchPrometheusMetricsPort()))\n\tlog.Infow(\"Shutting down scaffold ...\")\n\t_, _ = command, pm\n}\n\nconst AppName   = \"scaffold\"\n",
		"internal/app/router.go":      "package app\n\nfunc installRouters() { register(\"/ccwork/scaffold/robot/v1\") }\n",
		"internal/biz/biz.go":         "package biz\n",
		"internal/controller/app.go":  "package controller\n",
		"internal/store/store.go":     "package store\n",
		"docs/docs.go":                "package docs\n\nvar title = \"Scaffold API\"\nvar description = \"Scaffold HTTP service API\"\nvar route = \"/ccwork/scaffold/robot/v1\"\n",
		"docs/swagger.json":           "{\"title\":\"Scaffold API\",\"description\":\"Scaffold HTTP service API\",\"route\":\"/ccwork/scaffold/robot/v1\"}\n",
		"configs/config.example.yaml": "server:\n  enabled: true\n",
		"configs/config.yaml":         "password: real-secret\n",
		".idea/workspace.xml":         "private",
		".jenkins/Jenkinsfile":        "@Library('go-service-ci@v1') _\ngoServicePipeline()\n",
		"Makefile":                    "legacy-path=cmd/scaffold\nlegacy-bin=./scaffold\n",
		"Dockerfile":                  "RUN go build -o scaffold ./cmd/scaffold/main.go\nENTRYPOINT [\"./scaffold\"]\n",
		"README.md":                   "# scaffold\n`scaffold`\ninternal/app\n默认 APP_NAME 由 go.mod 推导为 scaffold\n默认构建产物：./build/scaffold\n--build-arg APP_NAME=scaffold\n镜像：scaffold:v1.2.3\nGET /ccwork/scaffold/robot/v1\n",
		"scripts/scaffold.bat":        "go build -o scaffold.exe .\r\n",
	}
	for name, content := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := RenderProject(source, target, RenderInput{Name: "order-service", Module: "example.com/platform/order-api", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(target, "go.mod"), "module example.com/platform/order-api\n")
	assertFileContains(t, filepath.Join(target, "main.go"), `"example.com/platform/order-api/internal/app"`)
	assertFileContains(t, filepath.Join(target, "main.go"), "app.NewApp()")
	assertFileContains(t, filepath.Join(target, "main.go"), "order-service API")
	assertFileContains(t, filepath.Join(target, "main.go"), "order-service HTTP service API")
	assertFileContains(t, filepath.Join(target, "internal", "app", "app.go"), `AppName   = "order-service"`)
	assertFileContains(t, filepath.Join(target, "internal", "app", "app.go"), `Short:        "order-service"`)
	assertFileContains(t, filepath.Join(target, "internal", "app", "app.go"), `metrics.NewPrometheusMetrics("order-service"`)
	assertFileContains(t, filepath.Join(target, "internal", "app", "app.go"), "Shutting down order-service ...")
	assertFileContains(t, filepath.Join(target, "internal", "app", "router.go"), "/ccwork/order-service/robot/v1")
	assertFileContains(t, filepath.Join(target, "docs", "docs.go"), "order-service API")
	assertFileContains(t, filepath.Join(target, "docs", "docs.go"), "order-service HTTP service API")
	assertFileContains(t, filepath.Join(target, "docs", "swagger.json"), "order-service API")
	assertFileContains(t, filepath.Join(target, "docs", "swagger.json"), "order-service HTTP service API")
	assertFileContains(t, filepath.Join(target, "docs", "swagger.json"), "/ccwork/order-service/robot/v1")
	assertFileEquals(t, filepath.Join(target, ".jenkins", "Jenkinsfile"), "@Library('go-service-ci@v1') _\ngoServicePipeline()\n")
	assertFileEquals(t, filepath.Join(target, "Makefile"), "legacy-path=cmd/scaffold\nlegacy-bin=./scaffold\n")
	assertFileEquals(t, filepath.Join(target, "Dockerfile"), "RUN go build -o scaffold ./cmd/scaffold/main.go\nENTRYPOINT [\"./scaffold\"]\n")
	assertFileEquals(t, filepath.Join(target, "scripts", "order-service.bat"), "go build -o scaffold.exe .\r\n")
	assertFileContains(t, filepath.Join(target, "README.md"), "internal/app")
	assertFileContains(t, filepath.Join(target, "README.md"), "默认 APP_NAME 由 go.mod 推导为 order-api")
	assertFileContains(t, filepath.Join(target, "README.md"), "./build/order-api")
	assertFileContains(t, filepath.Join(target, "README.md"), "APP_NAME=order-service")
	assertFileContains(t, filepath.Join(target, "README.md"), "order-service:v1.2.3")
	assertFileContains(t, filepath.Join(target, "README.md"), "/ccwork/order-service/robot/v1")
	for _, dir := range []string{"app", "biz", "controller", "store"} {
		info, err := os.Stat(filepath.Join(target, "internal", dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("V2 generic internal directory %q must exist, info=%v err=%v", dir, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "internal", "order-service")); !os.IsNotExist(err) {
		t.Fatalf("V2 internal directory must keep its generic path, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "cmd", "order-service")); !os.IsNotExist(err) {
		t.Fatalf("V2 root entry must not create a renamed cmd directory, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "scripts", "scaffold.bat")); !os.IsNotExist(err) {
		t.Fatalf("renamed Windows script must not keep its source path, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "configs", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("real config should be excluded, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".idea")); !os.IsNotExist(err) {
		t.Fatalf("IDE files should be excluded, err=%v", err)
	}
}

// TestBuildMigrationPlanRejectsModifiedFile verifies conflict-before-write behavior.
func TestBuildMigrationPlanRejectsModifiedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("business change"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := NewManifest("order-service", "example.com/order-service", "minimal", "local", "v0.1.0", "abc", generatorVersion, true)
	_, changes, _, err := BuildMigrationPlan(dir, manifest, Migration{SchemaVersion: 1, From: "v0.1.0", To: "v0.2.0", Operations: []Operation{{Type: "replace-if-unmodified", Path: "README.md", OldContent: "golden", Content: "updated"}}})
	if err == nil || changes != nil {
		t.Fatalf("expected conflict with no plan, err=%v changes=%v", err, changes)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "business change" {
		t.Fatalf("conflict must not write files, data=%q err=%v", data, readErr)
	}
}

// TestCompareVersion verifies generator minimum-version checks across tags.
func TestCompareVersion(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"v0.0.1", "v0.0.1", 0},
		{"v0.2.0", "v0.1.9", 1},
		{"v1.0.0-alpha", "v1.0.0", -1},
	} {
		if got := CompareVersion(test.left, test.right); got != test.want {
			t.Errorf("CompareVersion(%q, %q)=%d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestResolveGeneratorVersion(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{
			name: "uses module tag",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.3"}},
			ok:   true,
			want: "v0.0.3",
		},
		{
			name: "falls back for development build",
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:   true,
			want: generatorVersion,
		},
		{
			name: "falls back without build info",
			want: generatorVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveGeneratorVersion(tt.info, tt.ok); got != tt.want {
				t.Fatalf("ResolveGeneratorVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommandUsesExecutableName(t *testing.T) {
	command := NewCommand(&strings.Builder{}, &strings.Builder{})
	if got, want := command.Use, "ccwork-scaffold-cli"; got != want {
		t.Fatalf("command.Use = %q, want %q", got, want)
	}
}

func TestVersionUsesExecutableName(t *testing.T) {
	var output strings.Builder
	command := NewCommand(&output, &output)
	command.SetArgs([]string{"version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(output.String()), "ccwork-scaffold-cli "+generatorVersion; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

// TestValidateRepositoryRejectsCredentials verifies source URL boundary validation.
func TestValidateRepositoryRejectsCredentials(t *testing.T) {
	if err := ValidateRepository("https://user:password@example.com/repo.git"); err == nil {
		t.Fatal("expected embedded credentials to be rejected")
	}
	if err := ValidateModule("https://user@example.com/repo"); err == nil {
		t.Fatal("expected module URL to be rejected")
	}
}

// TestCommandNewFromAnnotatedTag verifies the end-to-end local Git generation seam.
func TestCommandNewFromAnnotatedTag(t *testing.T) {
	source := t.TempDir()
	writeTestFile(t, source, "go.mod", "module scaffold\n\ngo 1.25.0\n")
	writeTestFile(t, source, "releases.yaml", "schemaVersion: 1\ndefaultVersion: v0.2.0\nreleases:\n  - version: v0.2.0\n    revision: \"<full-commit>\"\n    status: active\n    creatable: true\n    upgradeSource: true\n    minGeneratorVersion: v0.0.1\n    migrationSchemaVersion: 1\n")
	writeTestFile(t, source, "main.go", "package main\nimport \"scaffold/internal/app\"\nfunc main(){ app.NewApp() }\n")
	writeTestFile(t, source, "internal/app/app.go", "package app\nfunc NewApp() {}\n")
	ctx := context.Background()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}, {"add", "."}, {"commit", "-m", "initial"}, {"tag", "-a", "v0.2.0", "-m", "release"}} {
		if err := runCommand(ctx, "git", append([]string{"-C", source}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "demo-service")
	var output strings.Builder
	command := NewCommand(&output, &output)
	command.SetArgs([]string{"--repository", source, "--cache-dir", t.TempDir(), "new", "--name", "demo-service", "--module", "example.com/demo", "--dir", target, "--no-git-init"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	manifest, err := FetchManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Scaffold.Version != "v0.2.0" || manifest.Scaffold.Verification != "verified" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	assertFileContains(t, filepath.Join(target, "main.go"), "example.com/demo/internal/app")
	assertFileContains(t, filepath.Join(target, "main.go"), "app.NewApp()")
}

// assertFileContains verifies generated text at the public rendering seam.
func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q; got %q", path, want, data)
	}
}

// assertFileEquals verifies an unchanged generated file.
func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

// writeTestFile creates a fixture file and its parent directories.
func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
