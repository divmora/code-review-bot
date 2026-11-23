package app

import (
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Issue #23: a token goes only to its configured host, never to a host
// taken from the PR URL.
func TestClientForHostBinding(t *testing.T) {
	r := &Runner{Cfg: &config.Config{
		GitHub: config.GitHub{APIURL: "https://api.github.com", Token: "ghp"},
		GitLab: []config.GitLab{{BaseURL: "https://gitlab.example.com", Token: "glpat"}},
	}}
	for _, c := range []struct {
		ref  vcs.Ref
		ok   bool
		hint string
	}{
		{vcs.Ref{Platform: vcs.GitLab, Host: "gitlab.example.com"}, true, ""},
		{vcs.Ref{Platform: vcs.GitLab, Host: "GitLab.Example.com"}, true, ""},
		{vcs.Ref{Platform: vcs.GitLab, Host: "evil.example"}, false, "GITLAB_URL=https://evil.example"},
		{vcs.Ref{Platform: vcs.GitHub, Host: "github.com"}, true, ""},
		{vcs.Ref{Platform: vcs.GitHub, Host: "ghe.evil.example"}, false, "GITHUB_API_URL"},
	} {
		_, err := r.ClientFor(c.ref)
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), c.hint)) {
			t.Errorf("%s %s: %v", c.ref.Platform, c.ref.Host, err)
		}
	}
	// GitHub Enterprise Server: its API URL serves its own host only.
	r.Cfg.GitHub.APIURL = "https://ghe.example.com/api/v3"
	if _, err := r.ClientFor(vcs.Ref{Platform: vcs.GitHub, Host: "ghe.example.com"}); err != nil {
		t.Errorf("GHES: %v", err)
	}
	if _, err := r.ClientFor(vcs.Ref{Platform: vcs.GitHub, Host: "github.com"}); err == nil {
		t.Error("a GHES token doesn't go to github.com")
	}
}
