package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// 每个模板都得有中英两份。漏一份的话 --lang zh 会在 assets.Read 里 panic，
// 而那是 sbx 启动很久之后才会走到的一条路。
func TestTemplatesExistInBothLanguages(t *testing.T) {
	for _, pattern := range []string{projectTemplate, forwardTemplate, "prep/prep.Dockerfile.%s.tmpl"} {
		for _, lang := range []string{langEN, langZH} {
			b, err := templateAsset(pattern, lang)
			if err != nil {
				t.Fatalf("%s/%s: %v", pattern, lang, err)
			}
			if len(b) == 0 {
				t.Fatalf("%s/%s is empty", pattern, lang)
			}
		}
	}
}

func TestTemplateAssetRejectsUnknownLang(t *testing.T) {
	if _, err := templateAsset(projectTemplate, "fr"); err == nil {
		t.Fatal("expected an error for an unknown language")
	}
}

// 模板里的每一行都该是注释：写一份模板不该改变任何行为。
func TestTomlTemplatesAreAllComments(t *testing.T) {
	for _, pattern := range []string{projectTemplate, forwardTemplate} {
		for _, lang := range []string{langEN, langZH} {
			b, _ := templateAsset(pattern, lang)
			var cfg map[string]any
			if err := toml.Unmarshal(b, &cfg); err != nil {
				t.Fatalf("%s/%s is not valid TOML: %v", pattern, lang, err)
			}
			if len(cfg) != 0 {
				t.Fatalf("%s/%s sets %d key(s); the template must be inert", pattern, lang, len(cfg))
			}
		}
	}
}

// forward 的模板不能落在项目层：文件里就该说清楚这件事。
func TestForwardTemplatePointsAtTheWorkspaceLayer(t *testing.T) {
	for _, lang := range []string{langEN, langZH} {
		b, _ := templateAsset(forwardTemplate, lang)
		if !strings.Contains(string(b), "~/.sbx/workspaces/") {
			t.Fatalf("the %s forward template does not name the workspace layer", lang)
		}
	}
}

func TestWriteTemplateRefusesToClobber(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sandbox.toml")
	if err := writeTemplate(p, projectTemplate, langEN, false); err != nil {
		t.Fatal(err)
	}
	if err := writeTemplate(p, projectTemplate, langEN, false); err == nil {
		t.Fatal("expected the second write to refuse without --force")
	}
	before, _ := os.ReadFile(p)
	if err := writeTemplate(p, projectTemplate, langZH, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if string(before) == string(after) {
		t.Fatal("--force did not overwrite the file")
	}
}
