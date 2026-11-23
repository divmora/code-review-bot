package app

import (
	"context"
	"fmt"

	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Approval decides whether the bot approves a reviewed PR (with Post).
type Approval struct {
	// Block is the lowest severity that blocks approval: any finding at
	// or above it (new, beyond max_comments, or still present from an
	// earlier review) means no approval. Empty approves whatever the
	// review found. Questions never block.
	Block review.Severity
	// MinScore, when above 0, also needs every score at least this high.
	MinScore int
	// Withdraw removes the bot's earlier approval when a review doesn't
	// pass. Without it, an earlier approval is left as is.
	Withdraw bool
}

// Approval outcomes, reported in the CLI's JSON output.
const (
	ApprovalApproved        = "approved"         // approved now
	ApprovalAlreadyApproved = "already_approved" // passes; the bot had approved this head
	ApprovalWithdrawn       = "withdrawn"        // doesn't pass; the bot's approval was removed
	ApprovalNotApproved     = "not_approved"     // doesn't pass; no approval to remove (or Withdraw is off)
	ApprovalUnchanged       = "unchanged"        // not enough to decide (partial, too large, nothing reviewed)
	ApprovalFailed          = "failed"           // the code host refused (see the logs)
)

// Verdicts of Decide.
const (
	verdictPass    = "pass"
	verdictBlock   = "block"
	verdictUnknown = "unknown"
)

// Decide applies the rule to a review result. reason says why, for the
// approval message and the logs.
func (a Approval) Decide(res *review.Result) (verdict, reason string) {
	switch {
	case res == nil, res.TooLarge:
		return verdictUnknown, "the change was too large to review"
	case len(res.Reviewed) == 0:
		return verdictUnknown, "nothing was reviewed"
	}
	if a.Block != "" {
		n := 0
		for _, list := range [][]review.Finding{res.Findings, res.Overflow, res.Repeated} {
			for _, f := range list {
				if f.Kind != review.KindQuestion && f.Severity.AtLeast(a.Block) {
					n++
				}
			}
		}
		if n > 0 {
			return verdictBlock, fmt.Sprintf("%d finding(s) at %s or above", n, a.Block)
		}
	}
	if len(res.Errors) > 0 { // part of the change wasn't reviewed
		return verdictUnknown, "part of the review failed"
	}
	if a.MinScore > 0 {
		if res.Scores == nil {
			return verdictUnknown, "the review gave no scores"
		}
		for _, n := range review.ScoreNames {
			if v := res.Scores.Get(n); v < a.MinScore {
				return verdictBlock, fmt.Sprintf("%s score %d is below %d", n, v, a.MinScore)
			}
		}
	}
	if a.Block == "" {
		return verdictPass, "reviewed"
	}
	reason = "no findings at " + string(a.Block) + " or above"
	if a.MinScore > 0 {
		reason += fmt.Sprintf(", every score at least %d", a.MinScore)
	}
	return verdictPass, reason
}

// approve applies the rule on the code host. Failures are logged and
// reported, not fatal: the review is already posted.
func (r *Runner) approve(ctx context.Context, c vcs.Client, pr *vcs.PR, res *review.Result, a Approval, bot string) (outcome, reason string) {
	verdict, reason := a.Decide(res)
	if verdict == verdictUnknown {
		r.Logger.Info("approval unchanged", "reason", reason)
		return ApprovalUnchanged, reason
	}
	ap, ok := c.(vcs.Approver)
	if !ok {
		return ApprovalFailed, "this code host can't approve"
	}
	if verdict == verdictPass {
		changed, err := ap.Approve(ctx, pr, fmt.Sprintf("Approved by @%s: %s.", bot, reason))
		switch {
		case err != nil:
			r.Logger.Warn("could not approve", "error", err)
			return ApprovalFailed, err.Error()
		case !changed:
			return ApprovalAlreadyApproved, reason
		}
		r.Logger.Info("approved", "reason", reason)
		return ApprovalApproved, reason
	}
	if !a.Withdraw {
		return ApprovalNotApproved, reason
	}
	changed, err := ap.Unapprove(ctx, pr, fmt.Sprintf("@%s withdrew its approval: %s.", bot, reason))
	switch {
	case err != nil:
		r.Logger.Warn("could not withdraw the approval", "error", err)
		return ApprovalFailed, err.Error()
	case changed:
		r.Logger.Info("approval withdrawn", "reason", reason)
		return ApprovalWithdrawn, reason
	}
	return ApprovalNotApproved, reason
}
