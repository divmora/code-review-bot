package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Precedence, lowest first: review.defaults, .codereview.yaml, the admin
// overrides, the CLI flags (issue #45).
func TestRepoConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".codereview.yaml"), []byte("profile: balanced\nmax_comments: 10\nmin_severity: nitpick\nlanguage: German\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, "http://127.0.0.1:1")
	r.Cfg.Review.Defaults.Poem = ptr(true)
	chill, five, major := config.Chill, 5, "major"
	ov := &config.Overrides{Profile: &chill, MaxComments: &five, MinSeverity: &major}
	c := &vcs.LocalClient{Dir: dir}

	rc, err := r.RepoConfig(context.Background(), c, &vcs.PR{}, ov)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Profile != config.Chill || rc.MaxComments != 5 || rc.MinSeverity != "major" {
		t.Errorf("overrides should win over the repository file: %s %d %s", rc.Profile, rc.MaxComments, rc.MinSeverity)
	}
	if rc.Language != "German" || rc.Poem == nil || !*rc.Poem {
		t.Errorf("unset levels should fall through: language %q poem %v", rc.Language, rc.Poem)
	}

	// The flags, as the review command sets them, win over the overrides.
	r.Override = func(rc *config.RepoConfig) {
		rc.Profile, rc.MaxComments, rc.MinSeverity = config.Assertive, 30, "minor"
	}
	rc, _ = r.RepoConfig(context.Background(), c, &vcs.PR{}, ov)
	if rc.Profile != config.Assertive || rc.MaxComments != 30 || rc.MinSeverity != "minor" {
		t.Errorf("flags should win over the overrides: %s %d %s", rc.Profile, rc.MaxComments, rc.MinSeverity)
	}
}
