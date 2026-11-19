// Package vcs reads pull/merge requests from code hosts and publishes
// review results back to them.
package vcs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// Platform names.
const (
	GitHub = "github"
	GitLab = "gitlab"
	Local  = "local"
)

// Markers embedded as HTML comments so the bot can find its own output.
const (
	SummaryMarker     = "<!-- codereview:summary -->"
	fingerprintPrefix = "<!-- codereview:fp="
	reviewedSHAPrefix = "<!-- codereview:reviewed_sha="
	statePrefix       = "<!-- codereview:state="
)

// ErrNotFound is returned when a file or object does not exist.
var ErrNotFound = errors.New("not found")

// Ref identifies a pull/merge request.
type Ref struct {
	Platform string
	Host     string // web host, e.g. github.com or gitlab.example.com
	Repo     string // owner/repo or group/subgroup/project
	Number   int
}

func (r Ref) String() string { return fmt.Sprintf("%s:%s#%d", r.Host, r.Repo, r.Number) }

// PR is pull/merge request metadata.
type PR struct {
	Ref
	Title      string
	Body       string
	Author     string
	BaseBranch string
	HeadBranch string
	BaseSHA    string
	HeadSHA    string
	StartSHA   string // GitLab diff_refs.start_sha
	Draft      bool
	URL        string
}

// Comment is an inline review comment on the new side of the diff.
type Comment struct {
	Path      string
	OldPath   string
	StartLine int // first line of a multi-line range; 0 or == Line for one line
	Line      int
	OldLine   int // old-side number when Line is a context line (GitLab)
	Body      string
	// Fingerprint dedupes comments across review runs.
	Fingerprint string
}

// BotState is what the bot previously left on a PR.
type BotState struct {
	SummaryID   string
	SummaryBody string
	ReviewedSHA string
	// State is the JSON the bot stored in its summary (file summaries).
	State        []byte
	Fingerprints map[string]bool
	// Comments are the bot's inline comments already on the PR, used to
	// recognise a finding that was posted before under different wording.
	Comments []PostedComment
}

// PostedComment is an inline comment the bot posted earlier.
type PostedComment struct {
	Path     string
	Line     int
	Title    string
	Question bool
}

var titleRe = regexp.MustCompile(`(?m)^\*\*(.+?)\*\*\s*$`)

// parsePosted reads a bot comment body. ok is false for non-bot comments.
func parsePosted(path string, line int, body string) (PostedComment, bool) {
	if !strings.Contains(body, fingerprintPrefix) || path == "" || line <= 0 {
		return PostedComment{}, false
	}
	pc := PostedComment{Path: path, Line: line, Question: strings.Contains(body, "❓ Question")}
	if m := titleRe.FindStringSubmatch(body); m != nil {
		pc.Title = m[1]
	}
	return pc, true
}

// Client is a code host connection.
type Client interface {
	Platform() string
	GetPR(ctx context.Context, ref Ref) (*PR, error)
	// Files lists changed files with parsed hunks.
	Files(ctx context.Context, pr *PR) ([]diff.File, error)
	// FileContent returns a file at a commit, or ErrNotFound.
	FileContent(ctx context.Context, pr *PR, path, ref string) ([]byte, error)
	// ChangedPaths lists paths changed between two commits.
	ChangedPaths(ctx context.Context, pr *PR, from, to string) ([]string, error)
	BotState(ctx context.Context, pr *PR) (*BotState, error)
	// PostInline posts comments. Comments the host rejects (for example
	// a line outside the diff) are returned so the caller can list them
	// in the summary instead.
	PostInline(ctx context.Context, pr *PR, header string, comments []Comment) (rejected []Comment, err error)
	UpsertSummary(ctx context.Context, pr *PR, state *BotState, body string) error
	PostComment(ctx context.Context, pr *PR, body string) error
	// React adds an emoji reaction to a top-level comment/note.
	React(ctx context.Context, pr *PR, commentID string, emoji string) error
	// SetScoreLabels replaces the PR's score labels (e.g. "Quality Score::85"
	// on GitLab, "Quality Score: 85" on GitHub) with the given scores.
	SetScoreLabels(ctx context.Context, pr *PR, scores []ScoreLabel) error
	// UpdatePR changes the title and/or description; nil leaves a field as is.
	UpdatePR(ctx context.Context, pr *PR, title, description *string) error
	// SetCommitStatus sets the bot's status on a commit, shown next to the
	// commit and in the PR's checks/pipeline widget.
	SetCommitStatus(ctx context.Context, pr *PR, sha string, st CommitStatus) error
}

// Commit status states.
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// CommitStatusName is the status context/name shown on commits.
const CommitStatusName = "codereview"

// CommitStatus is the bot's status on one commit.
type CommitStatus struct {
	State       string // StatusRunning, StatusSuccess, StatusFailed
	Description string // short, shown next to the commit (trimmed to 140 chars)
	TargetURL   string
}

func trimStatus(s string) string {
	if len(s) > 140 {
		return s[:137] + "..."
	}
	return s
}

// ScoreLabel is one score to show as a label. Color is a hex RGB like "2da44e".
type ScoreLabel struct {
	Name  string // Quality, Security, Maintainability
	Value int
	Color string
}

// GitLabScoreLabel and GitHubScoreLabel name the labels. GitLab's "::" makes
// them scoped labels (one value per key).
func GitLabScoreLabel(name string, v int) string { return fmt.Sprintf("%s Score::%d", name, v) }
func GitHubScoreLabel(name string, v int) string { return fmt.Sprintf("%s Score: %d", name, v) }

// staleScoreLabels returns current labels that are score labels for one of
// the given names but not the wanted value.
func staleScoreLabels(current []string, scores []ScoreLabel, label func(string, int) string) []string {
	var out []string
	for _, l := range current {
		for _, s := range scores {
			prefix := strings.TrimSuffix(label(s.Name, 0), "0")
			if strings.HasPrefix(l, prefix) && l != label(s.Name, s.Value) {
				out = append(out, l)
			}
		}
	}
	return out
}

// SuggestionFence returns the opening fence for a committable suggestion
// that replaces lines start..end on the platform.
func SuggestionFence(platform string, start, end int) string {
	if platform == Bitbucket {
		// No committable-suggestion syntax on Bitbucket Cloud: show the code.
		return "**Suggested change:**\n```"
	}
	if platform == GitLab {
		above := 0
		if start > 0 && start < end {
			above = end - start
		}
		return fmt.Sprintf("```suggestion:-%d+0", above)
	}
	return "```suggestion"
}

// FingerprintTag is embedded in comment bodies.
func FingerprintTag(fp string) string { return fingerprintPrefix + fp + " -->" }

// StateTag embeds JSON state in the summary as base64.
func StateTag(state []byte) string {
	return statePrefix + base64.StdEncoding.EncodeToString(state) + " -->"
}

var stateRe = regexp.MustCompile(`<!-- codereview:state=([A-Za-z0-9+/=]+) -->`)

// parseSummary fills summary fields of st from the bot's summary comment.
func parseSummary(id, body string, st *BotState) {
	st.SummaryID = id
	st.SummaryBody = body
	st.ReviewedSHA = reviewedSHA(body)
	if m := stateRe.FindStringSubmatch(body); m != nil {
		if b, err := base64.StdEncoding.DecodeString(m[1]); err == nil {
			st.State = b
		}
	}
}

// ReviewedSHATag records the last reviewed head commit in the summary.
func ReviewedSHATag(sha string) string { return reviewedSHAPrefix + sha + " -->" }

var (
	fpRe  = regexp.MustCompile(`<!-- codereview:fp=([a-f0-9]+) -->`)
	shaRe = regexp.MustCompile(`<!-- codereview:reviewed_sha=([A-Za-z0-9]+) -->`)
)

func collectFingerprints(body string, into map[string]bool) {
	for _, m := range fpRe.FindAllStringSubmatch(body, -1) {
		into[m[1]] = true
	}
}

func reviewedSHA(body string) string {
	if m := shaRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// isSummary reports whether a comment body is the bot's summary. The
// marker must lead the body so quoted copies by humans do not match.
func isSummary(body string) bool { return strings.HasPrefix(strings.TrimSpace(body), SummaryMarker) }

// ---- URL parsing ----

var (
	ghPR = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)`)
	glMR = regexp.MustCompile(`^/(.+?)/-/merge_requests/(\d+)`)
)

// ParseURL parses a PR/MR web URL. Hosts other than github.com are
// detected by path shape (/-/merge_requests/ is GitLab, /pull/ GitHub).
func ParseURL(raw string) (Ref, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return Ref{}, fmt.Errorf("not a URL: %q", raw)
	}
	if m := glMR.FindStringSubmatch(u.Path); m != nil {
		n, _ := strconv.Atoi(m[2])
		return Ref{Platform: GitLab, Host: u.Host, Repo: m[1], Number: n}, nil
	}
	if m := bbPRPath.FindStringSubmatch(u.Path); m != nil && (u.Host == "bitbucket.org" || u.Host == "www.bitbucket.org") {
		n, _ := strconv.Atoi(m[3])
		return Ref{Platform: Bitbucket, Host: "bitbucket.org", Repo: m[1] + "/" + m[2], Number: n}, nil
	}
	if m := ghPR.FindStringSubmatch(u.Path); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Ref{Platform: GitHub, Host: u.Host, Repo: m[1] + "/" + m[2], Number: n}, nil
	}
	return Ref{}, fmt.Errorf("unrecognised PR/MR URL %q (want .../owner/repo/pull/N, .../group/project/-/merge_requests/N or bitbucket.org/workspace/repo/pull-requests/N)", raw)
}

// ---- shared REST helper ----

type rest struct {
	base   string
	hc     *http.Client
	auth   func(ctx context.Context, req *http.Request) error
	name   string
	accept string
}

// HTTPError is a non-2xx response from a code host.
type HTTPError struct {
	Host   string
	Method string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("%s %s %s: HTTP %d: %s", e.Host, e.Method, e.Path, e.Status, b)
}

func statusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// do sends a request. out may be nil, *[]byte (raw body) or a JSON target.
func (r *rest) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	target := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		target = r.base + path
	}
	for attempt := 0; ; attempt++ {
		if body != nil {
			if s, ok := body.(io.Seeker); ok {
				_, _ = s.Seek(0, io.SeekStart)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, err
		}
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if r.accept != "" {
			req.Header.Set("Accept", r.accept)
		}
		req.Header.Set("User-Agent", "code-review-bot")
		if err := r.auth(ctx, req); err != nil {
			return nil, fmt.Errorf("%s auth: %w", r.name, err)
		}
		resp, err := r.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %s %s: %w", r.name, method, path, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode == 429 || resp.StatusCode >= 500 || isSecondaryRateLimit(resp, data)) && attempt < 3 {
			wait := time.Duration(1<<attempt) * 2 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(min(s, 60)) * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			if resp.StatusCode == http.StatusNotFound {
				return resp.Header, fmt.Errorf("%w: %w", ErrNotFound, &HTTPError{r.name, method, path, resp.StatusCode, string(data)})
			}
			return resp.Header, &HTTPError{r.name, method, path, resp.StatusCode, string(data)}
		}
		switch o := out.(type) {
		case nil:
		case *[]byte:
			*o = data
		default:
			if len(data) > 0 {
				if err := json.Unmarshal(data, out); err != nil {
					return resp.Header, fmt.Errorf("%s %s %s: decode: %w", r.name, method, path, err)
				}
			}
		}
		return resp.Header, nil
	}
}

func isSecondaryRateLimit(resp *http.Response, body []byte) bool {
	return resp.StatusCode == 403 && resp.Header.Get("Retry-After") != "" && bytes.Contains(bytes.ToLower(body), []byte("rate limit"))
}
