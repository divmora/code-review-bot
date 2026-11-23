// Package review turns a set of changed files into validated review
// findings and a walkthrough, using any llm.Client.
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Severity of a finding.
type Severity string

const (
	Critical Severity = "critical"
	Major    Severity = "major"
	Minor    Severity = "minor"
	Nitpick  Severity = "nitpick"
)

var severityRank = map[Severity]int{Critical: 4, Major: 3, Minor: 2, Nitpick: 1}

// ParseSeverity normalises a severity string; unknown values become minor.
func ParseSeverity(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "blocker", "high-critical":
		return Critical
	case "major", "high", "error":
		return Major
	case "minor", "medium", "warning":
		return Minor
	case "nitpick", "nit", "low", "info", "trivial":
		return Nitpick
	}
	return Minor
}

// AtLeast reports whether s is at least as severe as min.
func (s Severity) AtLeast(min Severity) bool { return severityRank[s] >= severityRank[min] }

// Finding is one validated review comment.
type Finding struct {
	Path        string   `json:"path"`
	OldPath     string   `json:"old_path,omitempty"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	OldLine     int      `json:"-"`
	Severity    Severity `json:"severity"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Confidence  float64  `json:"confidence"`
	Source      string   `json:"source"` // llm, verifier, secret-scan
	Fingerprint string   `json:"fingerprint"`
	// Kind is "issue" (default) or "question" (depends on data, config or
	// intent the code does not show; asked, not asserted).
	Kind string `json:"kind,omitempty"`

	lineText string
	// carried is the earlier finding this one stands for (Result.Carried).
	carried *PrevFinding
	// repeat marks a finding already posted on the PR while it goes
	// through verification with the new ones.
	repeat bool
}

// KindQuestion marks a finding posted as a question to the author.
const KindQuestion = "question"

// Highlight is a group of release-notes bullets.
type Highlight struct {
	Type  string   `json:"type"`
	Items []string `json:"items"`
}

// Cohort groups related files in the walkthrough.
type Cohort struct {
	Label   string   `json:"label"`
	Files   []string `json:"files"`
	Summary string   `json:"summary"`
}

// SkippedFile is a changed file that was not reviewed.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Result of a review run.
type Result struct {
	Summary       string            `json:"summary"`
	Cohorts       []Cohort          `json:"cohorts"`
	Effort        int               `json:"effort"`
	Poem          string            `json:"poem,omitempty"`
	FileSummaries map[string]string `json:"file_summaries"`
	// Findings are the comments to post inline (sorted, capped).
	Findings []Finding `json:"findings"`
	// Overflow are valid findings beyond max_comments.
	Overflow []Finding `json:"overflow,omitempty"`
	// Repeated are findings already posted on the PR that are still present.
	// They are not posted again but count towards the scores.
	Repeated []Finding `json:"repeated,omitempty"`
	// AlreadyPosted counts findings that match comments from earlier runs.
	AlreadyPosted int           `json:"already_posted"`
	Dropped       int           `json:"dropped"` // invalid lines, low confidence, below min severity
	Reviewed      []string      `json:"reviewed"`
	Unchanged     []string      `json:"unchanged,omitempty"` // skipped by incremental review
	Skipped       []SkippedFile `json:"skipped,omitempty"`
	Errors        []string      `json:"errors,omitempty"`     // failed batches (partial review)
	Guidelines    []string      `json:"guidelines,omitempty"` // guideline files used
	// ConfigChanges lists review config / guideline files this PR edits.
	// Their new versions apply only after merge (rules come from the target).
	ConfigChanges []string `json:"config_changes,omitempty"`
	// Highlights are release-notes bullets for the PR description.
	Highlights []Highlight `json:"highlights,omitempty"`
	// SuggestedTitle is a generated Conventional Commits title; TitleAccurate
	// tells whether the current title already fits.
	SuggestedTitle string `json:"suggested_title,omitempty"`
	TitleAccurate  bool   `json:"title_accurate"`
	// DescriptionCheck names what the PR description gets wrong or omits.
	DescriptionCheck string `json:"description_check,omitempty"`
	// Scores rate the change 1-100 (quality, security, maintainability).
	Scores *Scores `json:"scores,omitempty"`
	// ScoreChanges are the scores that moved from the previous review,
	// with why (empty without a previous review to anchor to).
	ScoreChanges []ScoreChange `json:"score_changes,omitempty"`
	// ScoresNote says why Scores is empty when the review gave none (for
	// example, the walkthrough failed). No scores are made up then.
	ScoresNote string `json:"scores_note,omitempty"`
	// Verification reports the second-pass check of the findings.
	Verification *Verification `json:"verification,omitempty"`
	TooLarge     bool          `json:"too_large,omitempty"`
	Model        string        `json:"model"`
	Usage        llm.Usage     `json:"usage"`
	Duration     time.Duration `json:"duration"`

	// carry are the previous review's scores, kept in the summary's state
	// when this review gave none, so the next review still anchors to them.
	carry         *carriedScores
	HeadSHA       string `json:"head_sha"`
	BaseSHA       string `json:"base_sha"`
	IncrementalOf string `json:"incremental_of,omitempty"`
	// Previous says what became of the previous review's open findings:
	// fixed, dismissed (thread resolved, code unchanged) or still open.
	Previous *PreviousFindings `json:"previous_findings,omitempty"`
	// Carried are earlier findings not reported again whose code didn't
	// change: not posted, but they count towards the scores.
	Carried []Finding `json:"-"`
	// open is what the summary's state keeps for the next review.
	open []PrevFinding
}

// allFindings are the new, overflow and repeated findings: everything still
// wrong with the change.
func (r *Result) allFindings() []Finding {
	out := append(append([]Finding{}, r.Findings...), r.Overflow...)
	return append(out, r.Repeated...)
}

// Input to a review run.
type Input struct {
	PR    *vcs.PR
	Files []diff.File
	Repo  config.RepoConfig
	// Content returns a file's new-side content for context; may be nil.
	Content func(ctx context.Context, path string) ([]byte, error)
	// OnlyPaths restricts review to these paths (incremental); nil = all.
	OnlyPaths map[string]bool
	// IncrementalFrom is the previously reviewed SHA, for reporting.
	IncrementalFrom string
	// PrevFileSummaries carries summaries of files not re-reviewed.
	PrevFileSummaries map[string]string
	// Known fingerprints of comments already on the PR.
	Known map[string]bool
	// Guidelines are repository guideline files (AGENTS.md and similar).
	Guidelines []Guideline
	// Existing are the bot's inline comments already on the PR; a finding
	// about the same code and topic counts as already posted.
	Existing []vcs.PostedComment
	// PrevScores are the previous review's scores, at PrevScoresSHA, to
	// anchor this review's scores to (nil scores from scratch). PrevOpen
	// is how many findings were open per score then (nil when unknown).
	PrevScores    *Scores
	PrevScoresSHA string
	PrevOpen      map[string]int
	// PrevFindings are the previous review's open findings, with Changed
	// set by the caller (nil when unknown: older summaries, --full).
	PrevFindings []PrevFinding
}

// Engine runs reviews.
type Engine struct {
	LLM llm.Client
	// VerifyLLM runs the verification pass; nil uses LLM. A different model
	// catches more of the first model's mistakes.
	VerifyLLM llm.Client
	Limits    config.Review
	Logger    *slog.Logger
}

type unit struct {
	file   *diff.File
	text   string
	tokens int
}

func estimateTokens(s string) int { return len(s)/4 + 1 }

// Run reviews the input. It returns an error only when nothing could be
// reviewed; partial failures are listed in Result.Errors.
func (e *Engine) Run(ctx context.Context, in Input) (*Result, error) {
	start := time.Now()
	log := e.Logger
	if log == nil {
		log = slog.Default()
	}
	res := &Result{FileSummaries: map[string]string{}, Model: e.LLM.Name()}
	if in.PR != nil {
		res.HeadSHA, res.BaseSHA = in.PR.HeadSHA, in.PR.BaseSHA
	}
	res.IncrementalOf = in.IncrementalFrom
	for _, g := range in.Guidelines {
		res.Guidelines = append(res.Guidelines, g.Path)
	}

	var files []*diff.File
	byPath := map[string]*diff.File{}
	for i := range in.Files {
		f := &in.Files[i]
		if r := skipReason(f, &in.Repo); r != "" {
			res.Skipped = append(res.Skipped, SkippedFile{f.Path, r})
			continue
		}
		if in.OnlyPaths != nil && !in.OnlyPaths[f.Path] {
			res.Unchanged = append(res.Unchanged, f.Path)
			if s, ok := in.PrevFileSummaries[f.Path]; ok {
				res.FileSummaries[f.Path] = s
			}
			continue
		}
		files = append(files, f)
		byPath[f.Path] = f
	}
	if len(files) > e.Limits.MaxFiles {
		res.TooLarge = true
		res.Summary = fmt.Sprintf("This pull request changes %d reviewable files, above the limit of %d, so it was not reviewed automatically. Split it up, narrow `path_filters`, or ask for a review of specific files.", len(files), e.Limits.MaxFiles)
		res.Duration = time.Since(start)
		return res, nil
	}
	if len(files) == 0 {
		res.Summary = "No reviewable changes."
		if len(res.Unchanged) > 0 {
			res.Summary = "No new reviewable changes since the last review."
		}
		res.Duration = time.Since(start)
		return res, nil
	}

	// Deterministic secret scan first.
	var findings []Finding
	for _, f := range files {
		findings = append(findings, scanSecrets(f)...)
	}

	contents := e.fetchContents(ctx, in, files)
	units := e.buildUnits(in, files, contents)
	batches := pack(units, e.Limits.BatchTokens)
	log.Info("reviewing", "files", len(files), "batches", len(batches), "model", e.LLM.Name())

	system := buildSystem(in.Repo, in.Guidelines)
	prHeader := prContext(in.PR)

	type batchResult struct {
		out   *reviewOutput
		usage llm.Usage
		err   error
	}
	// With review.passes > 1 each batch is reviewed several times
	// independently; the findings are merged (and deduplicated) below.
	passes := min(max(e.Limits.Passes, 1), 3)
	results := make([]batchResult, len(batches)*passes)
	sem := make(chan struct{}, max(e.Limits.Concurrency, 1))
	var wg sync.WaitGroup
	for j := range results {
		i, pass, b := j/passes, j%passes, batches[j/passes]
		wg.Add(1)
		go func(i, pass int, b []unit) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i*passes+pass].err = ctx.Err()
				return
			}
			defer func() { <-sem }()
			var user strings.Builder
			user.WriteString(prHeader)
			for _, u := range b {
				user.WriteString(u.text)
			}
			if pass == 0 {
				user.WriteString("\nReview the files above and submit your findings.")
			} else {
				user.WriteString("\nReview the files above independently and submit your findings. Another reviewer looked at them too; " +
					"assume they missed something: check every changed line for bugs, edge cases, error handling and security issues.")
			}
			resp, err := e.LLM.Complete(ctx, llm.Request{
				System:   system,
				Messages: []llm.Message{{Role: llm.User, Content: user.String()}},
				Schema:   reviewSchema,
			})
			if err != nil {
				results[i*passes+pass].err = err
				return
			}
			var out reviewOutput
			if err := json.Unmarshal(resp.JSON, &out); err != nil {
				results[i*passes+pass].err = fmt.Errorf("decode review JSON: %w", err)
				return
			}
			results[i*passes+pass] = batchResult{out: &out, usage: resp.Usage}
		}(i, pass, b)
	}
	wg.Wait()

	failed := 0 // batches where every pass failed
	for j, r := range results {
		i := j / passes
		if r.err != nil {
			batchFailed := true
			for k := i * passes; k < (i+1)*passes; k++ {
				batchFailed = batchFailed && results[k].err != nil
			}
			if batchFailed && j%passes == passes-1 {
				failed++
			}
			paths := make([]string, 0, len(batches[i]))
			for _, u := range batches[i] {
				paths = append(paths, u.file.Path)
			}
			label := fmt.Sprintf("batch %d", i+1)
			if passes > 1 {
				label += fmt.Sprintf(" pass %d", j%passes+1)
			}
			res.Errors = append(res.Errors, fmt.Sprintf("%s (%s): %v", label, strings.Join(uniq(paths), ", "), r.err))
			log.Error("review batch failed", "batch", i+1, "error", r.err)
			continue
		}
		res.Usage.InputTokens += r.usage.InputTokens
		res.Usage.OutputTokens += r.usage.OutputTokens
		res.Usage.CachedInputTokens += r.usage.CachedInputTokens
		for _, fs := range r.out.Files {
			p := normPath(fs.Path)
			if _, ok := byPath[p]; !ok || strings.TrimSpace(fs.Summary) == "" {
				continue
			}
			if prev := res.FileSummaries[p]; prev != "" && !strings.Contains(prev, fs.Summary) {
				res.FileSummaries[p] = prev + " " + strings.TrimSpace(fs.Summary)
			} else {
				res.FileSummaries[p] = strings.TrimSpace(fs.Summary)
			}
		}
		for _, c := range r.out.Comments {
			f, ok := byPath[normPath(c.Path)]
			if !ok {
				res.Dropped++
				continue
			}
			fd, ok := validate(f, rawComment{int(c.StartLine), int(c.EndLine), c.Severity, c.Category, c.Title, c.Body, c.Suggestion, float64(c.Confidence), c.Kind})
			if !ok {
				res.Dropped++
				continue
			}
			findings = append(findings, fd)
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if failed == len(batches) {
		return nil, fmt.Errorf("all %d review batches failed: %s", failed, strings.Join(res.Errors, "; "))
	}
	for _, f := range files {
		res.Reviewed = append(res.Reviewed, f.Path)
	}

	kept := e.filter(res, in, findings)
	if e.Limits.Verify == nil || *e.Limits.Verify {
		// Findings already posted on the PR are verified too: they count
		// towards the scores, so a speculative one must not slip through
		// just because an earlier run posted it.
		repeats := res.Repeated
		res.Repeated = nil
		for i := range repeats {
			repeats[i].repeat = true
		}
		verified := e.verify(ctx, res, in, byPath, contents, append(kept, repeats...))
		kept = kept[:0:0]
		for _, f := range verified {
			if f.repeat {
				res.Repeated = append(res.Repeated, f)
			} else {
				kept = append(kept, f)
			}
		}
		kept = e.filter(res, in, kept) // fingerprints and dedupes findings the verifier added
	}
	e.rankAndCap(res, in, kept)
	e.reconcile(res, in)
	e.walkthrough(ctx, res, in, files)
	res.open = res.openState(in.Files)
	res.Duration = time.Since(start)
	return res, nil
}

func (e *Engine) fetchContents(ctx context.Context, in Input, files []*diff.File) map[string]string {
	out := map[string]string{}
	if in.Content == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, f := range files {
		wg.Add(1)
		go func(f *diff.File) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := in.Content(ctx, f.Path)
			if err != nil || bytes.IndexByte(data, 0) >= 0 {
				return
			}
			if len(data) > 8<<20 { // context only; skip huge files
				return
			}
			mu.Lock()
			out[f.Path] = string(data)
			mu.Unlock()
		}(f)
	}
	wg.Wait()
	return out
}

func (e *Engine) buildUnits(in Input, files []*diff.File, contents map[string]string) []unit {
	budget := e.Limits.BatchTokens
	var units []unit
	for _, f := range files {
		instr := append(in.Repo.InstructionsFor(f.Path), nestedFor(in.Guidelines, f.Path)...)
		full := fileBlock(f, f.Hunks, instr, e.contextBlock(contents[f.Path], f.Hunks), "")
		if t := estimateTokens(full); t <= budget {
			units = append(units, unit{f, full, t})
			continue
		}
		// Too big: drop full content, then split by hunks.
		parts := splitHunks(f.Hunks, budget*4*3/4)
		for i, hs := range parts {
			label := ""
			if len(parts) > 1 {
				label = fmt.Sprintf("part %d of %d", i+1, len(parts))
			}
			// Keep a small window around this part's hunks if it fits.
			txt := fileBlock(f, hs, instr, excerptBlock(contents[f.Path], hs, 15), label)
			if estimateTokens(txt) > budget {
				txt = fileBlock(f, hs, instr, "", label)
			}
			units = append(units, unit{f, txt, estimateTokens(txt)})
		}
	}
	return units
}

// splitHunks groups hunks so each group renders under maxChars. A single
// oversized hunk is cut into line ranges.
func splitHunks(hunks []diff.Hunk, maxChars int) [][]diff.Hunk {
	var out [][]diff.Hunk
	var cur []diff.Hunk
	size := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur, size = nil, 0
		}
	}
	for _, h := range hunks {
		for _, piece := range cutHunk(h, maxChars) {
			ps := hunkChars(piece)
			if size+ps > maxChars {
				flush()
			}
			cur = append(cur, piece)
			size += ps
		}
	}
	flush()
	return out
}

func hunkChars(h diff.Hunk) int {
	n := 40
	for _, l := range h.Lines {
		n += len(l.Text) + 10
	}
	return n
}

func cutHunk(h diff.Hunk, maxChars int) []diff.Hunk {
	if hunkChars(h) <= maxChars {
		return []diff.Hunk{h}
	}
	var out []diff.Hunk
	cur := diff.Hunk{Section: h.Section}
	size := 40
	for _, l := range h.Lines {
		if size+len(l.Text)+10 > maxChars && len(cur.Lines) > 0 {
			out = append(out, finishPiece(cur))
			cur, size = diff.Hunk{Section: h.Section}, 40
		}
		cur.Lines = append(cur.Lines, l)
		size += len(l.Text) + 10
	}
	if len(cur.Lines) > 0 {
		out = append(out, finishPiece(cur))
	}
	return out
}

func finishPiece(h diff.Hunk) diff.Hunk {
	for _, l := range h.Lines {
		if l.NewNo > 0 && h.NewStart == 0 {
			h.NewStart = l.NewNo
		}
		if l.OldNo > 0 && h.OldStart == 0 {
			h.OldStart = l.OldNo
		}
		if l.Kind != diff.Del {
			h.NewLines++
		}
		if l.Kind != diff.Add {
			h.OldLines++
		}
	}
	return h
}

// contextBlock renders the new file for context: whole when it is small,
// otherwise numbered excerpts around the hunks.
func (e *Engine) contextBlock(content string, hunks []diff.Hunk) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) <= e.Limits.FullFileMaxLines {
		var b strings.Builder
		fmt.Fprintf(&b, "<full_content lines=\"%d\">\n", len(lines))
		for i, l := range lines {
			fmt.Fprintf(&b, "%5d | %s\n", i+1, l)
		}
		b.WriteString("</full_content>\n")
		return b.String()
	}
	return excerptBlock(content, hunks, e.Limits.ContextLines)
}

// excerptBlock renders numbered windows of n lines around each hunk's new
// range, merging overlapping windows.
func excerptBlock(content string, hunks []diff.Hunk, n int) string {
	if content == "" || n <= 0 || len(hunks) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	type span struct{ from, to int } // 1-based, inclusive
	var spans []span
	for _, h := range hunks {
		from := max(1, h.NewStart-n)
		to := min(len(lines), h.NewStart+max(h.NewLines, 1)-1+n)
		if from > to {
			continue
		}
		if k := len(spans) - 1; k >= 0 && from <= spans[k].to+1 {
			spans[k].to = max(spans[k].to, to)
			continue
		}
		spans = append(spans, span{from, to})
	}
	if len(spans) == 0 {
		return ""
	}
	var b strings.Builder
	ranges := make([]string, len(spans))
	for i, sp := range spans {
		ranges[i] = fmt.Sprintf("%d-%d", sp.from, sp.to)
	}
	fmt.Fprintf(&b, "<context_excerpt total_lines=\"%d\" ranges=\"%s\">\n", len(lines), strings.Join(ranges, ","))
	for i, sp := range spans {
		if i > 0 {
			b.WriteString("  ... |\n")
		}
		for ln := sp.from; ln <= sp.to; ln++ {
			fmt.Fprintf(&b, "%5d | %s\n", ln, lines[ln-1])
		}
	}
	b.WriteString("</context_excerpt>\n")
	return b.String()
}

func fileBlock(f *diff.File, hunks []diff.Hunk, instr []string, content, part string) string {
	var b strings.Builder
	attrs := fmt.Sprintf(`path=%q status=%q`, f.Path, f.Status)
	if f.Status == diff.Renamed {
		attrs += fmt.Sprintf(` old_path=%q`, f.OldPath)
	}
	if part != "" {
		attrs += fmt.Sprintf(` part=%q`, part)
	}
	fmt.Fprintf(&b, "\n<file %s>\n", attrs)
	if len(instr) > 0 {
		b.WriteString("<instructions>\n" + strings.Join(instr, "\n") + "\n</instructions>\n")
	}
	b.WriteString(content) // pre-rendered <full_content> or <context_excerpt>
	view := diff.File{Path: f.Path, Hunks: hunks}
	b.WriteString("<diff>\n" + view.Annotated() + "</diff>\n</file>\n")
	return b.String()
}

// pack groups units into batches under the token budget, keeping files
// from the same directory together.
func pack(units []unit, budget int) [][]unit {
	sort.SliceStable(units, func(i, j int) bool { return units[i].file.Path < units[j].file.Path })
	var out [][]unit
	var cur []unit
	size := 0
	for _, u := range units {
		if size+u.tokens > budget && len(cur) > 0 {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, u)
		size += u.tokens
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func prContext(pr *vcs.PR) string {
	if pr == nil {
		return ""
	}
	// The bot's own summary block is not the author's description.
	body := StripDescriptionBlock(pr.Body)
	if len(body) > 4000 {
		body = body[:4000] + "\n[truncated]"
	}
	return fmt.Sprintf("<pull_request>\n<title>%s</title>\n<description>\n%s\n</description>\n</pull_request>\n", pr.Title, body)
}

type rawComment struct {
	start, end           int
	severity, category   string
	title, body, suggest string
	confidence           float64
	kind                 string // "question" or "issue"
}

// validate maps a model comment onto the diff. The range must sit inside
// one hunk and touch an added line (or a pure-deletion hunk), so the bot
// never comments on code the change did not touch.
func validate(f *diff.File, c rawComment) (Finding, bool) {
	end, start := c.end, c.start
	if end <= 0 {
		end = start
	}
	if start <= 0 || start > end {
		start = end
	}
	h := f.HunkFor(end)
	if h == nil || h.LineAt(end) == nil {
		return Finding{}, false
	}
	keepSuggestion := true
	if start != end && (f.HunkFor(start) != h || h.LineAt(start) == nil) {
		start = end
		keepSuggestion = false
	}
	added := f.AddedLines()
	touches := false
	hunkHasAdds := false
	for _, l := range h.Lines {
		if l.Kind == diff.Add {
			hunkHasAdds = true
		}
	}
	for n := start; n <= end; n++ {
		if added[n] {
			touches = true
			break
		}
	}
	if !touches && hunkHasAdds {
		return Finding{}, false
	}
	title := strings.TrimSpace(c.title)
	body := strings.TrimSpace(c.body)
	if title == "" && body == "" {
		return Finding{}, false
	}
	endLine := h.LineAt(end)
	fd := Finding{
		Path: f.Path, OldPath: f.OldPath, StartLine: start, EndLine: end,
		Severity: ParseSeverity(c.severity), Category: normCategory(c.category),
		Title: title, Body: body, Confidence: clamp01(c.confidence), Source: "llm",
		lineText: endLine.Text,
	}
	if endLine.Kind == diff.Context {
		fd.OldLine = endLine.OldNo
	}
	if strings.EqualFold(strings.TrimSpace(c.kind), KindQuestion) {
		fd.Kind = KindQuestion
		if severityRank[fd.Severity] > severityRank[Minor] {
			fd.Severity = Minor // a question is not a defect until answered
		}
	}
	if keepSuggestion {
		fd.Suggestion = cleanSuggestion(c.suggest)
		// A suggestion identical to the current code is noise.
		if fd.Suggestion != "" && fd.Suggestion == currentText(h, start, end) {
			fd.Suggestion = ""
		}
	}
	return fd, true
}

func currentText(h *diff.Hunk, start, end int) string {
	var lines []string
	for n := start; n <= end; n++ {
		if l := h.LineAt(n); l != nil {
			lines = append(lines, l.Text)
		}
	}
	return strings.Join(lines, "\n")
}

func cleanSuggestion(s string) string {
	s = strings.TrimRight(s, " \n\t")
	if strings.HasPrefix(strings.TrimSpace(s), "```") {
		t := strings.TrimSpace(s)
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			t = t[nl+1:]
		} else {
			return ""
		}
		t = strings.TrimSuffix(strings.TrimRight(t, " \n"), "```")
		s = strings.TrimRight(t, "\n")
	}
	if strings.Contains(s, "```") {
		return "" // would break the fence
	}
	return s
}

func normCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "bug", "security", "performance", "reliability", "maintainability", "style", "docs", "tests":
		return c
	case "":
		return "bug"
	}
	return "maintainability"
}

func clamp01(v float64) float64 {
	if v <= 0 {
		return 0.6 // models that omit confidence
	}
	if v > 1 {
		return 1
	}
	return v
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

func fingerprint(f Finding) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", f.Path, f.Category, strings.ToLower(f.Title), strings.TrimSpace(f.lineText))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// thresholds per profile.
func minimums(rc config.RepoConfig) (Severity, float64) {
	sev, conf := Minor, 0.5
	switch rc.Profile {
	case config.Chill:
		sev, conf = Minor, 0.7
	case config.Assertive:
		sev, conf = Nitpick, 0.4
	}
	if rc.MinSeverity != "" {
		sev = ParseSeverity(rc.MinSeverity)
	}
	return sev, conf
}

// filter applies the profile thresholds, fingerprints findings, merges
// duplicates (keeping the most severe) and drops findings already posted.
func (e *Engine) filter(res *Result, in Input, findings []Finding) []Finding {
	minSev, minConf := minimums(in.Repo)
	best := map[string]Finding{}
	var order []string
	for _, f := range findings {
		if f.Source != "secret-scan" && f.Fingerprint == "" && (!f.Severity.AtLeast(minSev) || f.Confidence < minConf) {
			res.Dropped++
			continue
		}
		if f.Fingerprint == "" {
			f.Fingerprint = fingerprint(f)
		}
		if prev, ok := best[f.Fingerprint]; ok {
			if severityRank[f.Severity] > severityRank[prev.Severity] {
				best[f.Fingerprint] = f
			}
			continue
		}
		best[f.Fingerprint] = f
		order = append(order, f.Fingerprint)
	}
	var kept []Finding
	for _, fp := range order {
		f := best[fp]
		if in.Known[fp] || postedBefore(f, in.Existing) {
			res.AlreadyPosted++
			// Still present: not posted again, but it counts for the scores.
			res.Repeated = append(res.Repeated, f)
			continue
		}
		kept = append(kept, f)
	}
	return mergeOverlapping(res, kept)
}

// postedBefore reports whether an earlier bot comment covers the finding:
// same file, within 2 lines, same topic (titles are worded differently from
// run to run, so the exact fingerprint is not enough).
func postedBefore(f Finding, existing []vcs.PostedComment) bool {
	for _, e := range existing {
		if e.Path != f.Path {
			continue
		}
		start := f.StartLine
		if start <= 0 {
			start = f.EndLine
		}
		if e.Line < start-2 || e.Line > f.EndLine+2 {
			continue
		}
		kind := ""
		if e.Question {
			kind = KindQuestion
		}
		if sameTopic(f, Finding{Path: e.Path, StartLine: e.Line, EndLine: e.Line, Title: e.Title, Kind: kind}) {
			return true
		}
	}
	return false
}

// mergeOverlapping folds findings that point at the same code (same file,
// line ranges within 2 lines) into one: an issue beats a question, then the
// higher severity, then the higher confidence. Two phrasings of one problem
// would otherwise be posted twice.
func mergeOverlapping(res *Result, fs []Finding) []Finding {
	near := func(a, b Finding) bool {
		as, bs := max(a.StartLine, 1), max(b.StartLine, 1)
		if a.StartLine == 0 {
			as = a.EndLine
		}
		if b.StartLine == 0 {
			bs = b.EndLine
		}
		return a.Path == b.Path && as <= b.EndLine+2 && bs <= a.EndLine+2
	}
	better := func(a, b Finding) bool {
		if (a.Kind == KindQuestion) != (b.Kind == KindQuestion) {
			return b.Kind == KindQuestion
		}
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] > severityRank[b.Severity]
		}
		return a.Confidence >= b.Confidence
	}
	var out []Finding
	for _, f := range fs {
		merged := false
		for i := range out {
			// Secret-scan hits and model findings about different things on
			// one line both stay; merge only same-source-type duplicates.
			if near(out[i], f) && (out[i].Source == "secret-scan") == (f.Source == "secret-scan") && sameTopic(out[i], f) {
				if better(f, out[i]) {
					out[i] = f
				}
				res.Dropped++
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, f)
		}
	}
	return out
}

// sameTopic reports whether two nearby findings are about the same problem:
// their titles share two significant word stems (one if either is a
// question), or they share category and line.
// Distinct bugs on adjacent lines must stay separate.
func sameTopic(a, b Finding) bool {
	if a.Category != "" && a.Category == b.Category && a.EndLine == b.EndLine {
		return true
	}
	stems := map[string]bool{}
	for _, w := range titleStems(a.Title) {
		stems[w] = true
	}
	shared := 0
	for _, w := range titleStems(b.Title) {
		if stems[w] {
			shared++
		}
	}
	// A question that restates a nearby issue needs only one shared stem.
	if a.Kind == KindQuestion || b.Kind == KindQuestion {
		return shared >= 1
	}
	return shared >= 2
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "from": true, "into": true, "when": true, "this": true, "that": true, "before": true, "after": true, "does": true, "missing": true}

// titleStems returns crude stems: lower-cased words of 4+ letters, cut to 6
// letters ("overwritten", "overwrite" -> "overwr").
func titleStems(t string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return r < 'a' || r > 'z' }) {
		if len(w) < 4 || stopWords[w] {
			continue
		}
		if len(w) > 6 {
			w = w[:6]
		}
		out = append(out, w)
	}
	return out
}

// rankAndCap orders findings (issues before questions, most severe and most
// confident first) and splits them at max_comments.
func (e *Engine) rankAndCap(res *Result, in Input, kept []Finding) {
	// Most severe and most confident first, so the cap keeps the best.
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if (a.Kind == KindQuestion) != (b.Kind == KindQuestion) {
			return b.Kind == KindQuestion
		}
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] > severityRank[b.Severity]
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.StartLine < b.StartLine
	})
	limit := in.Repo.MaxComments
	if limit <= 0 {
		limit = len(kept)
	}
	if len(kept) > limit {
		res.Findings, res.Overflow = kept[:limit], kept[limit:]
	} else {
		res.Findings = kept
	}
}

func (e *Engine) walkthrough(ctx context.Context, res *Result, in Input, files []*diff.File) {
	var b strings.Builder
	b.WriteString(prContext(in.PR))
	b.WriteString("\n<files>\n")
	paths := make([]string, 0, len(res.FileSummaries))
	for p := range res.FileSummaries {
		paths = append(paths, p)
	}
	for _, f := range files {
		if _, ok := res.FileSummaries[f.Path]; !ok {
			paths = append(paths, f.Path)
		}
	}
	sort.Strings(paths)
	stats := map[string]string{}
	for i := range in.Files {
		f := &in.Files[i]
		stats[f.Path] = fmt.Sprintf("+%d/-%d", f.AddedCount(), f.DeletedCount())
	}
	for _, p := range uniq(paths) {
		fmt.Fprintf(&b, "- %s (%s): %s\n", p, stats[p], res.FileSummaries[p])
	}
	b.WriteString("</files>\n<findings>\n")
	for _, f := range res.allFindings() {
		kind := f.Category
		if f.Kind == KindQuestion {
			kind = "question"
		}
		fmt.Fprintf(&b, "- [%s/%s] %s:%d %s\n", f.Severity, kind, f.Path, f.EndLine, f.Title)
	}
	for _, f := range res.Carried {
		fmt.Fprintf(&b, "- [%s/%s] %s:%d %s (from an earlier review, not fixed: its code is unchanged)\n", f.Severity, f.Category, f.Path, f.EndLine, f.Title)
	}
	b.WriteString("</findings>\n")
	if s := strings.TrimSpace(in.Repo.HighLevelSummaryInstructions); s != "" {
		b.WriteString("\nInstructions for the summary and highlights (from the repository config): " + s + "\n")
	}
	if s := strings.TrimSpace(in.Repo.AutoTitleInstructions); s != "" {
		b.WriteString("\nInstructions for the title (from the repository config): " + s + "\n")
	}
	if in.PR != nil && in.PR.Title != "" {
		b.WriteString("\nCurrent PR title: " + in.PR.Title + "\n")
	}
	if p := in.PrevScores; p != nil {
		fmt.Fprintf(&b, "\nPrevious scores (at %s): quality %d, security %d, maintainability %d. Keep each score unless this update changes it; "+
			"when you change a score, say why in score_reasons in a few words.\n", shortSHA(in.PrevScoresSHA), p.Quality, p.Security, p.Maintainability)
	}
	if in.Repo.Poem != nil && *in.Repo.Poem {
		b.WriteString("\nInclude a poem.")
	} else {
		b.WriteString("\nDo not include a poem (empty string).")
	}
	system := summarySystem
	if in.Repo.Language != "" && !strings.EqualFold(in.Repo.Language, "english") {
		system += "\nWrite in " + in.Repo.Language + "."
	}
	resp, err := e.LLM.Complete(ctx, llm.Request{
		System:    system,
		Messages:  []llm.Message{{Role: llm.User, Content: b.String()}},
		Schema:    summarySchema,
		MaxTokens: 4096,
	})
	var out summaryOutput
	var dropped []string
	if err == nil {
		out, dropped, err = decodeSummary(resp.JSON)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			res.Errors = append(res.Errors, "walkthrough: "+err.Error())
		}
		res.Summary = fmt.Sprintf("Reviewed %d file(s).", len(res.Reviewed))
		res.Cohorts = defaultCohorts(paths, res.FileSummaries)
		e.noScores(res, in, "the walkthrough failed")
		return
	}
	if len(dropped) > 0 {
		log := e.Logger
		if log == nil {
			log = slog.Default()
		}
		log.Warn("walkthrough: ignored fields the model got wrong", "fields", dropped)
	}
	res.Usage.InputTokens += resp.Usage.InputTokens
	res.Usage.OutputTokens += resp.Usage.OutputTokens
	res.Usage.CachedInputTokens += resp.Usage.CachedInputTokens
	res.Summary = strings.TrimSpace(out.Summary)
	res.Cohorts = fixCohorts(out.Cohorts, paths, res.FileSummaries)
	res.Effort = max(1, min(5, int(out.Effort)))
	res.Poem = strings.TrimSpace(out.Poem)
	res.SuggestedTitle = strings.TrimSpace(strings.SplitN(out.Title, "\n", 2)[0])
	if len(res.SuggestedTitle) > 100 {
		res.SuggestedTitle = res.SuggestedTitle[:100]
	}
	res.TitleAccurate = out.TitleOK == nil || *out.TitleOK
	res.DescriptionCheck = strings.TrimSpace(out.DescriptionCheck)
	for _, h := range out.Highlights {
		var items []string
		for _, it := range h.Items {
			if it = strings.TrimSpace(it); it != "" {
				items = append(items, it)
			}
		}
		if len(items) > 0 {
			res.Highlights = append(res.Highlights, Highlight{Type: h.Type, Items: items})
		}
	}
	if out.Scores.Quality < 1 || out.Scores.Security < 1 || out.Scores.Maintainability < 1 {
		e.noScores(res, in, "the walkthrough gave no scores")
		return
	}
	sc := finalScores(Scores{Quality: int(out.Scores.Quality), Security: int(out.Scores.Security), Maintainability: int(out.Scores.Maintainability)}, res.scoreFindings())
	res.Scores = &sc
	e.anchor(res, in, map[string]string{"quality": out.ScoreReasons.Quality, "security": out.ScoreReasons.Security, "maintainability": out.ScoreReasons.Maintainability})
}

// noScores leaves the review without scores rather than guessing them
// from the findings (a guess dragged quality to 1 on large changes). The
// previous review's scores are kept in the state for the next review.
func (e *Engine) noScores(res *Result, in Input, why string) {
	res.Scores, res.ScoresNote = nil, why
	if in.PrevScores != nil {
		res.ScoresNote += "; scores are unchanged from the last review"
		res.carry = &carriedScores{Scores: *in.PrevScores, SHA: in.PrevScoresSHA, Open: in.PrevOpen}
	}
}

// anchor moves res.Scores from the previous review's scores only as far
// as the change supports (see anchorScores). The same commit reviewed
// again keeps the previous scores.
func (e *Engine) anchor(res *Result, in Input, reasons map[string]string) {
	prev := in.PrevScores
	if prev == nil || res.Scores == nil {
		return
	}
	if in.PR != nil && in.PrevScoresSHA != "" && in.PrevScoresSHA == in.PR.HeadSHA {
		sc := *prev
		res.Scores = &sc
		return
	}
	prevOpen := in.PrevOpen
	if in.PrevFindings != nil { // counted the same way as now
		var fs []Finding
		for _, p := range in.PrevFindings {
			fs = append(fs, Finding{Severity: p.Severity, Category: p.Category})
		}
		prevOpen = OpenFindings(fs)
	}
	sc, changes := anchorScores(*prev, *res.Scores, res.scoreFindings(), prevOpen, reasons, res.Previous)
	res.Scores, res.ScoreChanges = &sc, changes
}

func shortSHA(s string) string {
	if s == "" {
		return "the last review"
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// fixCohorts drops unknown files and adds files the model left out.
func fixCohorts(cs []Cohort, paths []string, sums map[string]string) []Cohort {
	known := map[string]bool{}
	for _, p := range paths {
		known[p] = true
	}
	seen := map[string]bool{}
	var out []Cohort
	for _, c := range cs {
		var fs []string
		for _, f := range c.Files {
			f = normPath(f)
			if known[f] && !seen[f] {
				seen[f] = true
				fs = append(fs, f)
			}
		}
		if len(fs) > 0 {
			c.Files = fs
			out = append(out, c)
		}
	}
	var rest []string
	for _, p := range paths {
		if !seen[p] {
			rest = append(rest, p)
		}
	}
	return append(out, defaultCohorts(rest, sums)...)
}

func defaultCohorts(paths []string, sums map[string]string) []Cohort {
	var out []Cohort
	for _, p := range paths {
		out = append(out, Cohort{Label: p, Files: []string{p}, Summary: sums[p]})
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
