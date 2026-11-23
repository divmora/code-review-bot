package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/divmora/code-review-bot/pkg/vcs"
)

var severityBadge = map[Severity]string{
	Critical: "🔴 Critical",
	Major:    "🟠 Major",
	Minor:    "🟡 Minor",
	Nitpick:  "🔵 Nitpick",
}

var categoryLabel = map[string]string{
	"bug": "⚠️ Potential issue", "security": "🛡️ Security", "performance": "⚡ Performance",
	"reliability": "🧯 Reliability", "maintainability": "🛠️ Refactor suggestion", "style": "🧹 Style",
	"docs": "📝 Docs", "tests": "🧪 Tests",
}

// CommentBody renders a finding as an inline comment for a platform.
func CommentBody(f Finding, platform string) string {
	var b strings.Builder
	if f.Kind == KindQuestion {
		b.WriteString("_❓ Question_ | _" + severityBadge[f.Severity] + "_\n\n")
	} else {
		fmt.Fprintf(&b, "_%s_ | _%s_\n\n", firstNonEmpty(categoryLabel[f.Category], "⚠️ Potential issue"), severityBadge[f.Severity])
	}
	fmt.Fprintf(&b, "**%s**\n\n", escapeInline(f.Title))
	if f.Body != "" {
		b.WriteString(f.Body + "\n\n")
	}
	if f.Suggestion != "" {
		b.WriteString(vcs.SuggestionFence(platform, f.StartLine, f.EndLine) + "\n")
		b.WriteString(f.Suggestion + "\n```\n\n")
	}
	if f.Source == "secret-scan" {
		b.WriteString("<sub>Found by the built-in secret scanner.</sub>\n\n")
	}
	b.WriteString(vcs.FingerprintTag(f.Fingerprint))
	return b.String()
}

// ToComments converts findings into inline comments.
func ToComments(fs []Finding, platform string) []vcs.Comment {
	out := make([]vcs.Comment, 0, len(fs))
	for _, f := range fs {
		out = append(out, vcs.Comment{
			Path: f.Path, OldPath: f.OldPath, StartLine: f.StartLine, Line: f.EndLine, OldLine: f.OldLine,
			Body: CommentBody(f, platform), Fingerprint: f.Fingerprint,
		})
	}
	return out
}

// State is persisted in the summary comment between runs.
type State struct {
	FileSummaries map[string]string `json:"file_summaries"`
	// Scores are this review's scores at ScoresSHA, and OpenFindings the
	// findings then open per score, which the next review anchors to.
	Scores       *Scores        `json:"scores,omitempty"`
	ScoresSHA    string         `json:"scores_sha,omitempty"`
	OpenFindings map[string]int `json:"open_findings,omitempty"`
}

// summaryScoresRe reads the scores row of an older summary's table.
var summaryScoresRe = regexp.MustCompile(`## Scores\s*\n\s*\| Quality \| Security \| Maintainability \|\s*\n[^\n]*\n\|[^|0-9]*\*\*(\d{1,3})\*\*[^|]*\|[^|0-9]*\*\*(\d{1,3})\*\*[^|]*\|[^|0-9]*\*\*(\d{1,3})\*\*`)

// SummaryScores reads the scores from a summary comment written before
// they were kept in its state.
func SummaryScores(body string) (*Scores, bool) {
	m := summaryScoresRe.FindStringSubmatch(body)
	if m == nil {
		return nil, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil || n < 1 || n > 100 {
			return nil, false
		}
		v[i] = n
	}
	return &Scores{Quality: v[0], Security: v[1], Maintainability: v[2]}, true
}

// SummaryOptions tweak the summary comment.
type SummaryOptions struct {
	BotName string
	// Rejected are findings the host refused inline; listed in the body.
	Rejected []Finding
	// Posted is how many inline comments were published.
	Posted int
	// Commands shows the @bot command help in the footer; set it only when
	// a webhook service answers those commands.
	Commands bool
	// TitlePlaceholder is mentioned next to a suggested title.
	TitlePlaceholder string
}

// Markers around the bot's block in the PR description.
const (
	DescriptionStart = "<!-- codereview:description:start -->"
	DescriptionEnd   = "<!-- codereview:description:end -->"
)

// DescriptionBlock renders the "Summary by codereview" block for the PR
// description, or "" when there is nothing to say.
func DescriptionBlock(res *Result, bot string) string {
	if len(res.Highlights) == 0 && strings.TrimSpace(res.Summary) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(DescriptionStart + "\n")
	fmt.Fprintf(&b, "## Summary by %s\n\n", firstNonEmpty(bot, "codereview"))
	if len(res.Highlights) == 0 {
		b.WriteString(strings.TrimSpace(res.Summary) + "\n")
	}
	for _, h := range res.Highlights {
		fmt.Fprintf(&b, "* **%s**\n", h.Type)
		for _, it := range h.Items {
			b.WriteString("  * " + escapeInline(it) + "\n")
		}
	}
	b.WriteString(DescriptionEnd)
	return b.String()
}

// ApplyDescription returns the new PR description, and whether it changed:
// the bot's previous block is replaced, else the placeholder, else the block
// is appended when autoAppend is set. Text outside the block is never changed.
func ApplyDescription(current, block, placeholder string, autoAppend bool) (string, bool) {
	if block == "" {
		return current, false
	}
	if i := strings.Index(current, DescriptionStart); i >= 0 {
		if j := strings.Index(current[i:], DescriptionEnd); j >= 0 {
			next := current[:i] + block + current[i+j+len(DescriptionEnd):]
			return next, next != current
		}
	}
	if placeholder != "" && strings.Contains(current, placeholder) {
		return strings.Replace(current, placeholder, block, 1), true
	}
	if !autoAppend {
		return current, false
	}
	if strings.TrimSpace(current) == "" {
		return block, true
	}
	return strings.TrimRight(current, "\n") + "\n\n" + block, true
}

// StripDescriptionBlock removes the bot's block from a PR description.
func StripDescriptionBlock(body string) string {
	i := strings.Index(body, DescriptionStart)
	if i < 0 {
		return body
	}
	j := strings.Index(body[i:], DescriptionEnd)
	if j < 0 {
		return body
	}
	return strings.TrimSpace(body[:i] + body[i+j+len(DescriptionEnd):])
}

// ApplyTitle returns the generated title when the current title contains the
// placeholder (case-insensitive); the title is never changed otherwise.
func ApplyTitle(current, suggested, placeholder string) (string, bool) {
	if placeholder == "" || suggested == "" || !strings.Contains(strings.ToLower(current), strings.ToLower(placeholder)) {
		return current, false
	}
	return suggested, suggested != current
}

var effortLabel = map[int]string{1: "Trivial", 2: "Simple", 3: "Moderate", 4: "Complex", 5: "Very complex"}
var effortMinutes = map[int]string{1: "~5", 2: "~10", 3: "~20", 4: "~45", 5: "~90"}

// SummaryMarkdown renders the walkthrough comment.
func SummaryMarkdown(res *Result, opts SummaryOptions) string {
	var b strings.Builder
	b.WriteString(vcs.SummaryMarker + "\n")
	b.WriteString("## Walkthrough\n\n")
	b.WriteString(strings.TrimSpace(res.Summary) + "\n\n")
	if len(res.ConfigChanges) > 0 {
		b.WriteString("> [!NOTE]\n> This PR changes the review configuration (" + codeList(res.ConfigChanges) + "). ")
		b.WriteString("Reviews use the version on the target branch, so the new rules apply after this PR merges.\n\n")
	}

	if len(res.Cohorts) > 0 {
		b.WriteString("## Changes\n\n| Cohort / File(s) | Summary |\n|---|---|\n")
		for _, c := range res.Cohorts {
			files := make([]string, len(c.Files))
			for i, f := range c.Files {
				files[i] = "`" + escapeCell(f) + "`"
			}
			label := escapeCell(c.Label)
			if len(c.Files) == 1 && c.Label == c.Files[0] {
				fmt.Fprintf(&b, "| %s | %s |\n", files[0], escapeCell(c.Summary))
			} else {
				fmt.Fprintf(&b, "| **%s**<br>%s | %s |\n", label, strings.Join(files, ", "), escapeCell(c.Summary))
			}
		}
		b.WriteString("\n")
	}
	if res.DescriptionCheck != "" || (!res.TitleAccurate && res.SuggestedTitle != "") {
		b.WriteString("## 📝 Description check\n\n")
		if res.DescriptionCheck != "" {
			b.WriteString(escapeInline(res.DescriptionCheck) + "\n\n")
		}
		if !res.TitleAccurate && res.SuggestedTitle != "" {
			fmt.Fprintf(&b, "Suggested title: `%s`", strings.ReplaceAll(res.SuggestedTitle, "`", "'"))
			if opts.TitlePlaceholder != "" {
				fmt.Fprintf(&b, " (put `%s` in the title to apply it)", opts.TitlePlaceholder)
			}
			b.WriteString("\n\n")
		}
	}
	if res.Scores != nil {
		b.WriteString("## Scores\n\n| Quality | Security | Maintainability |\n|:---:|:---:|:---:|\n")
		fmt.Fprintf(&b, "| %s | %s | %s |\n\n", scoreCell(res.Scores.Quality), scoreCell(res.Scores.Security), scoreCell(res.Scores.Maintainability))
		if len(res.ScoreChanges) > 0 {
			parts := make([]string, 0, len(res.ScoreChanges))
			for _, c := range res.ScoreChanges {
				parts = append(parts, fmt.Sprintf("%s %d → %d (%s)", c.Name, c.From, c.To, c.Reason))
			}
			b.WriteString("Since the last review: " + strings.Join(parts, " · ") + "\n\n")
		}
	}
	if res.Effort > 0 {
		fmt.Fprintf(&b, "## Estimated code review effort\n\n🎯 %d (%s) | ⏱️ %s minutes\n\n", res.Effort, effortLabel[res.Effort], effortMinutes[res.Effort])
	}
	if res.Poem != "" {
		b.WriteString("## Poem\n\n")
		for _, l := range strings.Split(res.Poem, "\n") {
			b.WriteString("> " + l + "\n")
		}
		b.WriteString("\n")
	}

	extra := append(append([]Finding{}, opts.Rejected...), res.Overflow...)
	if len(extra) > 0 {
		fmt.Fprintf(&b, "<details>\n<summary>📌 Additional comments (%d)</summary>\n\n", len(extra))
		b.WriteString("These could not be posted inline (outside the diff or beyond the comment limit).\n\n")
		for _, f := range extra {
			fmt.Fprintf(&b, "- **%s** `%s:%s` — %s\n", severityBadge[f.Severity], f.Path, lineRange(f), escapeInline(f.Title))
			if f.Body != "" {
				b.WriteString("  " + strings.ReplaceAll(firstParagraph(f.Body), "\n", " ") + "\n")
			}
		}
		b.WriteString("\n</details>\n\n")
	}

	b.WriteString("<details>\n<summary>📋 Review details</summary>\n\n")
	fmt.Fprintf(&b, "- **Model:** `%s`\n", res.Model)
	if res.HeadSHA != "" {
		if res.IncrementalOf != "" {
			fmt.Fprintf(&b, "- **Commits:** reviewed changes from `%s` to `%s` (incremental)\n", short(res.IncrementalOf), short(res.HeadSHA))
		} else {
			fmt.Fprintf(&b, "- **Commit:** `%s` (base `%s`)\n", short(res.HeadSHA), short(res.BaseSHA))
		}
	}
	fmt.Fprintf(&b, "- **Files reviewed:** %d", len(res.Reviewed))
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(&b, " · unchanged since last review: %d", len(res.Unchanged))
	}
	fmt.Fprintf(&b, " · skipped: %d\n", len(res.Skipped))
	fmt.Fprintf(&b, "- **Comments:** %d posted", opts.Posted)
	if res.AlreadyPosted > 0 {
		fmt.Fprintf(&b, " · %d already posted earlier", res.AlreadyPosted)
	}
	if res.Dropped > 0 {
		fmt.Fprintf(&b, " · %d filtered out", res.Dropped)
	}
	b.WriteString("\n")
	if v := res.Verification; v != nil && v.Ran {
		fmt.Fprintf(&b, "- **Verification** (`%s`): %d confirmed · %d false positives removed · %d turned into questions · %d added\n", v.Model, v.Confirmed, v.FalsePositives, v.Questions, v.Added)
	}
	if len(res.Guidelines) > 0 {
		names := make([]string, len(res.Guidelines))
		for i, g := range res.Guidelines {
			names[i] = "`" + g + "`"
		}
		fmt.Fprintf(&b, "- **Guidelines:** %s\n", strings.Join(names, ", "))
	}
	if res.Usage.InputTokens > 0 {
		fmt.Fprintf(&b, "- **Tokens:** %d in / %d out\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	if v := res.Verification; v != nil && len(v.Removed) > 0 {
		b.WriteString("\n<details>\n<summary>Removed by verification</summary>\n\n")
		for _, r := range v.Removed {
			fmt.Fprintf(&b, "- `%s:%d` %s (%s): %s\n", r.Path, r.Line, escapeInline(r.Title), r.Was, escapeInline(r.Reason))
		}
		b.WriteString("\n</details>\n")
	}
	if len(res.Skipped) > 0 {
		b.WriteString("\n<details>\n<summary>Skipped files</summary>\n\n")
		for _, s := range res.Skipped {
			fmt.Fprintf(&b, "- `%s` — %s\n", s.Path, s.Reason)
		}
		b.WriteString("\n</details>\n")
	}
	if len(res.Errors) > 0 {
		b.WriteString("\n**Warnings** (the review may be incomplete):\n")
		for _, e := range res.Errors {
			fmt.Fprintf(&b, "- %s\n", escapeInline(truncate(e, 300)))
		}
	}
	b.WriteString("\n</details>\n\n")

	if opts.Commands {
		bot := firstNonEmpty(opts.BotName, "codereview")
		fmt.Fprintf(&b, "---\n<sub>💬 Commands: `@%[1]s review` (new changes) · `@%[1]s full review` · `@%[1]s help` · or ask `@%[1]s <question>`</sub>\n\n", bot)
	} else {
		b.WriteString("---\n<sub>Reviewed by code-review-bot</sub>\n\n")
	}
	if res.HeadSHA != "" {
		b.WriteString(vcs.ReviewedSHATag(res.HeadSHA) + "\n")
	}
	state := State{FileSummaries: res.FileSummaries}
	if res.Scores != nil {
		state.Scores, state.ScoresSHA, state.OpenFindings = res.Scores, res.HeadSHA, OpenFindings(res.allFindings())
	}
	st, _ := json.Marshal(state)
	b.WriteString(vcs.StateTag(st) + "\n")
	return b.String()
}

// ReviewHeader is the body of the GitHub review that carries inline comments.
func ReviewHeader(n int) string {
	return fmt.Sprintf("**Actionable comments posted: %d**", n)
}

// Markdown renders a standalone report (CLI output).
func Markdown(res *Result) string {
	var b strings.Builder
	b.WriteString("# Code review\n\n")
	b.WriteString(strings.TrimSpace(res.Summary) + "\n\n")
	if res.Scores != nil {
		fmt.Fprintf(&b, "| Quality | Security | Maintainability |\n|:---:|:---:|:---:|\n| %s | %s | %s |\n\n",
			scoreCell(res.Scores.Quality), scoreCell(res.Scores.Security), scoreCell(res.Scores.Maintainability))
	}
	all := append(append([]Finding{}, res.Findings...), res.Overflow...)
	if len(all) == 0 {
		b.WriteString("No issues found. ✅\n\n")
	}
	byFile := map[string][]Finding{}
	var files []string
	for _, f := range all {
		if _, ok := byFile[f.Path]; !ok {
			files = append(files, f.Path)
		}
		byFile[f.Path] = append(byFile[f.Path], f)
	}
	sort.Strings(files)
	for _, p := range files {
		fmt.Fprintf(&b, "## `%s`\n\n", p)
		fs := byFile[p]
		sort.Slice(fs, func(i, j int) bool { return fs[i].StartLine < fs[j].StartLine })
		for _, f := range fs {
			cat := f.Category
			if f.Kind == KindQuestion {
				cat = "❓ question"
			}
			fmt.Fprintf(&b, "### L%s · %s · %s\n\n**%s**\n\n%s\n\n", lineRange(f), severityBadge[f.Severity], cat, f.Title, f.Body)
			if f.Suggestion != "" {
				b.WriteString("Suggested change:\n\n```\n" + f.Suggestion + "\n```\n\n")
			}
		}
	}
	if len(res.Skipped) > 0 {
		b.WriteString("## Skipped files\n\n")
		for _, s := range res.Skipped {
			fmt.Fprintf(&b, "- `%s` — %s\n", s.Path, s.Reason)
		}
		b.WriteString("\n")
	}
	if len(res.Errors) > 0 {
		b.WriteString("## Warnings\n\n")
		for _, e := range res.Errors {
			b.WriteString("- " + e + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "---\nModel `%s` · %d files reviewed · %d in / %d out tokens · %s\n", res.Model, len(res.Reviewed), res.Usage.InputTokens, res.Usage.OutputTokens, res.Duration.Round(1e8))
	return b.String()
}

// Text renders a compact terminal report.
func Text(res *Result, color bool) string {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	sevColor := map[Severity]string{Critical: "1;31", Major: "33", Minor: "36", Nitpick: "2"}
	var b strings.Builder
	b.WriteString(paint("1", "Summary") + "\n" + strings.TrimSpace(res.Summary) + "\n\n")
	if sc := res.Scores; sc != nil {
		bandColor := map[string]string{"green": "32", "orange": "33", "red": "31"}
		var parts []string
		for _, n := range ScoreNames {
			v := sc.Get(n)
			parts = append(parts, n+" "+paint(bandColor[ScoreBand(v)], fmt.Sprint(v)))
		}
		b.WriteString(paint("1", "Scores") + "  " + strings.Join(parts, "  ") + "\n\n")
	}
	all := append(append([]Finding{}, res.Findings...), res.Overflow...)
	if len(all) == 0 {
		b.WriteString(paint("32", "No issues found.") + "\n")
	}
	for _, f := range all {
		label := strings.ToUpper(string(f.Severity))
		if f.Kind == KindQuestion {
			label = "QUESTION"
		}
		fmt.Fprintf(&b, "%s %s:%s %s\n", paint(sevColor[f.Severity], label), f.Path, lineRange(f), paint("1", f.Title))
		for _, l := range strings.Split(strings.TrimSpace(f.Body), "\n") {
			b.WriteString("    " + l + "\n")
		}
		if f.Suggestion != "" {
			b.WriteString(paint("2", "    suggestion:") + "\n")
			for _, l := range strings.Split(f.Suggestion, "\n") {
				b.WriteString(paint("32", "    + "+l) + "\n")
			}
		}
		b.WriteString("\n")
	}
	for _, e := range res.Errors {
		b.WriteString(paint("33", "warning: ") + e + "\n")
	}
	if len(res.Guidelines) > 0 {
		fmt.Fprintf(&b, "%s\n", paint("2", "guidelines: "+strings.Join(res.Guidelines, ", ")))
	}
	if res.DescriptionCheck != "" {
		b.WriteString(paint("33", "description check: ") + res.DescriptionCheck + "\n")
	}
	if !res.TitleAccurate && res.SuggestedTitle != "" {
		b.WriteString(paint("33", "suggested title: ") + res.SuggestedTitle + "\n")
	}
	if v := res.Verification; v != nil && v.Ran {
		for _, r := range v.Removed {
			fmt.Fprintf(&b, "%s\n", paint("2", fmt.Sprintf("removed by verification: %s:%d %s (%s): %s", r.Path, r.Line, r.Title, r.Was, r.Reason)))
		}
		fmt.Fprintf(&b, "%s\n", paint("2", fmt.Sprintf("verification (%s): %d confirmed, %d false positives removed, %d questions, %d added", v.Model, v.Confirmed, v.FalsePositives, v.Questions, v.Added)))
	}
	if len(res.ConfigChanges) > 0 {
		fmt.Fprintf(&b, "%s\n", paint("33", "note: this PR changes review config ("+strings.Join(res.ConfigChanges, ", ")+"); the target branch version was used"))
	}
	fmt.Fprintf(&b, "%s\n", paint("2", fmt.Sprintf("%d files reviewed, %d skipped · %s · %d in / %d out tokens · %s",
		len(res.Reviewed), len(res.Skipped), res.Model, res.Usage.InputTokens, res.Usage.OutputTokens, res.Duration.Round(1e8))))
	return b.String()
}

// SARIF renders findings as SARIF 2.1.0 for code-scanning dashboards.
func SARIF(res *Result) ([]byte, error) {
	type loc struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
				EndLine   int `json:"endLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	}
	level := map[Severity]string{Critical: "error", Major: "error", Minor: "warning", Nitpick: "note"}
	var results []map[string]any
	for _, f := range append(append([]Finding{}, res.Findings...), res.Overflow...) {
		var l loc
		l.PhysicalLocation.ArtifactLocation.URI = f.Path
		l.PhysicalLocation.Region.StartLine = f.StartLine
		l.PhysicalLocation.Region.EndLine = f.EndLine
		results = append(results, map[string]any{
			"ruleId":              f.Category,
			"level":               level[f.Severity],
			"message":             map[string]string{"text": f.Title + "\n\n" + f.Body},
			"locations":           []loc{l},
			"partialFingerprints": map[string]string{"codereview/v1": f.Fingerprint},
		})
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool":    map[string]any{"driver": map[string]any{"name": "code-review-bot", "informationUri": "https://github.com/divmora/code-review-bot"}},
			"results": results,
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}

var bandEmoji = map[string]string{"green": "🟢", "orange": "🟠", "red": "🔴"}

func scoreCell(v int) string {
	return fmt.Sprintf("%s **%d**", bandEmoji[ScoreBand(v)], v)
}

func codeList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

func lineRange(f Finding) string {
	if f.StartLine > 0 && f.StartLine < f.EndLine {
		return fmt.Sprintf("%d-%d", f.StartLine, f.EndLine)
	}
	return fmt.Sprint(f.EndLine)
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "<br>")
}

func escapeInline(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ") }

func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	return truncate(s, 400)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
