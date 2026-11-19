package review

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/pkg/diff"
)

func TestLoadGuidelines(t *testing.T) {
	repo := map[string]string{
		"AGENTS.md":             "Use log/slog. Never touch generated code.",
		"api/AGENTS.md":         "Laravel: use form requests for validation.",
		"api/public/AGENTS.md":  "Frontend: AngularJS 1.x only.",
		"go-services/AGENTS.md": "Go rules.",
		".cursorrules":          strings.Repeat("x", 20<<10), // truncated per file
	}
	var mu sync.Mutex
	var asked []string
	fetch := func(_ context.Context, p string) ([]byte, error) {
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		if c, ok := repo[p]; ok {
			return []byte(c), nil
		}
		return nil, errors.New("not found")
	}
	gs := LoadGuidelines(context.Background(), DefaultGuidelineFiles, []string{"api/public/scripts/hr.js"}, fetch)
	var paths []string
	for _, g := range gs {
		paths = append(paths, g.Path)
	}
	if got := strings.Join(paths, ","); got != "AGENTS.md,.cursorrules,api/AGENTS.md,api/public/AGENTS.md" {
		t.Errorf("loaded = %s", got)
	}
	for _, p := range asked {
		if p == "go-services/AGENTS.md" {
			t.Error("only folders of changed files should be searched")
		}
	}
	for _, g := range gs {
		if g.Path == ".cursorrules" && !strings.HasSuffix(g.Content, "[truncated]") {
			t.Error("large guideline should be truncated")
		}
	}

	nested := nestedFor(gs, "api/public/scripts/hr.js")
	if len(nested) != 2 || !strings.Contains(nested[0], "api/public/AGENTS.md") {
		t.Errorf("nearest folder first: %v", nested)
	}
	if len(nestedFor(gs, "README.md")) != 0 {
		t.Error("root file gets no nested guidelines")
	}
	sys := buildSystem(config.RepoConfig{Profile: config.Balanced}, gs)
	if !strings.Contains(sys, `<repository_guidelines source="AGENTS.md">`) || strings.Contains(sys, "Laravel") {
		t.Errorf("only root guidelines belong in the system prompt")
	}
}

func TestNestedGuidelinesReachFileBlock(t *testing.T) {
	h, _ := diff.ParsePatch("@@ -1 +1 @@\n-a\n+b\n")
	f := diff.File{Path: "api/x.php", OldPath: "api/x.php", Status: diff.Modified, Hunks: h}
	e := &Engine{Limits: config.Review{BatchTokens: 40000, FullFileMaxLines: 800, ContextLines: 60}}
	units := e.buildUnits(Input{Repo: defaults(), Guidelines: []Guideline{{Path: "api/AGENTS.md", Dir: "api", Content: "Use form requests."}}}, []*diff.File{&f}, nil)
	if len(units) != 1 || !strings.Contains(units[0].text, "<instructions>\nGuidelines from api/AGENTS.md:\nUse form requests.") {
		t.Errorf("nested guideline missing from file block:\n%s", units[0].text)
	}
}

func TestConfigChangesNote(t *testing.T) {
	got := ConfigChanges([]string{"main.go", ".codereview.yaml", "api/AGENTS.md", "docs/agents.md", "CLAUDE.md"}, ".codereview.yaml", DefaultGuidelineFiles)
	if strings.Join(got, ",") != ".codereview.yaml,api/AGENTS.md,CLAUDE.md" {
		t.Errorf("config changes = %v", got)
	}
	md := SummaryMarkdown(&Result{Summary: "x", ConfigChanges: got}, SummaryOptions{})
	if !strings.Contains(md, "apply after this PR merges") || !strings.Contains(md, "`api/AGENTS.md`") {
		t.Errorf("note missing:\n%s", md)
	}
	if strings.Contains(SummaryMarkdown(&Result{Summary: "x"}, SummaryOptions{}), "[!NOTE]") {
		t.Error("no note without config changes")
	}
}
