package vcs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// GitLabClient talks to the GitLab REST API v4 (gitlab.com or self-managed).
type GitLabClient struct {
	r    *rest
	self selfCache // the token user's ID
	stateKey
}

// NewGitLab creates a client for baseURL (e.g. https://gitlab.example.com).
func NewGitLab(baseURL, token string, skipTLSVerify bool) *GitLabClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if skipTLSVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed self-managed instances
	}
	return &GitLabClient{r: &rest{
		base: strings.TrimRight(baseURL, "/") + "/api/v4",
		hc:   &http.Client{Timeout: 60 * time.Second, Transport: tr},
		name: "gitlab",
		auth: func(_ context.Context, req *http.Request) error {
			if token == "" {
				return errors.New("no GitLab token configured (set GITLAB_TOKEN or gitlab[].token)")
			}
			req.Header.Set("PRIVATE-TOKEN", token)
			return nil
		},
	}}
}

func (g *GitLabClient) Platform() string { return GitLab }

func proj(repo string) string { return url.PathEscape(repo) }

func (g *GitLabClient) mrPath(pr Ref) string {
	return fmt.Sprintf("/projects/%s/merge_requests/%d", proj(pr.Repo), pr.Number)
}

func (g *GitLabClient) GetPR(ctx context.Context, ref Ref) (*PR, error) {
	var v struct {
		Title          string `json:"title"`
		Description    string `json:"description"`
		Draft          bool   `json:"draft"`
		WorkInProgress bool   `json:"work_in_progress"`
		State          string `json:"state"` // opened, closed, locked, merged
		WebURL         string `json:"web_url"`
		SourceBranch   string `json:"source_branch"`
		TargetBranch   string `json:"target_branch"`
		SHA            string `json:"sha"`
		Author         struct {
			Username string `json:"username"`
		} `json:"author"`
		DiffRefs struct {
			BaseSHA  string `json:"base_sha"`
			HeadSHA  string `json:"head_sha"`
			StartSHA string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	// diff_refs can lag behind a fresh push; retry briefly until it is set.
	for attempt := 0; ; attempt++ {
		if _, err := g.r.do(ctx, "GET", g.mrPath(ref), nil, &v); err != nil {
			return nil, err
		}
		if v.DiffRefs.HeadSHA != "" && (v.SHA == "" || v.DiffRefs.HeadSHA == v.SHA) || attempt >= 5 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return &PR{
		Ref: ref, Title: v.Title, Body: v.Description, Author: v.Author.Username,
		Draft: v.Draft || v.WorkInProgress, URL: v.WebURL,
		BaseBranch: v.TargetBranch, HeadBranch: v.SourceBranch,
		BaseSHA: v.DiffRefs.BaseSHA, HeadSHA: firstNonEmpty(v.DiffRefs.HeadSHA, v.SHA), StartSHA: v.DiffRefs.StartSHA,
		State: map[string]string{"opened": StateOpen, "locked": StateOpen, "closed": StateClosed, "merged": StateMerged}[v.State],
	}, nil
}

// pages follows X-Next-Page pagination.
func (g *GitLabClient) pages(ctx context.Context, path string, each func([]byte) error) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	page := "1"
	for i := 0; page != "" && i < 200; i++ {
		var raw []byte
		h, err := g.r.do(ctx, "GET", path+sep+"per_page=100&page="+page, nil, &raw)
		if err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return err
		}
		page = h.Get("X-Next-Page")
	}
	return nil
}

type glDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Diff        string `json:"diff"`
	TooLarge    bool   `json:"too_large"`
	Collapsed   bool   `json:"collapsed"`
}

func (d glDiff) toFile() (diff.File, error) {
	f := diff.File{Path: d.NewPath, OldPath: d.OldPath, Status: diff.Modified}
	switch {
	case d.NewFile:
		f.Status = diff.Added
	case d.DeletedFile:
		f.Status = diff.Deleted
	case d.RenamedFile:
		f.Status = diff.Renamed
	}
	if strings.HasPrefix(d.Diff, "Binary files") {
		f.Binary = true
		return f, nil
	}
	if d.Diff == "" && (d.TooLarge || d.Collapsed) {
		f.Truncated = true
		return f, nil
	}
	h, err := diff.ParsePatch(d.Diff)
	if err != nil {
		return f, fmt.Errorf("parse diff for %s: %w", d.NewPath, err)
	}
	f.Hunks = h
	return f, nil
}

func (g *GitLabClient) Files(ctx context.Context, pr *PR) ([]diff.File, error) {
	var out []diff.File
	err := g.pages(ctx, g.mrPath(pr.Ref)+"/diffs", func(raw []byte) error {
		var page []glDiff
		if err := jsonUnmarshal(raw, &page); err != nil {
			return err
		}
		for _, d := range page {
			f, err := d.toFile()
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return nil
	})
	if err == nil || !errors.Is(err, ErrNotFound) {
		return out, err
	}
	// GitLab < 15.7 has no /diffs; fall back to /changes.
	var v struct {
		Changes []glDiff `json:"changes"`
	}
	if _, err := g.r.do(ctx, "GET", g.mrPath(pr.Ref)+"/changes?access_raw_diffs=true", nil, &v); err != nil {
		return nil, err
	}
	out = out[:0]
	for _, d := range v.Changes {
		f, err := d.toFile()
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func (g *GitLabClient) FileContent(ctx context.Context, pr *PR, path, ref string) ([]byte, error) {
	var raw []byte
	_, err := g.r.do(ctx, "GET", fmt.Sprintf("/projects/%s/repository/files/%s/raw?ref=%s", proj(pr.Repo), url.PathEscape(path), url.QueryEscape(ref)), nil, &raw)
	return raw, err
}

func (g *GitLabClient) ChangedPaths(ctx context.Context, pr *PR, from, to string) ([]string, error) {
	var v struct {
		Diffs []glDiff `json:"diffs"`
	}
	if _, err := g.r.do(ctx, "GET", fmt.Sprintf("/projects/%s/repository/compare?from=%s&to=%s&straight=true", proj(pr.Repo), url.QueryEscape(from), url.QueryEscape(to)), nil, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Diffs))
	for _, d := range v.Diffs {
		out = append(out, d.NewPath)
	}
	return out, nil
}

type glNote struct {
	ID     int64 `json:"id"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
	Body     string `json:"body"`
	System   bool   `json:"system"`
	Resolved bool   `json:"resolved"`
	Position *struct {
		NewPath string `json:"new_path"`
		NewLine int    `json:"new_line"`
	} `json:"position"`
}

// selfID is the token user's ID: only its notes are the bot's.
func (g *GitLabClient) selfID(ctx context.Context) (string, error) {
	return g.self.get(ctx, func(ctx context.Context) (string, error) {
		var v struct {
			ID int64 `json:"id"`
		}
		if _, err := g.r.do(ctx, "GET", "/user", nil, &v); err != nil {
			return "", fmt.Errorf("read the token's user: %w", err)
		}
		return strconv.FormatInt(v.ID, 10), nil
	})
}

func (g *GitLabClient) BotState(ctx context.Context, pr *PR) (*BotState, error) {
	self, err := g.selfID(ctx)
	if err != nil {
		return nil, err
	}
	st := &BotState{Fingerprints: map[string]bool{}}
	var pick summaryPick
	err = g.pages(ctx, g.mrPath(pr.Ref)+"/discussions", func(raw []byte) error {
		var page []struct {
			Notes []glNote `json:"notes"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return err
		}
		for _, d := range page {
			for _, n := range d.Notes {
				own := strconv.FormatInt(n.Author.ID, 10) == self
				if n.System || !own && !g.signed(pr.Ref, n.Body) {
					continue // only the bot's own notes, or signed ones (issue #21)
				}
				if isSummary(n.Body) {
					pick.offer(strconv.FormatInt(n.ID, 10), n.Body, own)
				}
				collectFingerprints(n.Body, st.Fingerprints)
				if n.Position != nil {
					if pc, ok := parsePosted(n.Position.NewPath, n.Position.NewLine, n.Body); ok {
						pc.Resolved = n.Resolved
						st.Comments = append(st.Comments, pc)
					}
				}
			}
		}
		return nil
	})
	pick.apply(st)
	return st, err
}

func (g *GitLabClient) PostInline(ctx context.Context, pr *PR, _ string, comments []Comment) ([]Comment, error) {
	var rejected []Comment
	for _, c := range comments {
		pos := map[string]any{
			"position_type": "text",
			"base_sha":      pr.BaseSHA,
			"start_sha":     pr.StartSHA,
			"head_sha":      pr.HeadSHA,
			"old_path":      firstNonEmpty(c.OldPath, c.Path),
			"new_path":      c.Path,
			"new_line":      c.Line,
		}
		if c.OldLine > 0 {
			pos["old_line"] = c.OldLine
		}
		_, err := g.r.do(ctx, "POST", g.mrPath(pr.Ref)+"/discussions", map[string]any{"body": g.sign(pr.Ref, Sanitize(c.Body)), "position": pos}, nil)
		if err != nil {
			if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusUnprocessableEntity {
				rejected = append(rejected, c)
				continue
			}
			return rejected, err
		}
	}
	return rejected, nil
}

func (g *GitLabClient) UpsertSummary(ctx context.Context, pr *PR, st *BotState, body string) error {
	if st != nil && st.SummaryID != "" {
		_, err := g.r.do(ctx, "PUT", g.mrPath(pr.Ref)+"/notes/"+st.SummaryID, map[string]string{"body": g.sign(pr.Ref, Sanitize(body))}, nil)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return g.PostComment(ctx, pr, body)
}

func (g *GitLabClient) PostComment(ctx context.Context, pr *PR, body string) error {
	_, err := g.r.do(ctx, "POST", g.mrPath(pr.Ref)+"/notes", map[string]string{"body": g.sign(pr.Ref, Sanitize(body))}, nil)
	return err
}

func (g *GitLabClient) React(ctx context.Context, pr *PR, noteID, emoji string) error {
	_, err := g.r.do(ctx, "POST", g.mrPath(pr.Ref)+"/notes/"+url.PathEscape(noteID)+"/award_emoji", map[string]string{"name": emoji}, nil)
	return err
}

// CurrentUser returns the login of the token's user.
func (g *GitLabClient) CurrentUser(ctx context.Context) (string, error) {
	var v struct {
		Username string `json:"username"`
	}
	if _, err := g.r.do(ctx, "GET", "/user", nil, &v); err != nil {
		return "", err
	}
	return v.Username, nil
}

func (g *GitLabClient) SetScoreLabels(ctx context.Context, pr *PR, scores []ScoreLabel) error {
	var mr struct {
		Labels []string `json:"labels"`
	}
	if _, err := g.r.do(ctx, "GET", g.mrPath(pr.Ref), nil, &mr); err != nil {
		return err
	}
	var add []string
	for _, sc := range scores {
		name := GitLabScoreLabel(sc.Name, sc.Value)
		add = append(add, name)
		// Create it with the band colour; an existing one gets that colour
		// too, so a label made earlier (by hand, another tool) isn't left
		// in some other colour.
		_, err := g.r.do(ctx, "POST", fmt.Sprintf("/projects/%s/labels", proj(pr.Repo)), map[string]string{"name": name, "color": "#" + sc.Color}, nil)
		if err != nil {
			if s := statusOf(err); s != http.StatusConflict && s != http.StatusBadRequest {
				return fmt.Errorf("create label %s: %w", name, err)
			}
			g.recolorLabel(ctx, pr.Repo, name, sc.Color)
		}
	}
	body := map[string]string{"add_labels": strings.Join(add, ",")}
	if stale := staleScoreLabels(mr.Labels, scores, GitLabScoreLabel); len(stale) > 0 {
		body["remove_labels"] = strings.Join(stale, ",")
	}
	_, err := g.r.do(ctx, "PUT", g.mrPath(pr.Ref), body, nil)
	return err
}

// recolorLabel gives an existing project label the colour (hex, no #).
// Best effort: a group label can't be changed from the project, and a
// failure leaves the label as it was.
func (g *GitLabClient) recolorLabel(ctx context.Context, repo, name, color string) {
	path := fmt.Sprintf("/projects/%s/labels/%s", proj(repo), url.PathEscape(name))
	var l struct {
		Color          string `json:"color"`
		IsProjectLabel *bool  `json:"is_project_label"`
	}
	if _, err := g.r.do(ctx, "GET", path, nil, &l); err != nil || sameColor(l.Color, color) || (l.IsProjectLabel != nil && !*l.IsProjectLabel) {
		return
	}
	_, _ = g.r.do(ctx, "PUT", path, map[string]string{"color": "#" + color}, nil)
}

func (g *GitLabClient) UpdatePR(ctx context.Context, pr *PR, title, description *string) error {
	body := map[string]string{}
	if title != nil {
		body["title"] = *title
	}
	if description != nil {
		body["description"] = NeutraliseQuickActions(*description)
	}
	if len(body) == 0 {
		return nil
	}
	_, err := g.r.do(ctx, "PUT", g.mrPath(pr.Ref), body, nil)
	return err
}

func (g *GitLabClient) SetCommitStatus(ctx context.Context, pr *PR, sha string, st CommitStatus) error {
	state := map[string]string{StatusRunning: "running", StatusSuccess: "success", StatusFailed: "failed"}[st.State]
	if state == "" {
		return fmt.Errorf("unknown status %q", st.State)
	}
	body := map[string]string{"state": state, "name": CommitStatusName, "description": trimStatus(st.Description)}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	_, err := g.r.do(ctx, "POST", fmt.Sprintf("/projects/%s/statuses/%s", proj(pr.Repo), url.PathEscape(sha)), body, nil)
	if statusOf(err) == http.StatusBadRequest && strings.Contains(err.Error(), "Cannot transition") {
		return nil // the same state is already set
	}
	return err
}
