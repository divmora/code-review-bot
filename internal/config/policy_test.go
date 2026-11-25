package config

import (
	"strings"
	"testing"
)

func TestRepoPolicy(t *testing.T) {
	on := true
	defaults := RepoConfig{Profile: Balanced, FailOn: "major", CommitStatus: &on}
	rc, err := ParseRepoConfig([]byte("score_labels: true\ncommit_status: false\nfail_on: Minor\n"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if rc.ScoreLabels == nil || !*rc.ScoreLabels || rc.CommitStatus == nil || *rc.CommitStatus || rc.FailOn != "minor" || len(rc.Ignored) != 0 {
		t.Errorf("repo settings not applied: %+v", rc)
	}

	// A repository can't loosen the operator's fail_on or turn it off.
	for _, v := range []string{"critical", "none"} {
		rc, err := ParseRepoConfig([]byte("fail_on: "+v+"\n"), defaults)
		if err != nil {
			t.Fatal(err)
		}
		if rc.FailOn != "major" || len(rc.Ignored) != 1 || !strings.Contains(rc.Ignored[0], "fail_on") {
			t.Errorf("fail_on %s: got %q, ignored %v", v, rc.FailOn, rc.Ignored)
		}
	}
	// Without an operator fail_on, the repository sets its own.
	if rc, _ := ParseRepoConfig([]byte("fail_on: critical\n"), RepoConfig{Profile: Balanced}); rc.FailOn != "critical" {
		t.Errorf("fail_on with no operator value = %q", rc.FailOn)
	}
	if _, err := ParseRepoConfig([]byte("fail_on: high\n"), defaults); err == nil {
		t.Error("an unknown fail_on should fail")
	}

	// Approvals are operator-only: the key is reported and has no effect.
	rc, err = ParseRepoConfig([]byte("approve:\n  enabled: true\n  block: none\n"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Ignored) != 1 || !strings.Contains(rc.Ignored[0], "review.approve") {
		t.Errorf("approve in the repository: ignored = %v", rc.Ignored)
	}
}

func TestCheckPolicy(t *testing.T) {
	cfg, err := Strict("t", []byte("model: openai:m\nreview:\n  approve:\n    enabled: true\n    block: none\n    min_score: 70\n    withdraw: false\n  defaults:\n    fail_on: major\n    score_labels: true\n    commit_status: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Review.Approve; !*a.Enabled || a.Block != "none" || *a.MinScore != 70 || *a.Withdraw {
		t.Errorf("approve = %+v", a)
	}
	_, err = Strict("t", []byte("model: openai:m\nreview:\n  approve:\n    block: high\n    min_score: 101\n  defaults:\n    fail_on: blocker\n"))
	for _, want := range []string{"review.approve.block", "review.approve.min_score", "review.defaults.fail_on"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error about %s, got %v", want, err)
		}
	}
}

// review.commands is a deployment setting: operator config only (issue #47).
func TestCommandsSetting(t *testing.T) {
	cfg, err := Strict("t", []byte("model: openai:m\nreview:\n  commands: true\n"))
	if err != nil || !cfg.Review.Commands {
		t.Fatalf("review.commands: %v %v", cfg.Review.Commands, err)
	}
	if cfg, _ := Strict("t", []byte("model: openai:m\n")); cfg.Review.Commands {
		t.Error("commands should be off by default")
	}
	rc, err := ParseRepoConfig([]byte("commands: true\n"), RepoConfig{Profile: Balanced})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Ignored) != 1 || !strings.Contains(rc.Ignored[0], "review.commands") {
		t.Errorf("commands in the repository: ignored = %v", rc.Ignored)
	}
}

// A repository's learnings reach the review, after the operator's and
// before the console's overrides (issue #53).
func TestRepoLearnings(t *testing.T) {
	defaults := RepoConfig{Profile: Balanced, Learnings: []Learning{{Text: "operator"}}}
	rc, err := ParseRepoConfig([]byte("learnings:\n  - text: repository\n    path: \"legacy/**\"\n"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	ov := &Overrides{Learnings: []Learning{{Text: "console"}}}
	ov.Apply(&rc)
	var got []string
	for _, l := range rc.Learnings {
		got = append(got, l.Text+"|"+l.Path)
	}
	if strings.Join(got, ",") != "operator|,repository|legacy/**,console|" {
		t.Errorf("learnings = %v", got)
	}
	if b := rc.LearningsBlock(); !strings.Contains(b, "- [legacy/**] repository") {
		t.Errorf("the repository's learning should be in the prompt block:\n%s", b)
	}
	if len(defaults.Learnings) != 1 {
		t.Errorf("the operator's defaults must not change: %v", defaults.Learnings)
	}

	// The prompt block still caps the total.
	var many strings.Builder
	many.WriteString("learnings:\n")
	for i := 0; i < MaxLearnings+20; i++ {
		many.WriteString("  - text: rule\n")
	}
	rc, _ = ParseRepoConfig([]byte(many.String()), defaults)
	if n := strings.Count(rc.LearningsBlock(), "\n"); n != MaxLearnings {
		t.Errorf("prompt block has %d learnings, want the cap %d", n, MaxLearnings)
	}
}
