package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.lock", "Cargo.lock", true},
		{"*.lock", "sub/dir/yarn.lock", true},
		{"vendor/", "vendor/a/b.go", true},
		{"vendor/", "src/vendor/a.go", true},
		{"vendor/", "vendors/a.go", false},
		{"dist/**", "dist/x/y.js", true},
		{"src/**/*.ts", "src/a/b/c.ts", true},
		{"src/**/*.ts", "src/c.ts", true},
		{"src/*.ts", "src/a/c.ts", false},
		{"docs", "docs/readme.md", true},
		{"main", "main", true},
		{"release/*", "release/1.2", true},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestRepoConfigMerge(t *testing.T) {
	var d RepoConfig
	d.fillDefaults()
	d.PathFilters = []string{"!**/*.gen.go"}
	rc, err := ParseRepoConfig([]byte(`
profile: assertive
instructions: Prefer table-driven tests.
path_filters: ["src/**", "!src/legacy/**"]
path_instructions:
  - path: "**/*_test.go"
    instructions: Check for t.Parallel().
auto_review:
  drafts: true
  base_branches: [main, "release/*"]
`), d)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Profile != Assertive || rc.MaxComments != 25 || !*rc.AutoReview.Drafts || !*rc.AutoReview.Enabled {
		t.Errorf("merge wrong: %+v", rc)
	}
	for p, want := range map[string]bool{"src/a.go": true, "src/legacy/x.go": false, "src/x.gen.go": false, "README.md": false} {
		if got := rc.IncludePath(p); got != want {
			t.Errorf("IncludePath(%s) = %v", p, got)
		}
	}
	if got := rc.InstructionsFor("pkg/a_test.go"); len(got) != 1 {
		t.Errorf("InstructionsFor = %v", got)
	}
	if r := rc.AutoReviewSkipReason("feat: x", "develop", false); r == "" {
		t.Errorf("develop should be skipped")
	}
	if r := rc.AutoReviewSkipReason("WIP: x", "main", false); r == "" {
		t.Errorf("WIP title should be skipped")
	}
	if r := rc.AutoReviewSkipReason("feat: x", "release/2", true); r != "" {
		t.Errorf("unexpected skip: %s", r)
	}
	if _, err := ParseRepoConfig([]byte("profile: wild"), d); err == nil {
		t.Errorf("bad profile should fail")
	}
}

func TestLoadAndModelChain(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	t.Setenv("MY_KEY", "secret")
	t.Setenv("CODEREVIEW_MODEL", "")
	t.Setenv("CODEREVIEW_FALLBACK", "")
	_ = os.WriteFile(p, []byte(`
model: main
fallback: [cheap, "ollama:qwen:7b"]
models:
  main:  {provider: bedrock, model: "anthropic.claude-x-v1:0", region: us-east-1, timeout: 90s}
  cheap: {provider: openai, model: gpt-x, api_key: "${MY_KEY}"}
`), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := cfg.ModelChain("")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 || chain[0].Provider != "bedrock" || chain[0].Timeout.Seconds() != 90 || chain[1].APIKey != "secret" || chain[2].Model != "qwen:7b" {
		t.Errorf("chain = %+v", chain)
	}
	chain, _ = cfg.ModelChain("gemini:g-1")
	if chain[0].Provider != "gemini" {
		t.Errorf("override ignored: %+v", chain[0])
	}
	if _, err := cfg.ModelChain("nonsense"); err == nil {
		t.Errorf("bad ref should fail")
	}
}

// The shipped examples must stay loadable.
func TestExamplesParse(t *testing.T) {
	t.Setenv("CODEREVIEW_MODEL", "")
	cfg, err := Load("../../examples/codereview.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) < 5 || len(cfg.GitLab) != 2 || cfg.GitHub.Token == "" && os.Getenv("GITHUB_TOKEN") != "" {
		t.Errorf("example config not fully parsed: %+v", cfg)
	}
	if _, err := cfg.ModelChain(""); err != nil {
		t.Errorf("example model chain: %v", err)
	}
	data, err := os.ReadFile("../../examples/.codereview.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := ParseRepoConfig(data, cfg.Review.Defaults)
	if err != nil {
		t.Fatal(err)
	}
	if rc.MaxComments != 20 || len(rc.PathInstructions) != 3 || rc.IncludePath("docs/x.md") {
		t.Errorf("example repo config not applied: %+v", rc)
	}
}

func TestInlineConfigEnv(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	t.Setenv("CODEREVIEW_MODEL", "")
	t.Setenv("MY_TOKEN", "glpat-x")
	t.Setenv("CODEREVIEW_CONFIG_YAML", "model: fast\nmodels:\n  fast: {provider: ollama, model: q}\ngitlab:\n  - base_url: https://gl.example.com/\n    token: ${MY_TOKEN}\n")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "fast" || len(cfg.GitLab) != 1 || cfg.GitLab[0].Token != "glpat-x" || cfg.GitLab[0].BaseURL != "https://gl.example.com" {
		t.Errorf("inline config not applied: %+v", cfg)
	}
}

// Lookup order: --config, $CODEREVIEW_CONFIG, $CODEREVIEW_CONFIG_YAML, then
// ./codereview.yaml (issue #49).
func TestSourceOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("codereview.yaml", "model: cwd\n")
	explicit := write("explicit.yaml", "model: explicit\n")
	t.Chdir(dir)
	t.Setenv("CODEREVIEW_CONFIG", "")
	t.Setenv("CODEREVIEW_CONFIG_YAML", "")

	check := func(flag, want string) {
		t.Helper()
		name, data, err := Source(flag)
		if err != nil || string(data) != "model: "+want+"\n" {
			t.Errorf("want %s, got %s (%q) %v", want, name, data, err)
		}
	}
	check("", "cwd") // the fallback when nothing is set

	t.Setenv("CODEREVIEW_CONFIG_YAML", "model: inline\n")
	check("", "inline") // the env var beats the file in the working directory

	t.Setenv("CODEREVIEW_CONFIG", explicit)
	check("", "explicit") // a path in $CODEREVIEW_CONFIG beats the inline YAML

	t.Setenv("CODEREVIEW_CONFIG", "")
	check(explicit, "explicit") // and so does --config
}
