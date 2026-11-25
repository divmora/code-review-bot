package main

import (
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/review"
)

func TestApprovalBlock(t *testing.T) {
	for in, want := range map[string]review.Severity{"major": review.Major, "Critical": review.Critical, " nitpick ": review.Nitpick, "minor": review.Minor, "none": ""} {
		if got, err := approvalBlock(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	// Unlike --min-severity, a typo is an error: it must not approve more
	// than intended.
	for _, in := range []string{"high", "majr", "low"} {
		if _, err := approvalBlock(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

// The rule is review.approve; a given --approve only turns it on or off
// (issues #40, #42).
func TestApprovalFrom(t *testing.T) {
	yes, no, sixty := true, false, 60
	cfg := config.Approve{Enabled: &yes, Block: "minor", MinScore: &sixty, Withdraw: &no}
	a, err := approvalFrom(cfg, false, false)
	if err != nil || a == nil || a.Block != review.Minor || a.MinScore != 60 || a.Withdraw {
		t.Fatalf("from the config: %+v %v", a, err)
	}
	if a, _ := approvalFrom(cfg, true, false); a != nil {
		t.Errorf("--approve=false should turn it off: %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{Block: "none"}, true, true); a == nil || a.Block != "" {
		t.Errorf("--approve should turn on a rule that isn't enabled: %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, true, true); a == nil || a.Block != review.Major || a.MinScore != 0 || !a.Withdraw {
		t.Errorf("--approve alone uses the defaults (major, 0, withdraw): %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, false, false); a != nil {
		t.Errorf("off by default: %+v", a)
	}
	if _, err := approvalFrom(config.Approve{Enabled: &yes, Block: "high"}, false, false); err == nil || !strings.Contains(err.Error(), "review.approve.block") {
		t.Errorf("a bad block should name the key: %v", err)
	}
}

// The rule is not part of CODEREVIEW_OVERRIDES: the console writes it into
// the job's config file.
func TestApproveNotInOverrides(t *testing.T) {
	t.Setenv("CODEREVIEW_OVERRIDES", `{"approve":{"enabled":true}}`)
	if _, err := overridesFromEnv(&config.Config{}); err == nil {
		t.Error("approve in CODEREVIEW_OVERRIDES should be rejected as an unknown field")
	}
}
