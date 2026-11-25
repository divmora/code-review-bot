package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/divmora/code-review-bot/pkg/vcs"
)

func ptr[T any](v T) *T { return &v }

// The config's commit_status and fail_on apply when no flag is given, and a
// given flag wins, including an explicit false or "none" (issue #40).
func TestPolicyFromConfig(t *testing.T) {
	llmSrv, _ := fakeLLM(t, `{"files":[],"comments":[]}`)
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}
	run := func(opts Options) (*Outcome, *fakeGitHub) {
		t.Helper()
		gh := &fakeGitHub{head: "head1"}
		srv := httptest.NewServer(gh.handler(t))
		t.Cleanup(srv.Close)
		r := newRunner(t, llmSrv.URL)
		r.Cfg.Review.Defaults.CommitStatus = ptr(true)
		r.Cfg.Review.Defaults.FailOn = "Minor"
		out, err := r.Review(context.Background(), vcs.NewGitHub(srv.URL, vcs.StaticToken("t")), ref, opts)
		if err != nil {
			t.Fatal(err)
		}
		return out, gh
	}

	out, gh := run(Options{Post: true})
	if len(gh.statuses) != 2 {
		t.Errorf("commit_status from the config: statuses = %v", gh.statuses)
	}
	if out.FailOn != "minor" {
		t.Errorf("fail_on from the config = %q", out.FailOn)
	}

	out, gh = run(Options{Post: true, CommitStatus: ptr(false), FailOn: ptr("none")})
	if len(gh.statuses) != 0 {
		t.Errorf("--commit-status=false should win: statuses = %v", gh.statuses)
	}
	if out.FailOn != "none" {
		t.Errorf("--fail-on none should win: %q", out.FailOn)
	}

	// Without --post nothing is written, whatever the config says.
	if _, gh = run(Options{}); len(gh.statuses) != 0 {
		t.Errorf("dry run set statuses: %v", gh.statuses)
	}
}
