package vcs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// TokenSource yields a bearer token (static PAT or GitHub App installation).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed token.
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) {
	if s == "" {
		return "", fmt.Errorf("no GitHub token configured (set GITHUB_TOKEN or github.token / GitHub App credentials)")
	}
	return string(s), nil
}

// GitHubClient talks to the GitHub REST API (github.com or GHES).
type GitHubClient struct {
	r *rest
}

// NewGitHub creates a client. apiURL is https://api.github.com or
// https://<ghes-host>/api/v3.
func NewGitHub(apiURL string, tokens TokenSource) *GitHubClient {
	return &GitHubClient{r: &rest{
		base:   strings.TrimRight(apiURL, "/"),
		hc:     &http.Client{Timeout: 60 * time.Second},
		name:   "github",
		accept: "application/vnd.github+json",
		auth: func(ctx context.Context, req *http.Request) error {
			t, err := tokens.Token(ctx)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+t)
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			return nil
		},
	}}
}

func (g *GitHubClient) Platform() string { return GitHub }

func repoPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func (g *GitHubClient) GetPR(ctx context.Context, ref Ref) (*PR, error) {
	var v struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
		State   string `json:"state"` // open, closed
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	}
	if _, err := g.r.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repoPath(ref.Repo), ref.Number), nil, &v); err != nil {
		return nil, err
	}
	state := v.State
	if v.Merged {
		state = StateMerged
	}
	return &PR{
		Ref: ref, Title: v.Title, Body: v.Body, Author: v.User.Login, Draft: v.Draft, URL: v.HTMLURL,
		BaseBranch: v.Base.Ref, HeadBranch: v.Head.Ref, BaseSHA: v.Base.SHA, HeadSHA: v.Head.SHA, State: state,
	}, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// pages follows Link: rel="next" pagination.
func (g *GitHubClient) pages(ctx context.Context, path string, each func(page []byte) error) error {
	next := path
	for i := 0; next != "" && i < 100; i++ {
		var raw []byte
		h, err := g.r.do(ctx, "GET", next, nil, &raw)
		if err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return err
		}
		next = ""
		if m := linkNext.FindStringSubmatch(h.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return nil
}

func (g *GitHubClient) Files(ctx context.Context, pr *PR) ([]diff.File, error) {
	var out []diff.File
	err := g.pages(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repoPath(pr.Repo), pr.Number), func(raw []byte) error {
		var page []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Status           string `json:"status"`
			Patch            string `json:"patch"`
			Changes          int    `json:"changes"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return err
		}
		for _, f := range page {
			df := diff.File{Path: f.Filename, OldPath: firstNonEmpty(f.PreviousFilename, f.Filename)}
			switch f.Status {
			case "added":
				df.Status = diff.Added
			case "removed":
				df.Status = diff.Deleted
			case "renamed":
				df.Status = diff.Renamed
			default:
				df.Status = diff.Modified
			}
			if f.Patch == "" && f.Changes > 0 {
				// GitHub omits patches for binary and very large files.
				df.Truncated = true
			}
			if f.Patch != "" {
				h, err := diff.ParsePatch(f.Patch)
				if err != nil {
					return fmt.Errorf("parse patch for %s: %w", f.Filename, err)
				}
				df.Hunks = h
			}
			out = append(out, df)
		}
		return nil
	})
	return out, err
}

func (g *GitHubClient) FileContent(ctx context.Context, pr *PR, path, ref string) ([]byte, error) {
	r := *g.r
	r.accept = "application/vnd.github.raw+json"
	var raw []byte
	escaped := make([]string, 0)
	for _, seg := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(seg))
	}
	_, err := r.do(ctx, "GET", fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repoPath(pr.Repo), strings.Join(escaped, "/"), url.QueryEscape(ref)), nil, &raw)
	return raw, err
}

func (g *GitHubClient) ChangedPaths(ctx context.Context, pr *PR, from, to string) ([]string, error) {
	var v struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if _, err := g.r.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=300", repoPath(pr.Repo), url.PathEscape(from), url.PathEscape(to)), nil, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Files))
	for _, f := range v.Files {
		out = append(out, f.Filename)
	}
	return out, nil
}

func (g *GitHubClient) BotState(ctx context.Context, pr *PR) (*BotState, error) {
	st := &BotState{Fingerprints: map[string]bool{}}
	type comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	err := g.pages(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repoPath(pr.Repo), pr.Number), func(raw []byte) error {
		var page []comment
		if err := jsonUnmarshal(raw, &page); err != nil {
			return err
		}
		for _, c := range page {
			if isSummary(c.Body) {
				parseSummary(fmt.Sprint(c.ID), c.Body, st)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = g.pages(ctx, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100", repoPath(pr.Repo), pr.Number), func(raw []byte) error {
		var page []struct {
			Body         string `json:"body"`
			Path         string `json:"path"`
			Line         *int   `json:"line"`
			OriginalLine *int   `json:"original_line"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return err
		}
		for _, c := range page {
			collectFingerprints(c.Body, st.Fingerprints)
			line := 0
			if c.Line != nil {
				line = *c.Line
			} else if c.OriginalLine != nil {
				line = *c.OriginalLine
			}
			if pc, ok := parsePosted(c.Path, line, c.Body); ok {
				st.Comments = append(st.Comments, pc)
			}
		}
		return nil
	})
	return st, err
}

type ghReviewComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

func toGH(c Comment) ghReviewComment {
	gc := ghReviewComment{Path: c.Path, Body: c.Body, Line: c.Line, Side: "RIGHT"}
	if c.StartLine > 0 && c.StartLine < c.Line {
		gc.StartLine, gc.StartSide = c.StartLine, "RIGHT"
	}
	return gc
}

func (g *GitHubClient) PostInline(ctx context.Context, pr *PR, header string, comments []Comment) ([]Comment, error) {
	if len(comments) == 0 {
		return nil, nil
	}
	gcs := make([]ghReviewComment, len(comments))
	for i, c := range comments {
		gcs[i] = toGH(c)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repoPath(pr.Repo), pr.Number)
	_, err := g.r.do(ctx, "POST", path, map[string]any{
		"commit_id": pr.HeadSHA, "event": "COMMENT", "body": header, "comments": gcs,
	}, nil)
	if err == nil {
		return nil, nil
	}
	if statusOf(err) != http.StatusUnprocessableEntity {
		return nil, err
	}
	// One bad position fails the whole review; post one by one instead.
	var rejected []Comment
	single := fmt.Sprintf("/repos/%s/pulls/%d/comments", repoPath(pr.Repo), pr.Number)
	for i, c := range comments {
		body := map[string]any{"commit_id": pr.HeadSHA, "path": gcs[i].Path, "body": gcs[i].Body, "line": gcs[i].Line, "side": "RIGHT"}
		if gcs[i].StartLine > 0 {
			body["start_line"], body["start_side"] = gcs[i].StartLine, "RIGHT"
		}
		if _, err := g.r.do(ctx, "POST", single, body, nil); err != nil {
			if statusOf(err) == http.StatusUnprocessableEntity {
				rejected = append(rejected, c)
				continue
			}
			return rejected, err
		}
	}
	return rejected, nil
}

func (g *GitHubClient) UpsertSummary(ctx context.Context, pr *PR, st *BotState, body string) error {
	if st != nil && st.SummaryID != "" {
		_, err := g.r.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/comments/%s", repoPath(pr.Repo), st.SummaryID), map[string]string{"body": body}, nil)
		if err == nil || statusOf(err) != http.StatusNotFound {
			return err
		}
	}
	return g.PostComment(ctx, pr, body)
}

func (g *GitHubClient) PostComment(ctx context.Context, pr *PR, body string) error {
	_, err := g.r.do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repoPath(pr.Repo), pr.Number), map[string]string{"body": body}, nil)
	return err
}

func (g *GitHubClient) React(ctx context.Context, pr *PR, commentID, emoji string) error {
	_, err := g.r.do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/comments/%s/reactions", repoPath(pr.Repo), url.PathEscape(commentID)), map[string]string{"content": emoji}, nil)
	return err
}

// CurrentUser returns the login of the token's user.
func (g *GitHubClient) CurrentUser(ctx context.Context) (string, error) {
	var v struct {
		Login string `json:"login"`
	}
	if _, err := g.r.do(ctx, "GET", "/user", nil, &v); err != nil {
		return "", err
	}
	return v.Login, nil
}

func (g *GitHubClient) SetScoreLabels(ctx context.Context, pr *PR, scores []ScoreLabel) error {
	base := "/repos/" + repoPath(pr.Repo)
	var current []struct {
		Name string `json:"name"`
	}
	if _, err := g.r.do(ctx, "GET", fmt.Sprintf("%s/issues/%d/labels?per_page=100", base, pr.Number), nil, &current); err != nil {
		return err
	}
	names := make([]string, len(current))
	for i, l := range current {
		names[i] = l.Name
	}
	for _, stale := range staleScoreLabels(names, scores, GitHubScoreLabel) {
		if _, err := g.r.do(ctx, "DELETE", fmt.Sprintf("%s/issues/%d/labels/%s", base, pr.Number, url.PathEscape(stale)), nil, nil); err != nil && statusOf(err) != http.StatusNotFound {
			return fmt.Errorf("remove label %s: %w", stale, err)
		}
	}
	var add []string
	for _, sc := range scores {
		name := GitHubScoreLabel(sc.Name, sc.Value)
		add = append(add, name)
		if _, err := g.r.do(ctx, "POST", base+"/labels", map[string]string{"name": name, "color": sc.Color}, nil); err != nil && statusOf(err) != http.StatusUnprocessableEntity {
			return fmt.Errorf("create label %s: %w", name, err)
		}
	}
	_, err := g.r.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/labels", base, pr.Number), map[string][]string{"labels": add}, nil)
	return err
}

func (g *GitHubClient) UpdatePR(ctx context.Context, pr *PR, title, description *string) error {
	body := map[string]string{}
	if title != nil {
		body["title"] = *title
	}
	if description != nil {
		body["body"] = *description
	}
	if len(body) == 0 {
		return nil
	}
	_, err := g.r.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/pulls/%d", repoPath(pr.Repo), pr.Number), body, nil)
	return err
}

func (g *GitHubClient) SetCommitStatus(ctx context.Context, pr *PR, sha string, st CommitStatus) error {
	state := map[string]string{StatusRunning: "pending", StatusSuccess: "success", StatusFailed: "failure"}[st.State]
	if state == "" {
		return fmt.Errorf("unknown status %q", st.State)
	}
	body := map[string]string{"state": state, "context": CommitStatusName, "description": trimStatus(st.Description)}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	_, err := g.r.do(ctx, "POST", fmt.Sprintf("/repos/%s/statuses/%s", repoPath(pr.Repo), url.PathEscape(sha)), body, nil)
	return err
}
