package config

import (
	"fmt"
	"strings"
)

// Approve configures approving pull requests, as --approve and its flags do
// (issue #40). It lives in the operator config (review.approve) only: a
// repository's .codereview.yaml cannot turn approvals on or loosen them.
type Approve struct {
	// Enabled approves PRs that pass (with --post). Default false.
	Enabled *bool `yaml:"enabled"`
	// Block is the lowest severity that blocks approval: critical, major,
	// minor, nitpick, or none (approve whatever the review found).
	// Default major.
	Block string `yaml:"block"`
	// MinScore, 1 to 100, also needs every score at least this high; 0
	// ignores the scores.
	MinScore int `yaml:"min_score"`
	// Withdraw removes the bot's earlier approval when a review doesn't
	// pass or can't judge a new commit. Default true.
	Withdraw *bool `yaml:"withdraw"`
}

// severityRank orders fail_on and approve.block values; "" and "none"
// rank 0 (off).
var severityRank = map[string]int{"": 0, "none": 0, "nitpick": 1, "minor": 2, "major": 3, "critical": 4}

// NormSeverity lowercases and trims a fail_on or approve.block value.
func NormSeverity(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// CheckSeverity validates a fail_on or approve.block value ("" is unset).
func CheckSeverity(key, s string) error {
	if _, ok := severityRank[NormSeverity(s)]; !ok {
		return fmt.Errorf("%s %q: want critical, major, minor, nitpick or none", key, s)
	}
	return nil
}

// failOnAllowed reports whether a repository's fail_on may replace the
// operator's: it may set one where the operator has none, and otherwise
// only make it stricter (a lower severity), never turn it off.
func failOnAllowed(repo, operator string) bool {
	o := severityRank[NormSeverity(operator)]
	if o == 0 {
		return true
	}
	r := severityRank[NormSeverity(repo)]
	return r > 0 && r <= o
}

func (a Approve) check() []error {
	var errs []error
	if err := CheckSeverity("review.approve.block", a.Block); err != nil {
		errs = append(errs, err)
	}
	if a.MinScore < 0 || a.MinScore > 100 {
		errs = append(errs, fmt.Errorf("review.approve.min_score %d: want 0 to 100", a.MinScore))
	}
	return errs
}
