package internal

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenderProjectExcludesSourceReleaseHistory 验证两种 profile 和无 profile 的旧来源不携带脚手架发布历史。
func TestRenderProjectExcludesSourceReleaseHistory(t *testing.T) {
	for _, profile := range []string{"legacy", "minimal", "example"} {
		t.Run(profile, func(t *testing.T) {
			// 📋1.同时建立脚手架私有记录和新工程需要的共享发布能力
			source, target := t.TempDir(), t.TempDir()
			excluded := []string{
				"CHANGELOG.md", "CHANGELOG.zh-CN.md", "changelog.en.md", "CHANGELOG.txt", "CHANGELOG.rst",
				"release-notes.md", "RELEASE-NOTES.en.md", "changelog/v0.6.0.md", "releases/v0.6.0/manifest.json",
				"release-notes/v0.6.0.md", "docs/changelog/v0.6.0.md", "docs/releases/v0.6.0.md", "docs/release-notes/v0.6.0.md",
				"reports/release/manifest.json", "reports/release/incremental.md", "reports/release-r2/cumulative.md",
				"releases.yaml", "release/releases.yaml", ".scaffold/releases.yaml",
				".gitlab/release-policy.json", ".gitlab/ci-customizations.yaml",
			}
			retained := []string{
				"go.mod", "Makefile", "docs/release.md", "docs/releases-guide.md", "docs/CHANGELOG-format.md",
				"scripts/release/release.py", "scripts/release/cliff.toml", "scripts/release/Dockerfile",
				".gitlab/release/README.md", ".gitlab/release/policy.example.json", ".gitlab/release/evidence.example.json",
				".gitlab-ci.yml", ".gitlab/ci-installation.yaml", "internal/app/reports/report.go",
			}
			for _, name := range excluded {
				writeTestFile(t, source, name, "scaffold release history\n")
			}
			for _, name := range retained {
				writeTestFile(t, source, name, "project release capability\n")
			}
			input := RenderInput{Name: "order-service", Module: "example.invalid/order-service", Profile: "minimal"}
			if profile != "legacy" {
				input.Profile = profile
				writeTestFile(t, source, ".scaffold/profiles/"+profile+".yaml", "schemaVersion: 1\nname: "+profile+"\nownership: []\n")
			}

			// 📦2.经真实渲染入口生成，不能仅验证路径匹配函数
			if err := RenderProject(source, target, input); err != nil {
				t.Fatal(err)
			}

			// 🔍3.逐一检查私有历史被排除且工具和业务代码完整保留
			for _, name := range excluded {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(name))); !os.IsNotExist(err) {
					t.Errorf("source release history copied: %s (err=%v)", name, err)
				}
			}
			for _, name := range retained {
				if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(name))); err != nil {
					t.Errorf("project capability missing: %s: %v", name, err)
				}
			}
		})
	}
}

// TestRenderProjectOverlaysCannotRestoreReleaseHistory 验证 profile overlay 也不能重新注入来源发布日志。
func TestRenderProjectOverlaysCannotRestoreReleaseHistory(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	writeTestFile(t, source, ".scaffold/history.md", "scaffold history\n")
	writeTestFile(t, source, ".scaffold/tool.py", "shared tool\n")
	writeTestFile(t, source, ".scaffold/profiles/minimal.yaml", `schemaVersion: 1
name: minimal
overlays:
  - source: .scaffold/history.md
    target: CHANGELOG.md
  - source: .scaffold/history.md
    target: docs/releases/v0.6.0.md
  - source: .scaffold/tool.py
    target: scripts/release/tool.py
`)
	if err := RenderProject(source, target, RenderInput{Name: "order-service", Module: "example.invalid/order-service", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CHANGELOG.md", "docs/releases/v0.6.0.md"} {
		if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Errorf("overlay restored source history: %s", name)
		}
	}
	assertFileEquals(t, filepath.Join(target, "scripts/release/tool.py"), "shared tool\n")
}

// TestRenderProjectPreservesProjectReleaseHistory 验证排除来源记录不会清理目标工程自己的发布日志。
func TestRenderProjectPreservesProjectReleaseHistory(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	writeTestFile(t, source, "CHANGELOG.md", "scaffold history\n")
	writeTestFile(t, target, "CHANGELOG.md", "project history\n")
	if err := RenderProject(source, target, RenderInput{Name: "order-service", Module: "example.invalid/order-service", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, filepath.Join(target, "CHANGELOG.md"), "project history\n")
}
