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

// The rule is review.approve with the console's overrides written over
// it; a given --approve only turns it on or off (issues #40, #42).
func TestApprovalFrom(t *testing.T) {
	yes, no, sixty, zero := true, false, 60, 0
	cfg := config.Approve{Enabled: &yes, Block: "minor", MinScore: &sixty, Withdraw: &no}
	a, err := approvalFrom(cfg, false, false)
	if err != nil || a == nil || a.Block != review.Minor || a.MinScore != 60 || a.Withdraw {
		t.Fatalf("from the config: %+v %v", a, err)
	}
	// Overrides win field by field; an explicit min_score 0 counts.
	a, err = approvalFrom(cfg.Merge(&config.Approve{Block: "none", MinScore: &zero}), false, false)
	if err != nil || a == nil || a.Block != "" || a.MinScore != 0 || a.Withdraw {
		t.Fatalf("overrides should win: %+v %v", a, err)
	}
	if a, _ := approvalFrom(cfg, true, false); a != nil {
		t.Errorf("--approve=false should turn it off: %+v", a)
	}
	if a, _ := approvalFrom(cfg.Merge(&config.Approve{Enabled: &no}), false, false); a != nil {
		t.Errorf("overrides enabled=false should turn it off: %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, true, true); a == nil || a.Block != review.Major || a.MinScore != 0 || !a.Withdraw {
		t.Errorf("--approve alone uses the defaults (major, 0, withdraw): %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, false, false); a != nil {
		t.Errorf("off by default: %+v", a)
	}
	if _, err := approvalFrom(config.Approve{Enabled: &yes, Block: "high"}, false, false); err == nil || !strings.Contains(err.Error(), "approve.block") {
		t.Errorf("a bad block should name the key: %v", err)
	}
}

// The console sends the rule in CODEREVIEW_OVERRIDES; it is validated like
// the YAML, and the removed flags are gone.
func TestApproveOverrides(t *testing.T) {
	cfg := &config.Config{}
	t.Setenv("CODEREVIEW_OVERRIDES", `{"approve":{"enabled":true,"block":"none","min_score":0,"withdraw":false}}`)
	ov, err := overridesFromEnv(cfg)
	if err != nil || ov.Approve == nil || ov.Approve.MinScore == nil || *ov.Approve.MinScore != 0 || ov.Approve.Block != "none" || *ov.Approve.Withdraw {
		t.Fatalf("overrides: %+v %v", ov, err)
	}
	for _, bad := range []string{`{"approve":{"block":"high"}}`, `{"approve":{"min_score":101}}`, `{"approve":{"blok":"major"}}`} {
		t.Setenv("CODEREVIEW_OVERRIDES", bad)
		if _, err := overridesFromEnv(cfg); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
	for _, f := range []string{"approve-block", "approve-min-score", "approve-withdraw"} {
		if boolFlags[f] {
			t.Errorf("--%s should be gone", f)
		}
	}
}
