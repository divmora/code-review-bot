package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Approver approves a pull/merge request as the bot, and withdraws that
// approval. It is separate from Client so code implementing Client
// elsewhere keeps compiling; every client in this package implements it.
type Approver interface {
	// Approve approves the PR's current head as the bot. changed is false
	// when the bot had already approved it.
	Approve(ctx context.Context, pr *PR, body string) (changed bool, err error)
	// Unapprove withdraws the bot's approval. changed is false when the
	// bot had none.
	Unapprove(ctx context.Context, pr *PR, reason string) (changed bool, err error)
}

// ApprovalMarker marks the bot's approving GitHub reviews, so it finds its
// own approval whatever account it posts as (a user token or a GitHub App).
const ApprovalMarker = "<!-- codereview:approval -->"

// ---- GitLab: approve / unapprove as the token's user ----

func (g *GitLabClient) approvedBySelf(ctx context.Context, pr *PR) (bool, error) {
	var me struct {
		ID int64 `json:"id"`
	}
	if _, err := g.r.do(ctx, "GET", "/user", nil, &me); err != nil {
		return false, fmt.Errorf("token user: %w", err)
	}
	var v struct {
		ApprovedBy []struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if _, err := g.r.do(ctx, "GET", g.mrPath(pr.Ref)+"/approvals", nil, &v); err != nil {
		return false, err
	}
	for _, a := range v.ApprovedBy {
		if a.User.ID == me.ID {
			return true, nil
		}
	}
	return false, nil
}

// Approve approves the merge request. GitLab has no approval message, so
// body is not used (the summary comment says why).
func (g *GitLabClient) Approve(ctx context.Context, pr *PR, _ string) (bool, error) {
	done, err := g.approvedBySelf(ctx, pr)
	if err != nil || done {
		return false, err
	}
	in := map[string]string{}
	if pr.HeadSHA != "" {
		in["sha"] = pr.HeadSHA // fails if a newer push landed meanwhile
	}
	if _, err := g.r.do(ctx, "POST", g.mrPath(pr.Ref)+"/approve", in, nil); err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	return true, nil
}

func (g *GitLabClient) Unapprove(ctx context.Context, pr *PR, _ string) (bool, error) {
	done, err := g.approvedBySelf(ctx, pr)
	if err != nil || !done {
		return false, err
	}
	if _, err := g.r.do(ctx, "POST", g.mrPath(pr.Ref)+"/unapprove", nil, nil); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("unapprove: %w", err)
	}
	return true, nil
}

// ---- GitHub: an approving review, found again by its marker ----

type ghReview struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	Body     string `json:"body"`
	CommitID string `json:"commit_id"`
}

// botApprovals lists the bot's reviews that still approve the PR.
func (g *GitHubClient) botApprovals(ctx context.Context, pr *PR) ([]ghReview, error) {
	var out []ghReview
	err := g.pages(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repoPath(pr.Repo), pr.Number), func(page []byte) error {
		var rs []ghReview
		if err := json.Unmarshal(page, &rs); err != nil {
			return err
		}
		for _, r := range rs {
			if r.State == "APPROVED" && strings.Contains(r.Body, ApprovalMarker) {
				out = append(out, r)
			}
		}
		return nil
	})
	return out, err
}

func (g *GitHubClient) Approve(ctx context.Context, pr *PR, body string) (bool, error) {
	approvals, err := g.botApprovals(ctx, pr)
	if err != nil {
		return false, err
	}
	for _, r := range approvals {
		if pr.HeadSHA == "" || r.CommitID == pr.HeadSHA {
			return false, nil
		}
	}
	in := map[string]any{"event": "APPROVE", "body": strings.TrimSpace(body + "\n\n" + ApprovalMarker)}
	if pr.HeadSHA != "" {
		in["commit_id"] = pr.HeadSHA
	}
	if _, err := g.r.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/reviews", repoPath(pr.Repo), pr.Number), in, nil); err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	return true, nil
}

// Unapprove dismisses the bot's approving reviews. Dismissing needs write
// access, and branch protection may limit who can dismiss reviews.
func (g *GitHubClient) Unapprove(ctx context.Context, pr *PR, reason string) (bool, error) {
	approvals, err := g.botApprovals(ctx, pr)
	if err != nil {
		return false, err
	}
	for _, r := range approvals {
		path := fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/dismissals", repoPath(pr.Repo), pr.Number, r.ID)
		if _, err := g.r.do(ctx, "PUT", path, map[string]string{"message": reason, "event": "DISMISS"}, nil); err != nil {
			return false, fmt.Errorf("dismiss approval: %w", err)
		}
	}
	return len(approvals) > 0, nil
}

// ---- Bitbucket: approve / unapprove as the token's account ----

func (b *BitbucketClient) Approve(ctx context.Context, pr *PR, _ string) (bool, error) {
	if _, err := b.r.do(ctx, "POST", b.prPath(pr.Ref)+"/approve", nil, nil); err != nil {
		if statusOf(err) == http.StatusConflict { // already approved
			return false, nil
		}
		return false, fmt.Errorf("approve: %w", err)
	}
	return true, nil
}

func (b *BitbucketClient) Unapprove(ctx context.Context, pr *PR, _ string) (bool, error) {
	if _, err := b.r.do(ctx, "DELETE", b.prPath(pr.Ref)+"/approve", nil, nil); err != nil {
		if s := statusOf(err); s == http.StatusNotFound || s == http.StatusBadRequest { // nothing to withdraw
			return false, nil
		}
		return false, fmt.Errorf("unapprove: %w", err)
	}
	return true, nil
}

// ---- Local reviews can't approve ----

func (l *LocalClient) Approve(context.Context, *PR, string) (bool, error) { return false, errLocalPost }
func (l *LocalClient) Unapprove(context.Context, *PR, string) (bool, error) {
	return false, errLocalPost
}

var (
	_ Approver = (*GitLabClient)(nil)
	_ Approver = (*GitHubClient)(nil)
	_ Approver = (*BitbucketClient)(nil)
	_ Approver = (*LocalClient)(nil)
)
