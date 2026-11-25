package config

import (
	"fmt"
	"strings"
)

// Approve configures approving pull requests (issue #40). The rule comes
// from the operator config (review.approve) and from the console's
// CODEREVIEW_OVERRIDES (issue #42); --approve turns it on or off for one
// run. A repository's .codereview.yaml cannot set it.
type Approve struct {
	// Enabled approves PRs that pass (with --post). Default false.
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
	// Block is the lowest severity that blocks approval: critical, major,
	// minor, nitpick, or none (approve whatever the review found). Empty:
	// unset (default major).
	Block string `yaml:"block" json:"block,omitempty"`
	// MinScore, 1 to 100, also needs every score at least this high; 0
	// ignores the scores. Nil: unset (default 0).
	MinScore *int `yaml:"min_score" json:"min_score,omitempty"`
	// Withdraw removes the bot's earlier approval when a review doesn't
	// pass or can't judge a new commit. Default true.
	Withdraw *bool `yaml:"withdraw" json:"withdraw,omitempty"`
}

// Merge returns a with the set fields of o written over it.
func (a Approve) Merge(o *Approve) Approve {
	if o == nil {
		return a
	}
	if o.Enabled != nil {
		a.Enabled = o.Enabled
	}
	if o.Block != "" {
		a.Block = o.Block
	}
	if o.MinScore != nil {
		a.MinScore = o.MinScore
	}
	if o.Withdraw != nil {
		a.Withdraw = o.Withdraw
	}
	return a
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

// Check validates the rule; key names it in errors (e.g. "review.approve").
func (a Approve) Check(key string) []error {
	var errs []error
	if err := CheckSeverity(key+".block", a.Block); err != nil {
		errs = append(errs, err)
	}
	if a.MinScore != nil && (*a.MinScore < 0 || *a.MinScore > 100) {
		errs = append(errs, fmt.Errorf("%s.min_score %d: want 0 to 100", key, *a.MinScore))
	}
	return errs
}
