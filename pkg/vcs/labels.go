package vcs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Label is a label a project has.
type Label struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Labeler reads and sets a PR's labels (issue #35). GitLab and GitHub
// implement it; Bitbucket Cloud pull requests have no labels. It is a
// separate interface so Client stays as it was.
type Labeler interface {
	// AvailableLabels are the labels the PR may use: the project's and
	// its groups' on GitLab, the repository's on GitHub.
	AvailableLabels(ctx context.Context, pr *PR) ([]Label, error)
	// PRLabels are the PR's current labels.
	PRLabels(ctx context.Context, pr *PR) ([]string, error)
	// EditLabels adds and removes labels on the PR. Callers pass only
	// labels from AvailableLabels: both hosts would create a missing one.
	EditLabels(ctx context.Context, pr *PR, add, remove []string) error
}

// maxAvailableLabels bounds what is read (and sent to the model).
const maxAvailableLabels = 500

func (g *GitLabClient) AvailableLabels(ctx context.Context, pr *PR) ([]Label, error) {
	var out []Label
	seen := map[string]bool{}
	err := g.pages(ctx, fmt.Sprintf("/projects/%s/labels?include_ancestor_groups=true", proj(pr.Repo)), func(raw []byte) error {
		var page []Label
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, l := range page {
			if l.Name != "" && !seen[l.Name] && len(out) < maxAvailableLabels {
				seen[l.Name] = true
				out = append(out, l)
			}
		}
		return nil
	})
	return out, err
}

func (g *GitLabClient) PRLabels(ctx context.Context, pr *PR) ([]string, error) {
	var mr struct {
		Labels []string `json:"labels"`
	}
	if _, err := g.r.do(ctx, "GET", g.mrPath(pr.Ref), nil, &mr); err != nil {
		return nil, err
	}
	return mr.Labels, nil
}

func (g *GitLabClient) EditLabels(ctx context.Context, pr *PR, add, remove []string) error {
	if len(add)+len(remove) == 0 {
		return nil
	}
	body := map[string]string{}
	if len(add) > 0 {
		body["add_labels"] = strings.Join(add, ",")
	}
	if len(remove) > 0 {
		body["remove_labels"] = strings.Join(remove, ",")
	}
	_, err := g.r.do(ctx, "PUT", g.mrPath(pr.Ref), body, nil)
	return err
}

func (g *GitHubClient) AvailableLabels(ctx context.Context, pr *PR) ([]Label, error) {
	var out []Label
	err := g.pages(ctx, fmt.Sprintf("/repos/%s/labels?per_page=100", repoPath(pr.Repo)), func(raw []byte) error {
		var page []Label
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, l := range page {
			if l.Name != "" && len(out) < maxAvailableLabels {
				out = append(out, l)
			}
		}
		return nil
	})
	return out, err
}

func (g *GitHubClient) PRLabels(ctx context.Context, pr *PR) ([]string, error) {
	var current []Label
	if _, err := g.r.do(ctx, "GET", fmt.Sprintf("/repos/%s/issues/%d/labels?per_page=100", repoPath(pr.Repo), pr.Number), nil, &current); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(current))
	for _, l := range current {
		out = append(out, l.Name)
	}
	return out, nil
}

func (g *GitHubClient) EditLabels(ctx context.Context, pr *PR, add, remove []string) error {
	base := fmt.Sprintf("/repos/%s/issues/%d/labels", repoPath(pr.Repo), pr.Number)
	for _, l := range remove {
		if _, err := g.r.do(ctx, "DELETE", base+"/"+url.PathEscape(l), nil, nil); err != nil && statusOf(err) != http.StatusNotFound {
			return fmt.Errorf("remove label %s: %w", l, err)
		}
	}
	if len(add) > 0 {
		if _, err := g.r.do(ctx, "POST", base, map[string][]string{"labels": add}, nil); err != nil {
			return err
		}
	}
	return nil
}

// IsScoreLabel reports whether name is one of the bot's own score labels
// ("Quality Score::85" on GitLab, "Quality Score: 85" on GitHub).
func IsScoreLabel(name string) bool {
	return strings.Contains(name, " Score::") || strings.Contains(name, " Score: ")
}
