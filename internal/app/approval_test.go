package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

func TestApprovalDecide(t *testing.T) {
	f := func(sev review.Severity, kind string) review.Finding {
		return review.Finding{Severity: sev, Kind: kind}
	}
	reviewed := func(r review.Result) *review.Result {
		r.Reviewed = []string{"app.go"}
		return &r
	}
	scores := &review.Scores{Quality: 85, Security: 90, Maintainability: 70}
	for _, c := range []struct {
		name string
		a    Approval
		res  *review.Result
		want string
	}{
		{"clean", Approval{Block: review.Major}, reviewed(review.Result{}), verdictPass},
		{"minor only", Approval{Block: review.Major}, reviewed(review.Result{Findings: []review.Finding{f(review.Minor, ""), f(review.Nitpick, "")}}), verdictPass},
		{"a major blocks", Approval{Block: review.Major}, reviewed(review.Result{Findings: []review.Finding{f(review.Major, "")}}), verdictBlock},
		{"a critical blocks", Approval{Block: review.Major}, reviewed(review.Result{Findings: []review.Finding{f(review.Critical, "")}}), verdictBlock},
		{"beyond max_comments blocks", Approval{Block: review.Major}, reviewed(review.Result{Overflow: []review.Finding{f(review.Major, "")}}), verdictBlock},
		{"still present from before blocks", Approval{Block: review.Major}, reviewed(review.Result{Repeated: []review.Finding{f(review.Critical, "")}}), verdictBlock},
		{"questions never block", Approval{Block: review.Nitpick}, reviewed(review.Result{Findings: []review.Finding{f(review.Critical, review.KindQuestion)}}), verdictPass},
		{"nitpick blocks everything", Approval{Block: review.Nitpick}, reviewed(review.Result{Findings: []review.Finding{f(review.Nitpick, "")}}), verdictBlock},
		{"none approves anyway", Approval{}, reviewed(review.Result{Findings: []review.Finding{f(review.Critical, "")}}), verdictPass},
		{"score below the minimum", Approval{Block: review.Major, MinScore: 75}, reviewed(review.Result{Scores: scores}), verdictBlock},
		{"scores high enough", Approval{Block: review.Major, MinScore: 70}, reviewed(review.Result{Scores: scores}), verdictPass},
		{"no scores to check", Approval{Block: review.Major, MinScore: 70}, reviewed(review.Result{}), verdictUnknown},
		{"partial review", Approval{Block: review.Major}, reviewed(review.Result{Errors: []string{"batch 2 failed"}}), verdictUnknown},
		{"partial, but a blocker found", Approval{Block: review.Major}, reviewed(review.Result{Errors: []string{"x"}, Findings: []review.Finding{f(review.Major, "")}}), verdictBlock},
		{"too large", Approval{Block: review.Major}, reviewed(review.Result{TooLarge: true}), verdictUnknown},
		{"nothing reviewed", Approval{Block: review.Major}, &review.Result{}, verdictUnknown},
	} {
		if got, reason := c.a.Decide(c.res); got != c.want || reason == "" {
			t.Errorf("%s: %s (%q), want %s", c.name, got, reason, c.want)
		}
	}
}

// stubApprover is a code host that records approvals.
type stubApprover struct {
	vcs.Client
	approved bool
	fail     error
	messages []string
}

func (s *stubApprover) Approve(_ context.Context, _ *vcs.PR, body string) (bool, error) {
	if s.fail != nil {
		return false, s.fail
	}
	s.messages = append(s.messages, body)
	was := s.approved
	s.approved = true
	return !was, nil
}

func (s *stubApprover) Unapprove(_ context.Context, _ *vcs.PR, reason string) (bool, error) {
	s.messages = append(s.messages, reason)
	was := s.approved
	s.approved = false
	return was, nil
}

func TestRunnerApprove(t *testing.T) {
	ctx := context.Background()
	r := &Runner{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pr := &vcs.PR{}
	clean := &review.Result{Reviewed: []string{"a.go"}}
	blocked := &review.Result{Reviewed: []string{"a.go"}, Findings: []review.Finding{{Severity: review.Critical}}}
	partial := &review.Result{Reviewed: []string{"a.go"}, Errors: []string{"x"}}
	rule := Approval{Block: review.Major, Withdraw: true}
	host := &stubApprover{}

	if got, _ := r.approve(ctx, host, pr, clean, rule, "codereview"); got != ApprovalApproved || !host.approved || host.messages[0] != "Approved by @codereview: no findings at major or above." {
		t.Fatalf("approve: %s %+v", got, host)
	}
	if got, _ := r.approve(ctx, host, pr, clean, rule, "codereview"); got != ApprovalAlreadyApproved {
		t.Errorf("already approved: %s", got)
	}
	if got, _ := r.approve(ctx, host, pr, partial, rule, "codereview"); got != ApprovalUnchanged || !host.approved {
		t.Errorf("a partial review leaves the approval: %s", got)
	}
	if got, reason := r.approve(ctx, host, pr, blocked, rule, "codereview"); got != ApprovalWithdrawn || host.approved || reason != "1 finding(s) at major or above" {
		t.Errorf("withdraw: %s %q", got, reason)
	}
	if got, _ := r.approve(ctx, host, pr, blocked, rule, "codereview"); got != ApprovalNotApproved {
		t.Errorf("nothing to withdraw: %s", got)
	}

	// Withdraw off: an earlier approval stays.
	host = &stubApprover{approved: true}
	if got, _ := r.approve(ctx, host, pr, blocked, Approval{Block: review.Major}, "codereview"); got != ApprovalNotApproved || !host.approved {
		t.Errorf("withdraw off: %s %+v", got, host)
	}
	// A refusal from the code host is reported, not fatal.
	host = &stubApprover{fail: errors.New("403 you can't approve your own merge request")}
	if got, reason := r.approve(ctx, host, pr, clean, rule, "codereview"); got != ApprovalFailed || reason == "" {
		t.Errorf("refused: %s %q", got, reason)
	}
}
