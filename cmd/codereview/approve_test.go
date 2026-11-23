package main

import (
	"testing"

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
