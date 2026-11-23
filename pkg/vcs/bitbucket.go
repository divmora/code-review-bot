package vcs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// Bitbucket is the platform name for Bitbucket Cloud (bitbucket.org).
const Bitbucket = "bitbucket"

// BitbucketClient talks to the Bitbucket Cloud REST API 2.0.
//
// Bitbucket's Markdown escapes raw HTML, so the bot's hidden markers
// (<!-- codereview:... -->) and <details> sections are rewritten into
// Bitbucket-friendly Markdown on the way out and back on the way in. The
// rest of the bot keeps working with one marker format.
type BitbucketClient struct {
	r    *rest
	self selfCache // the token account's UUID
}

// BitbucketAuth holds one of the two supported credentials:
//   - Token: a repository, project or workspace access token (Bearer);
//   - Email + APIToken: an Atlassian account API token (Basic).
type BitbucketAuth struct {
	Token    string
	Email    string
	APIToken string
}

// NewBitbucket creates a client. apiURL defaults to https://api.bitbucket.org/2.0.
func NewBitbucket(apiURL string, auth BitbucketAuth) *BitbucketClient {
	if apiURL == "" {
		apiURL = "https://api.bitbucket.org/2.0"
	}
	return &BitbucketClient{r: &rest{
		base:   strings.TrimRight(apiURL, "/"),
		hc:     &http.Client{Timeout: 60 * time.Second},
		name:   "bitbucket",
		accept: "application/json",
		auth: func(_ context.Context, req *http.Request) error {
			switch {
			case auth.Token != "":
				req.Header.Set("Authorization", "Bearer "+auth.Token)
			case auth.Email != "" && auth.APIToken != "":
				req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth.Email+":"+auth.APIToken)))
			default:
				return errors.New("no Bitbucket credentials: set BITBUCKET_TOKEN (access token) or BITBUCKET_EMAIL + BITBUCKET_API_TOKEN")
			}
			return nil
		},
	}}
}

func (b *BitbucketClient) Platform() string { return Bitbucket }

func (b *BitbucketClient) repoPath(repo string) string {
	ws, slug, _ := strings.Cut(repo, "/")
	return "/repositories/" + url.PathEscape(ws) + "/" + url.PathEscape(slug)
}

func (b *BitbucketClient) prPath(ref Ref) string {
	return fmt.Sprintf("%s/pullrequests/%d", b.repoPath(ref.Repo), ref.Number)
}

type bbCommitRef struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
}

type bbPR struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Draft       bool        `json:"draft"`
	State       string      `json:"state"`
	Source      bbCommitRef `json:"source"`
	Destination bbCommitRef `json:"destination"`
	Author      struct {
		Nickname    string `json:"nickname"`
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (b *BitbucketClient) GetPR(ctx context.Context, ref Ref) (*PR, error) {
	var v bbPR
	if _, err := b.r.do(ctx, "GET", b.prPath(ref), nil, &v); err != nil {
		return nil, err
	}
	// The PR object carries abbreviated hashes; statuses and comparisons
	// need the full SHA.
	head, err := b.fullHash(ctx, ref.Repo, v.Source.Commit.Hash)
	if err != nil {
		return nil, err
	}
	base, err := b.fullHash(ctx, ref.Repo, v.Destination.Commit.Hash)
	if err != nil {
		return nil, err
	}
	return &PR{
		Ref: ref, Title: v.Title, Body: fromBitbucketMarkdown(v.Description),
		Author: firstNonEmpty(v.Author.Nickname, v.Author.DisplayName), Draft: v.Draft,
		URL: v.Links.HTML.Href, BaseBranch: v.Destination.Branch.Name, HeadBranch: v.Source.Branch.Name,
		BaseSHA: base, HeadSHA: head,
		State: map[string]string{"OPEN": StateOpen, "MERGED": StateMerged, "DECLINED": StateClosed, "SUPERSEDED": StateClosed}[v.State],
	}, nil
}

func (b *BitbucketClient) fullHash(ctx context.Context, repo, short string) (string, error) {
	if len(short) >= 40 || short == "" {
		return short, nil
	}
	var v struct {
		Hash string `json:"hash"`
	}
	if _, err := b.r.do(ctx, "GET", b.repoPath(repo)+"/commit/"+url.PathEscape(short), nil, &v); err != nil {
		return "", fmt.Errorf("resolve commit %s: %w", short, err)
	}
	return v.Hash, nil
}

func (b *BitbucketClient) Files(ctx context.Context, pr *PR) ([]diff.File, error) {
	r := *b.r
	r.accept = "text/plain"
	var raw []byte
	if _, err := r.do(ctx, "GET", b.prPath(pr.Ref)+"/diff", nil, &raw); err != nil {
		return nil, err
	}
	return diff.ParseMulti(string(raw))
}

func (b *BitbucketClient) FileContent(ctx context.Context, pr *PR, path, ref string) ([]byte, error) {
	r := *b.r
	r.accept = "*/*"
	escaped := make([]string, 0)
	for _, seg := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(seg))
	}
	var raw []byte
	_, err := r.do(ctx, "GET", b.repoPath(pr.Repo)+"/src/"+url.PathEscape(ref)+"/"+strings.Join(escaped, "/"), nil, &raw)
	return raw, err
}

// pages follows Bitbucket's "next" links.
func (b *BitbucketClient) pages(ctx context.Context, path string, each func(values json.RawMessage) error) error {
	next := path
	for i := 0; next != "" && i < 100; i++ {
		var page struct {
			Values json.RawMessage `json:"values"`
			Next   string          `json:"next"`
		}
		if _, err := b.r.do(ctx, "GET", next, nil, &page); err != nil {
			return err
		}
		if err := each(page.Values); err != nil {
			return err
		}
		next = page.Next
	}
	return nil
}

func (b *BitbucketClient) ChangedPaths(ctx context.Context, pr *PR, from, to string) ([]string, error) {
	var out []string
	// topic=false compares the two commits directly, not via merge base.
	spec := url.PathEscape(to) + ".." + url.PathEscape(from)
	err := b.pages(ctx, b.repoPath(pr.Repo)+"/diffstat/"+spec+"?topic=false&pagelen=100", func(values json.RawMessage) error {
		var page []struct {
			New *struct {
				Path string `json:"path"`
			} `json:"new"`
			Old *struct {
				Path string `json:"path"`
			} `json:"old"`
		}
		if err := json.Unmarshal(values, &page); err != nil {
			return err
		}
		for _, d := range page {
			switch {
			case d.New != nil:
				out = append(out, d.New.Path)
			case d.Old != nil:
				out = append(out, d.Old.Path)
			}
		}
		return nil
	})
	return out, err
}

type bbComment struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
	User    struct {
		UUID string `json:"uuid"`
		Type string `json:"type"`
	} `json:"user"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	Inline *struct {
		Path string `json:"path"`
		To   *int   `json:"to"`
		From *int   `json:"from"`
	} `json:"inline"`
}

// selfUUID is the token account's UUID: only its comments are the bot's.
// It is "" for an access token, which has no account to read: then only
// bot accounts (app_user) are trusted, which participants are not.
func (b *BitbucketClient) selfUUID(ctx context.Context) (string, error) {
	return b.self.get(ctx, func(ctx context.Context) (string, error) {
		var v struct {
			UUID string `json:"uuid"`
		}
		_, err := b.r.do(ctx, "GET", "/user", nil, &v)
		if s := statusOf(err); s == http.StatusForbidden || s == http.StatusUnauthorized || s == http.StatusNotFound {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read the token's account: %w", err)
		}
		return v.UUID, nil
	})
}

// bitbucketTrusted: a comment by the token's account, or, for an access
// token (self ""), by a bot account.
func bitbucketTrusted(self, uuid, typ string) bool {
	if self != "" {
		return uuid == self
	}
	return typ == "app_user"
}

func (b *BitbucketClient) BotState(ctx context.Context, pr *PR) (*BotState, error) {
	self, err := b.selfUUID(ctx)
	if err != nil {
		return nil, err
	}
	st := &BotState{Fingerprints: map[string]bool{}}
	err = b.pages(ctx, b.prPath(pr.Ref)+"/comments?pagelen=100", func(values json.RawMessage) error {
		var page []bbComment
		if err := json.Unmarshal(values, &page); err != nil {
			return err
		}
		for _, c := range page {
			if c.Deleted || !bitbucketTrusted(self, c.User.UUID, c.User.Type) {
				continue // only the bot's own comments (issue #21)
			}
			body := fromBitbucketMarkdown(c.Content.Raw)
			if c.Inline == nil && isSummary(body) {
				parseSummary(fmt.Sprint(c.ID), body, st)
			}
			collectFingerprints(body, st.Fingerprints)
			if c.Inline != nil {
				line := 0
				if c.Inline.To != nil {
					line = *c.Inline.To
				} else if c.Inline.From != nil {
					line = *c.Inline.From
				}
				if pc, ok := parsePosted(c.Inline.Path, line, body); ok {
					st.Comments = append(st.Comments, pc)
				}
			}
		}
		return nil
	})
	return st, err
}

func bbContent(body string) map[string]string {
	return map[string]string{"raw": toBitbucketMarkdown(Sanitize(body))}
}

func (b *BitbucketClient) PostInline(ctx context.Context, pr *PR, _ string, comments []Comment) ([]Comment, error) {
	var rejected []Comment
	for _, c := range comments {
		body := map[string]any{
			"content": bbContent(c.Body),
			"inline":  map[string]any{"path": c.Path, "to": c.Line},
		}
		if _, err := b.r.do(ctx, "POST", b.prPath(pr.Ref)+"/comments", body, nil); err != nil {
			if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusUnprocessableEntity {
				rejected = append(rejected, c)
				continue
			}
			return rejected, err
		}
	}
	return rejected, nil
}

func (b *BitbucketClient) UpsertSummary(ctx context.Context, pr *PR, st *BotState, body string) error {
	if st != nil && st.SummaryID != "" {
		_, err := b.r.do(ctx, "PUT", b.prPath(pr.Ref)+"/comments/"+url.PathEscape(st.SummaryID), map[string]any{"content": bbContent(body)}, nil)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return b.PostComment(ctx, pr, body)
}

func (b *BitbucketClient) PostComment(ctx context.Context, pr *PR, body string) error {
	_, err := b.r.do(ctx, "POST", b.prPath(pr.Ref)+"/comments", map[string]any{"content": bbContent(body)}, nil)
	return err
}

// React is a no-op: Bitbucket Cloud has no emoji reactions on comments.
func (b *BitbucketClient) React(context.Context, *PR, string, string) error { return nil }

// SetScoreLabels is a no-op: Bitbucket Cloud pull requests have no labels.
// The scores are still shown in the summary comment and the build status.
func (b *BitbucketClient) SetScoreLabels(context.Context, *PR, []ScoreLabel) error { return nil }

func (b *BitbucketClient) UpdatePR(ctx context.Context, pr *PR, title, description *string) error {
	if title == nil && description == nil {
		return nil
	}
	// Bitbucket expects the title on every update.
	body := map[string]string{"title": pr.Title}
	if title != nil {
		body["title"] = *title
	}
	if description != nil {
		body["description"] = toBitbucketMarkdown(*description)
	}
	_, err := b.r.do(ctx, "PUT", b.prPath(pr.Ref), body, nil)
	return err
}

func (b *BitbucketClient) SetCommitStatus(ctx context.Context, pr *PR, sha string, st CommitStatus) error {
	state := map[string]string{StatusRunning: "INPROGRESS", StatusSuccess: "SUCCESSFUL", StatusFailed: "FAILED"}[st.State]
	if state == "" {
		return fmt.Errorf("unknown status %q", st.State)
	}
	link := firstNonEmpty(st.TargetURL, pr.URL)
	if link == "" { // Bitbucket requires a URL on build statuses
		link = fmt.Sprintf("https://bitbucket.org/%s/pull-requests/%d", pr.Repo, pr.Number)
	}
	body := map[string]string{
		"key": CommitStatusName, "name": CommitStatusName, "state": state,
		"description": trimStatus(st.Description), "url": link,
	}
	_, err := b.r.do(ctx, "POST", b.repoPath(pr.Repo)+"/commit/"+url.PathEscape(sha)+"/statuses/build", body, nil)
	return err
}

// CurrentUser returns the authenticated account's nickname (user tokens),
// or an error for access tokens, which are not tied to a user.
func (b *BitbucketClient) CurrentUser(ctx context.Context) (string, error) {
	var v struct {
		Nickname string `json:"nickname"`
	}
	if _, err := b.r.do(ctx, "GET", "/user", nil, &v); err != nil {
		return "", err
	}
	return v.Nickname, nil
}

// ---- Markdown conversion ----

var (
	htmlMarker   = regexp.MustCompile(`<!-- (codereview:[^>]*?) -->`)
	linkMarker   = regexp.MustCompile(`(?m)^\[//\]: # \((codereview:[^)]*)\)$`)
	detailsOpen  = regexp.MustCompile(`(?s)<details>\s*<summary>(.*?)</summary>`)
	detailsClose = regexp.MustCompile(`</details>`)
	subTag       = regexp.MustCompile(`</?sub>`)
)

// toBitbucketMarkdown rewrites HTML the bot uses into Markdown Bitbucket
// renders: hidden markers become invisible link reference definitions on
// their own lines, <details> sections become bold headings.
func toBitbucketMarkdown(s string) string {
	s = htmlMarker.ReplaceAllString(s, "\n[//]: # ($1)\n")
	s = detailsOpen.ReplaceAllString(s, "**$1**\n")
	s = detailsClose.ReplaceAllString(s, "")
	s = subTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "<br>", " · ")
	s = strings.ReplaceAll(s, "> [!NOTE]\n", "> **Note**\n")
	// Collapse the blank lines the replacements leave behind.
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}

// fromBitbucketMarkdown turns markers back into the bot's standard form.
// The summary marker must lead the body again so isSummary still works.
func fromBitbucketMarkdown(s string) string {
	return strings.TrimLeft(linkMarker.ReplaceAllString(s, "<!-- $1 -->"), "\n")
}

var bbPRPath = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull-requests/(\d+)`)
