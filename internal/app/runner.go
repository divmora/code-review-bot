// Package app wires config, code hosts, models and the review engine
// into the operations shared by the CLI and the server.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Runner runs reviews and chat replies.
type Runner struct {
	Cfg    *config.Config
	Logger *slog.Logger
	// ModelOverride forces a model (CLI --model); repo config is ignored then.
	ModelOverride string
	// Override adjusts the repo config after loading (CLI flags).
	Override func(*config.RepoConfig)
	// VerifyModelOverride forces the verification model (CLI --verify-model).
	VerifyModelOverride string

	mu       sync.Mutex
	clients  map[string]llm.Client
	botNames map[string]string // per host: the token's user
}

// botName is the mention handle shown in summaries (@<bot> review) and used
// for placeholders: bot_name from the config, else the user the token
// belongs to (so the hint mentions the real bot account and the code host
// autocompletes it), else "codereview". GitHub App tokens cannot read
// /user; set bot_name to the App's slug there.
func (r *Runner) botName(ctx context.Context, c vcs.Client, host string) string {
	if r.Cfg.BotName != "" {
		return r.Cfg.BotName
	}
	r.mu.Lock()
	if n, ok := r.botNames[host]; ok {
		r.mu.Unlock()
		return n
	}
	r.mu.Unlock()
	name := "codereview"
	if u, ok := c.(interface {
		CurrentUser(context.Context) (string, error)
	}); ok {
		if login, err := u.CurrentUser(ctx); err == nil && login != "" {
			name = login
		} else if err != nil {
			r.Logger.Debug("could not read the token user; using @codereview", "error", err)
		}
	}
	r.mu.Lock()
	if r.botNames == nil {
		r.botNames = map[string]string{}
	}
	r.botNames[host] = name
	r.mu.Unlock()
	return name
}

// Options for one review.
type Options struct {
	// Auto is a push-triggered review; repo auto_review rules apply.
	Auto bool
	// Full ignores incremental state and re-reviews every file.
	Full bool
	// Post publishes comments and the summary. Without it nothing is written.
	Post bool
	// Overrides are admin-panel settings for this repository; they win over
	// .codereview.yaml, and their model choice is trusted.
	Overrides *config.Overrides
	// Commands advertises @bot chat commands in the summary (webhook services).
	Commands bool
	// ScoreLabels sets Quality/Security/Maintainability score labels on the
	// PR (with Post only).
	ScoreLabels bool
	// CommitStatus sets a "codereview" status on the reviewed commit:
	// running during the review, then success or failed (with Post only).
	CommitStatus bool
	// Approval approves the PR when the review passes its rule, and may
	// withdraw an earlier approval (with Post only). Nil leaves approvals
	// alone.
	Approval *Approval
}

// Outcome of a review.
type Outcome struct {
	PR         *vcs.PR
	Result     *review.Result
	SkipReason string // set when the review did not run
	Posted     int
	Rejected   []review.Finding
	// Approval is what happened to the bot's approval (see the Approval*
	// constants; empty without Options.Approval), and ApprovalReason why.
	Approval       string
	ApprovalReason string
}

// LLM returns a (cached) client for the model chain. repoModel is a model
// profile requested by the repository (honoured only with allow_repo_model)
// or by an admin (trusted).
func (r *Runner) LLM(repoModel string, trusted bool) (llm.Client, error) {
	ref := r.ModelOverride
	if ref == "" && repoModel != "" && (trusted || r.Cfg.AllowRepoModel) {
		if _, ok := r.Cfg.Models[repoModel]; ok {
			ref = repoModel
		} else {
			r.Logger.Warn("unknown model profile requested; using the default", "model", repoModel)
		}
	}
	chain, err := r.Cfg.ModelChain(ref)
	if err != nil {
		return nil, err
	}
	key := ""
	for _, c := range chain {
		key += c.String() + "|"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		return c, nil
	}
	c, err := llm.NewChain(chain, r.Logger)
	if err != nil {
		return nil, err
	}
	if r.clients == nil {
		r.clients = map[string]llm.Client{}
	}
	r.clients[key] = c
	return c, nil
}

func (r *Runner) setStatus(ctx context.Context, c vcs.Client, pr *vcs.PR, state, desc string) {
	if err := c.SetCommitStatus(ctx, pr, pr.HeadSHA, vcs.CommitStatus{State: state, Description: desc, TargetURL: pr.URL}); err != nil {
		r.Logger.Warn("could not set commit status", "state", state, "error", err)
	}
}

// statusLine is the commit status text, e.g.
// "Reviewed: 2 comments · Quality 60 · Security 100 · Maintainability 72".
func statusLine(res *review.Result, posted int) string {
	if res.TooLarge {
		return "Not reviewed: too many files"
	}
	s := fmt.Sprintf("Reviewed: %d comment", posted)
	if posted != 1 {
		s += "s"
	}
	if res.IncrementalOf != "" {
		s += " (new commits)"
	}
	if sc := res.Scores; sc != nil {
		s += fmt.Sprintf(" · Quality %d · Security %d · Maintainability %d", sc.Quality, sc.Security, sc.Maintainability)
	}
	return s
}

// placeholders returns the summary and title placeholders, following the
// bot's name when the repo kept the defaults.
func placeholders(rc config.RepoConfig, bot string) (summary, title string) {
	summary, title = rc.HighLevelSummaryPlaceholder, rc.AutoTitlePlaceholder
	if bot != "" && bot != "codereview" {
		if summary == "@codereview summary" {
			summary = "@" + bot + " summary"
		}
		if title == "@codereview" {
			title = "@" + bot
		}
	}
	return summary, title
}

// updatePR writes the summary block into the PR description and replaces a
// title that contains the title placeholder, like CodeRabbit. The PR is read
// again first so edits made during the review are not lost. Failures are
// logged: the review itself is already posted.
func (r *Runner) updatePR(ctx context.Context, c vcs.Client, ref vcs.Ref, res *review.Result, bot, summaryPH, titlePH string, autoAppend bool) {
	if res.TooLarge || len(res.Reviewed) == 0 {
		// Nothing was reviewed: the summary comment explains why, and a
		// description block saying so helps nobody.
		return
	}
	fresh, err := c.GetPR(ctx, ref)
	if err != nil {
		r.Logger.Warn("could not re-read the PR to update its description", "error", err)
		return
	}
	var title, desc *string
	if d, changed := review.ApplyDescription(fresh.Body, review.DescriptionBlock(res, bot), summaryPH, autoAppend); changed {
		desc = &d
	}
	if t, changed := review.ApplyTitle(fresh.Title, res.SuggestedTitle, titlePH); changed {
		title = &t
	}
	if title == nil && desc == nil {
		return
	}
	if err := c.UpdatePR(ctx, fresh, title, desc); err != nil {
		r.Logger.Warn("could not update the PR title/description", "error", err)
	}
}

// VerifyLLM returns the (cached) client for the verification pass, or nil to
// use the review model. Retries apply; fallbacks are not used, so a failing
// verifier simply leaves the findings unverified.
func (r *Runner) VerifyLLM() (llm.Client, error) {
	ref := firstNonEmpty(r.VerifyModelOverride, r.Cfg.Review.VerifyModel)
	if ref == "" {
		return nil, nil
	}
	m, err := r.Cfg.ResolveModel(ref)
	if err != nil {
		return nil, fmt.Errorf("verify_model: %w", err)
	}
	key := "verify|" + m.String() + "|" + m.BaseURL + "|" + m.Region
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		return c, nil
	}
	c, err := llm.New(m, r.Logger)
	if err != nil {
		return nil, fmt.Errorf("verify_model: %w", err)
	}
	if r.clients == nil {
		r.clients = map[string]llm.Client{}
	}
	r.clients[key] = c
	return c, nil
}

// RepoConfig loads .codereview.yaml. It is read from the base commit, not
// the PR head, so a PR cannot weaken the rules it is reviewed under. Local
// reviews read the working tree.
func (r *Runner) RepoConfig(ctx context.Context, c vcs.Client, pr *vcs.PR) (config.RepoConfig, error) {
	rc, err := r.loadRepoConfig(ctx, c, pr)
	if r.Override != nil {
		r.Override(&rc)
	}
	return rc, err
}

func (r *Runner) loadRepoConfig(ctx context.Context, c vcs.Client, pr *vcs.PR) (config.RepoConfig, error) {
	ref := pr.BaseSHA
	data, err := c.FileContent(ctx, pr, r.Cfg.Review.RepoConfigFile, ref)
	if err != nil {
		if errors.Is(err, vcs.ErrNotFound) {
			return r.Cfg.Review.Defaults, nil
		}
		r.Logger.Warn("could not read repo config; using defaults", "error", err)
		return r.Cfg.Review.Defaults, nil
	}
	rc, err := config.ParseRepoConfig(data, r.Cfg.Review.Defaults)
	if err != nil {
		return r.Cfg.Review.Defaults, fmt.Errorf("%s: %w", r.Cfg.Review.RepoConfigFile, err)
	}
	return rc, nil
}

// Review runs one review end to end.
func (r *Runner) Review(ctx context.Context, c vcs.Client, ref vcs.Ref, opts Options) (*Outcome, error) {
	r.useStateKey(c)
	pr, err := c.GetPR(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get pull request: %w", err)
	}
	out := &Outcome{PR: pr}
	rc, cfgErr := r.RepoConfig(ctx, c, pr)
	opts.Overrides.Apply(&rc)
	if pr.State == vcs.StateClosed || pr.State == vcs.StateMerged {
		if opts.Auto {
			// A pipeline can run for an MR that was closed or merged in the
			// meantime; nobody reads a review of it.
			out.SkipReason = "pull request is " + pr.State
			return out, nil
		}
		r.Logger.Warn("reviewing a pull request that is not open", "state", pr.State)
	}
	if opts.Auto {
		if reason := rc.AutoReviewSkipReason(pr.Title, pr.BaseBranch, pr.Draft); reason != "" {
			out.SkipReason = reason
			return out, nil
		}
	}

	state, err := c.BotState(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("read previous review state: %w", err)
	}
	if opts.Auto && state.ReviewedSHA == pr.HeadSHA && pr.HeadSHA != "" {
		out.SkipReason = "commit " + pr.HeadSHA + " was already reviewed"
		return out, nil
	}

	statusDone := true
	if opts.Post && opts.CommitStatus && pr.HeadSHA != "" {
		statusDone = false
		r.setStatus(ctx, c, pr, vcs.StatusRunning, "Review in progress")
		// Any return before the final success status (an error, a failed
		// post, cancellation) leaves the commit marked failed, never running.
		defer func() {
			if !statusDone {
				r.setStatus(context.WithoutCancel(ctx), c, pr, vcs.StatusFailed, "Review failed")
			}
		}()
	}

	files, err := c.Files(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}

	in := review.Input{PR: pr, Files: files, Repo: rc, Known: state.Fingerprints, Existing: state.Comments}
	in.Content = func(ctx context.Context, path string) ([]byte, error) {
		return c.FileContent(ctx, pr, path, pr.HeadSHA)
	}
	if rc.Guidelines.Enabled == nil || *rc.Guidelines.Enabled {
		names := rc.Guidelines.Files
		if names == nil {
			names = review.DefaultGuidelineFiles
		}
		changed := make([]string, 0, len(files))
		for _, f := range files {
			changed = append(changed, f.Path)
		}
		// From the target branch, like .codereview.yaml: a PR cannot
		// rewrite the guidelines it is reviewed against.
		in.Guidelines = review.LoadGuidelines(ctx, names, changed, func(ctx context.Context, p string) ([]byte, error) {
			return c.FileContent(ctx, pr, p, pr.BaseSHA)
		})
		if len(in.Guidelines) > 0 {
			r.Logger.Info("using repository guidelines", "files", len(in.Guidelines))
		}
	}
	if !opts.Full && *rc.AutoReview.Incremental && state.ReviewedSHA != "" && state.ReviewedSHA != pr.HeadSHA {
		paths, err := c.ChangedPaths(ctx, pr, state.ReviewedSHA, pr.HeadSHA)
		if err != nil {
			// Force-pushes drop the old commit; review everything.
			r.Logger.Info("incremental range unavailable; doing a full review", "from", state.ReviewedSHA, "error", err)
		} else {
			in.OnlyPaths = map[string]bool{}
			for _, p := range paths {
				in.OnlyPaths[p] = true
			}
			in.IncrementalFrom = state.ReviewedSHA
			var prev review.State
			if len(state.State) > 0 && json.Unmarshal(state.State, &prev) == nil {
				in.PrevFileSummaries = prev.FileSummaries
				in.PrevDiagram = prev.Diagram
			}
		}
	}

	if !opts.Full && (r.Cfg.Review.AnchorScores == nil || *r.Cfg.Review.AnchorScores) {
		anchorTo(&in, state)
	}
	if !opts.Full {
		in.PrevFindings = r.previousFindings(ctx, c, pr, state)
	}

	model, err := r.LLM(rc.Model, opts.Overrides != nil && opts.Overrides.Model != nil && *opts.Overrides.Model != "")
	if err != nil {
		return nil, err
	}
	verifier, err := r.VerifyLLM()
	if err != nil {
		return nil, err
	}
	eng := &review.Engine{LLM: model, VerifyLLM: verifier, Limits: r.Cfg.Review, Logger: r.Logger.With("pr", ref.String())}
	res, err := eng.Run(ctx, in)
	if err != nil {
		return nil, err
	}
	if cfgErr != nil {
		res.Errors = append(res.Errors, cfgErr.Error()+" (defaults were used)")
	}
	{
		names := rc.Guidelines.Files
		if names == nil {
			names = review.DefaultGuidelineFiles
		}
		changed := make([]string, 0, len(files))
		for _, f := range files {
			changed = append(changed, f.Path)
		}
		res.ConfigChanges = review.ConfigChanges(changed, r.Cfg.Review.RepoConfigFile, names)
	}
	out.Result = res

	if !opts.Post {
		return out, nil
	}
	if len(res.Findings) > 0 {
		comments := review.ToComments(res.Findings, c.Platform())
		rejected, err := c.PostInline(ctx, pr, review.ReviewHeader(len(comments)), comments)
		if err != nil {
			return out, fmt.Errorf("post inline comments: %w", err)
		}
		rej := map[string]bool{}
		for _, rc := range rejected {
			rej[rc.Fingerprint] = true
		}
		for _, f := range res.Findings {
			if rej[f.Fingerprint] {
				out.Rejected = append(out.Rejected, f)
			}
		}
		out.Posted = len(comments) - len(rejected)
	}
	bot := r.botName(ctx, c, ref.Host)
	summaryPH, titlePH := placeholders(rc, bot)
	body := review.SummaryMarkdown(res, review.SummaryOptions{BotName: bot, Rejected: out.Rejected, Posted: out.Posted, Commands: opts.Commands, TitlePlaceholder: titlePH})
	if err := c.UpsertSummary(ctx, pr, state, body); err != nil {
		return out, fmt.Errorf("post summary: %w", err)
	}
	r.updatePR(ctx, c, ref, res, bot, summaryPH, titlePH, rc.HighLevelSummary == nil || *rc.HighLevelSummary)
	if !statusDone {
		r.setStatus(ctx, c, pr, vcs.StatusSuccess, statusLine(res, out.Posted))
		statusDone = true
	}
	if opts.ScoreLabels && res.Scores != nil {
		colors := map[string]string{"green": "2da44e", "orange": "fb8c00", "red": "d73a49"}
		var labels []vcs.ScoreLabel
		for _, n := range review.ScoreNames {
			v := res.Scores.Get(n)
			labels = append(labels, vcs.ScoreLabel{Name: n, Value: v, Color: colors[review.ScoreBand(v)]})
		}
		// Labels are a convenience: a failure (e.g. missing permission) is
		// logged, not fatal, since the review is already posted.
		if err := c.SetScoreLabels(ctx, pr, labels); err != nil {
			r.Logger.Warn("could not set score labels", "error", err)
		}
	}
	if opts.Approval != nil {
		// The last review was of this same head: an approval then was for it.
		sameHead := state != nil && state.ReviewedSHA != "" && state.ReviewedSHA == pr.HeadSHA
		out.Approval, out.ApprovalReason = r.approve(ctx, c, pr, res, *opts.Approval, bot, sameHead)
	}
	return out, nil
}

// DiagramOutcome is what a diagram-only run did.
type DiagramOutcome struct {
	PR      *vcs.PR
	Diagram *review.Diagram
	Usage   llm.Usage
	Model   string
	// Note says why no diagram was drawn.
	Note string
}

// ErrDiagramOff is returned when the admin turned flow diagrams off for
// the repository: "@bot diagram" can't override that.
var ErrDiagramOff = errors.New("flow diagrams are turned off for this repository")

// Diagram draws the flow diagram for the PR's head without reviewing it
// again (the "@bot diagram" command), from the file summaries and open
// findings the last review stored, and with post replaces the diagram in
// the summary comment. It works whatever the repository's diagram mode,
// unless the admin's overrides set it to off.
func (r *Runner) Diagram(ctx context.Context, c vcs.Client, ref vcs.Ref, post bool, ov *config.Overrides) (*DiagramOutcome, error) {
	r.useStateKey(c)
	if ov != nil && ov.Diagram != nil && *ov.Diagram == review.DiagramOff {
		return nil, ErrDiagramOff
	}
	pr, err := c.GetPR(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get pull request: %w", err)
	}
	out := &DiagramOutcome{PR: pr}
	rc, _ := r.RepoConfig(ctx, c, pr)
	ov.Apply(&rc)
	state, err := c.BotState(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("read previous review state: %w", err)
	}
	var prev review.State
	if len(state.State) == 0 || json.Unmarshal(state.State, &prev) != nil || len(prev.FileSummaries) == 0 {
		return nil, errors.New("no review to draw from yet: review the pull request first")
	}
	files, err := c.Files(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}
	kept := files[:0:0]
	for _, f := range files {
		if rc.IncludePath(f.Path) {
			kept = append(kept, f)
		}
	}
	model, err := r.LLM(rc.Model, ov != nil && ov.Model != nil && *ov.Model != "")
	if err != nil {
		return nil, err
	}
	out.Model = model.Name()
	eng := &review.Engine{LLM: model, Limits: r.Cfg.Review, Logger: r.Logger.With("pr", ref.String())}
	d, usage, err := eng.DiagramOnly(ctx, pr, kept, prev.FileSummaries, prev.Findings)
	out.Usage = usage
	if err != nil {
		return out, err
	}
	if d == nil {
		out.Note = "the model found no flow to draw"
		return out, nil
	}
	out.Diagram = d
	if !post {
		return out, nil
	}
	if state.SummaryID == "" {
		return out, errors.New("no summary comment to add the diagram to")
	}
	prev.Diagram = d
	st, _ := json.Marshal(prev)
	body := review.ReplaceDiagram(state.SummaryBody, review.DiagramSection(d))
	body = vcs.ReplaceStateTag(body, st)
	if err := c.UpsertSummary(ctx, pr, state, body); err != nil {
		return out, fmt.Errorf("post summary: %w", err)
	}
	return out, nil
}

// Answer replies to a question on a PR and posts the reply when post is set.
func (r *Runner) Answer(ctx context.Context, c vcs.Client, ref vcs.Ref, question string, post bool, ov *config.Overrides) (string, error) {
	r.useStateKey(c)
	pr, err := c.GetPR(ctx, ref)
	if err != nil {
		return "", err
	}
	rc, _ := r.RepoConfig(ctx, c, pr)
	ov.Apply(&rc)
	files, err := c.Files(ctx, pr)
	if err != nil {
		return "", err
	}
	model, err := r.LLM(rc.Model, ov != nil && ov.Model != nil && *ov.Model != "")
	if err != nil {
		return "", err
	}
	eng := &review.Engine{LLM: model, Limits: r.Cfg.Review, Logger: r.Logger}
	ans, err := eng.Answer(ctx, pr, files, rc, question)
	if err != nil {
		return "", err
	}
	if post {
		quoted := "> " + strings.ReplaceAll(truncate(strings.TrimSpace(question), 300), "\n", "\n> ")
		if err := c.PostComment(ctx, pr, quoted+"\n\n"+ans+"\n\n<!-- codereview:reply -->"); err != nil {
			return ans, err
		}
	}
	return ans, nil
}

// useStateKey gives the client the key that signs the bot's markers
// (CODEREVIEW_STATE_KEY), when one is set.
func (r *Runner) useStateKey(c vcs.Client) {
	if r.Cfg == nil || r.Cfg.StateKey == "" {
		return
	}
	if k, ok := c.(vcs.StateKeyer); ok {
		k.SetStateKey([]byte(r.Cfg.StateKey))
	}
}

// githubWebHost is the web host whose PRs an API URL serves:
// api.github.com serves github.com and api.<name>.ghe.com <name>.ghe.com
// (GitHub Enterprise Cloud with data residency); Enterprise Server's
// https://<host>/api/v3 serves <host>.
func githubWebHost(api string) string {
	u, err := url.Parse(api)
	if err != nil || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Host)
	if h == "api.github.com" || strings.HasPrefix(h, "api.") && strings.HasSuffix(h, ".ghe.com") {
		return strings.TrimPrefix(h, "api.")
	}
	return h
}

// ClientFor returns a code host client for a ref using static credentials
// (CLI and token-based servers).
func (r *Runner) ClientFor(ref vcs.Ref) (vcs.Client, error) {
	switch ref.Platform {
	case vcs.GitHub:
		// The token only goes to the configured API (github.api_url or
		// GITHUB_API_URL, set in GitHub Actions), never to a host taken
		// from the PR URL (issue #23).
		api := r.Cfg.GitHub.APIURL
		want := r.Cfg.GitHub.Host
		if want == "" {
			want = githubWebHost(api)
		}
		if !strings.EqualFold(ref.Host, want) {
			return nil, fmt.Errorf("%s isn't the configured GitHub (%s): set GITHUB_API_URL (or github.api_url) to https://%s/api/v3 for GitHub Enterprise Server, or github.host when the API is reached under another name", ref.Host, want, ref.Host)
		}
		return vcs.NewGitHub(api, vcs.StaticToken(r.Cfg.GitHub.Token)), nil
	case vcs.Bitbucket:
		b := r.Cfg.Bitbucket
		return vcs.NewBitbucket(b.APIURL, vcs.BitbucketAuth{Token: b.Token, Email: b.Email, APIToken: b.APIToken}), nil
	case vcs.GitLab:
		// A token only goes to its own host: GITLAB_TOKEN to GITLAB_URL,
		// CI_SERVER_URL or gitlab.com, never to a host taken from the MR
		// URL (issue #23).
		gl, ok := r.Cfg.GitLabFor(ref.Host)
		if !ok {
			return nil, fmt.Errorf("no GitLab config for host %s: add it under gitlab: in the config (with host: %s when base_url is an internal name), or set GITLAB_URL=https://%s (GITLAB_TOKEN is only sent to GITLAB_URL, CI_SERVER_URL or gitlab.com)", ref.Host, ref.Host, ref.Host)
		}
		return vcs.NewGitLab(gl.BaseURL, gl.Token, gl.SkipTLSVerify), nil
	}
	return nil, fmt.Errorf("unsupported platform %q", ref.Platform)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// maxPrevFileChecks bounds the files fetched to check earlier findings.
const maxPrevFileChecks = 40

// previousFindings are the findings open at the last review, each marked
// Changed when the code it pointed at has changed since (or its file is
// gone): those count as fixed unless reported again (issue #15). nil for
// summaries written before findings were kept.
func (r *Runner) previousFindings(ctx context.Context, c vcs.Client, pr *vcs.PR, state *vcs.BotState) []review.PrevFinding {
	if state == nil || len(state.State) == 0 {
		return nil
	}
	var prev review.State
	if json.Unmarshal(state.State, &prev) != nil || prev.Findings == nil {
		return nil
	}
	out := append([]review.PrevFinding{}, prev.Findings...)
	from := state.ReviewedSHA
	if from == "" || from == pr.HeadSHA {
		return out // nothing changed since
	}
	var changed map[string]bool // nil: unknown (a force-push dropped the old commit)
	if paths, err := c.ChangedPaths(ctx, pr, from, pr.HeadSHA); err == nil {
		changed = map[string]bool{}
		for _, p := range paths {
			changed[p] = true
		}
	}
	contents := map[string][]byte{}
	gone := map[string]bool{}
	checked := 0
	for i := range out {
		p := &out[i]
		if changed != nil && !changed[p.Path] {
			continue // the file didn't change
		}
		if _, ok := contents[p.Path]; !ok && !gone[p.Path] {
			if checked >= maxPrevFileChecks {
				continue
			}
			checked++
			b, err := c.FileContent(ctx, pr, p.Path, pr.HeadSHA)
			switch {
			case errors.Is(err, vcs.ErrNotFound):
				gone[p.Path] = true
			case err != nil:
				r.Logger.Info("could not check an earlier finding", "path", p.Path, "error", err)
				continue
			default:
				contents[p.Path] = b
			}
		}
		switch {
		case gone[p.Path]:
			p.Changed = true
		case p.CodeHash != "":
			p.Changed = !review.CodeStillThere(contents[p.Path], *p)
		default: // the code wasn't kept: the file changing is all we know
			p.Changed = changed != nil
		}
	}
	return out
}

// anchorTo gives the review the previous scores to start from: those kept
// in the summary's state, or (older summaries) the scores table in it.
func anchorTo(in *review.Input, state *vcs.BotState) {
	if state == nil {
		return
	}
	var prev review.State
	if len(state.State) > 0 && json.Unmarshal(state.State, &prev) == nil && prev.Scores != nil {
		in.PrevScores, in.PrevScoresSHA, in.PrevOpen = prev.Scores, prev.ScoresSHA, prev.OpenFindings
		return
	}
	if sc, ok := review.SummaryScores(state.SummaryBody); ok {
		in.PrevScores, in.PrevScoresSHA = sc, state.ReviewedSHA
	}
}
