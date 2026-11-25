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

// review.approve applies when no --approve flag is given; each given flag
// wins over its key, and --approve=false turns approvals off (issue #40).
func TestApprovalFrom(t *testing.T) {
	yes, no := true, false
	cfg := config.Approve{Enabled: &yes, Block: "minor", MinScore: 60, Withdraw: &no}
	none := map[string]bool{}
	// Flag defaults as the review command declares them.
	a, err := approvalFrom(cfg, none, false, "major", 0, true)
	if err != nil || a == nil || a.Block != review.Minor || a.MinScore != 60 || a.Withdraw {
		t.Fatalf("from the config: %+v %v", a, err)
	}
	a, err = approvalFrom(cfg, map[string]bool{"approve-block": true, "approve-min-score": true, "approve-withdraw": true}, false, "none", 0, true)
	if err != nil || a == nil || a.Block != "" || a.MinScore != 0 || !a.Withdraw {
		t.Fatalf("flags should win: %+v %v", a, err)
	}
	if a, _ := approvalFrom(cfg, map[string]bool{"approve": true}, false, "major", 0, true); a != nil {
		t.Errorf("--approve=false should turn it off: %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, map[string]bool{"approve": true}, true, "major", 0, true); a == nil || a.Block != review.Major || !a.Withdraw {
		t.Errorf("--approve alone keeps the flag defaults: %+v", a)
	}
	if a, _ := approvalFrom(config.Approve{}, none, false, "major", 0, true); a != nil {
		t.Errorf("off by default: %+v", a)
	}
	if _, err := approvalFrom(config.Approve{Enabled: &yes, Block: "high"}, none, false, "major", 0, true); err == nil || !strings.Contains(err.Error(), "review.approve.block") {
		t.Errorf("a bad config block should name the key: %v", err)
	}
}
