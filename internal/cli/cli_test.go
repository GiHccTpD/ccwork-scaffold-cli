package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderProject verifies bounded path, module and package rewriting.
func TestRenderProject(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "cmd", "scaffold"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "internal", "scaffold"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":                      "module scaffold\n\ngo 1.25.0\n",
		"cmd/scaffold/main.go":        "package main\nimport \"scaffold/internal/scaffold\"\nfunc main(){ scaffold.NewApp() }\n",
		"internal/scaffold/app.go":    "package scaffold\nconst AppName = \"scaffold\"\n",
		"configs/config.example.yaml": "server:\n  enabled: true\n",
		"configs/config.yaml":         "password: real-secret\n",
		".idea/workspace.xml":         "private",
		"README.md":                   "# scaffold\n`scaffold`\ncmd/scaffold\n",
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
	if err := RenderProject(source, target, RenderInput{Name: "order-service", Module: "example.com/order-service", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(target, "go.mod"), "module example.com/order-service\n")
	assertFileContains(t, filepath.Join(target, "cmd", "order-service", "main.go"), "example.com/order-service/internal/order-service")
	assertFileContains(t, filepath.Join(target, "internal", "order-service", "app.go"), "package order_service")
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
	writeTestFile(t, source, "cmd/scaffold/main.go", "package main\nimport \"scaffold/internal/scaffold\"\nfunc main(){ scaffold.NewApp() }\n")
	writeTestFile(t, source, "internal/scaffold/app.go", "package scaffold\nfunc NewApp() {}\n")
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
	assertFileContains(t, filepath.Join(target, "cmd", "demo-service", "main.go"), "example.com/demo/internal/demo-service")
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
