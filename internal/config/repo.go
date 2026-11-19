package config

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Profile sets how picky the reviewer is.
type Profile string

const (
	Chill     Profile = "chill"     // bugs, security, real risks only
	Balanced  Profile = "balanced"  // plus maintainability and clear improvements
	Assertive Profile = "assertive" // plus style and nitpicks
)

// RepoConfig is what a repository commits as .codereview.yaml. Fields left
// unset inherit the operator defaults.
type RepoConfig struct {
	Profile Profile `yaml:"profile"`
	// Learnings are team decisions from earlier reviews ("we pad account
	// numbers with zeros on purpose"). The reviewer follows them and the
	// verifier drops findings they rule out.
	Learnings []Learning `yaml:"learnings"`
	// Instructions apply to the whole review (team conventions, focus).
	Instructions string `yaml:"instructions"`
	// PathInstructions add guidance for files matching a glob.
	PathInstructions []PathInstruction `yaml:"path_instructions"`
	// PathFilters include/exclude files. "!pattern" excludes. When any
	// include pattern is present, only matching files are reviewed.
	PathFilters []string `yaml:"path_filters"`
	MaxComments int      `yaml:"max_comments"`
	// MinSeverity drops comments below this level: critical, major, minor, nitpick.
	MinSeverity string `yaml:"min_severity"`
	// Language of the review text, e.g. "English", "German".
	Language string `yaml:"language"`
	Poem     *bool  `yaml:"poem"`

	AutoReview AutoReview `yaml:"auto_review"`
	// Guidelines controls reading AGENTS.md-style files from the target branch.
	Guidelines Guidelines `yaml:"guidelines"`

	// PR description and title, following CodeRabbit's conventions (same keys).
	// HighLevelSummary appends a "Summary by codereview" block to the PR
	// description (author text is never changed). Default true.
	HighLevelSummary *bool `yaml:"high_level_summary"`
	// HighLevelSummaryPlaceholder in the description is replaced by the summary
	// block, even when HighLevelSummary is false. Default "@codereview summary".
	HighLevelSummaryPlaceholder  string `yaml:"high_level_summary_placeholder"`
	HighLevelSummaryInstructions string `yaml:"high_level_summary_instructions"`
	// AutoTitlePlaceholder in the PR title makes the bot replace the title with
	// a generated one. Default "@codereview". The title is never changed otherwise.
	AutoTitlePlaceholder  string `yaml:"auto_title_placeholder"`
	AutoTitleInstructions string `yaml:"auto_title_instructions"`
	// Model picks one of the operator's named model profiles (only when
	// the operator sets allow_repo_model).
	Model string `yaml:"model"`
}

// Guidelines configures repository guideline files.
type Guidelines struct {
	// Enabled defaults to true.
	Enabled *bool `yaml:"enabled"`
	// Files at the repository root; nested AGENTS.md files in the changed
	// files' folders are always included when enabled. Default:
	// AGENTS.md, CLAUDE.md, .github/copilot-instructions.md, .cursorrules.
	Files []string `yaml:"files"`
}

// PathInstruction is guidance for a path glob.
type PathInstruction struct {
	Path         string `yaml:"path"`
	Instructions string `yaml:"instructions"`
}

// AutoReview controls reviews triggered by pushes (not by commands).
type AutoReview struct {
	Enabled *bool `yaml:"enabled"`
	Drafts  *bool `yaml:"drafts"`
	// BaseBranches limits auto review to PRs targeting matching branches
	// (globs). Empty means all.
	BaseBranches []string `yaml:"base_branches"`
	// IgnoreTitleKeywords skips PRs whose title contains any keyword.
	IgnoreTitleKeywords []string `yaml:"ignore_title_keywords"`
	// Incremental reviews only files changed since the last reviewed commit.
	Incremental *bool `yaml:"incremental"`
}

func boolPtr(b bool) *bool { return &b }

func (r *RepoConfig) fillDefaults() {
	if r.Profile == "" {
		r.Profile = Balanced
	}
	if r.MaxComments <= 0 {
		r.MaxComments = 25
	}
	if r.Language == "" {
		r.Language = "English"
	}
	if r.Poem == nil {
		r.Poem = boolPtr(false)
	}
	if r.Guidelines.Enabled == nil {
		r.Guidelines.Enabled = boolPtr(true)
	}
	if r.HighLevelSummary == nil {
		r.HighLevelSummary = boolPtr(true)
	}
	if r.HighLevelSummaryPlaceholder == "" {
		r.HighLevelSummaryPlaceholder = "@codereview summary"
	}
	if r.AutoTitlePlaceholder == "" {
		r.AutoTitlePlaceholder = "@codereview"
	}
	a := &r.AutoReview
	if a.Enabled == nil {
		a.Enabled = boolPtr(true)
	}
	if a.Drafts == nil {
		a.Drafts = boolPtr(false)
	}
	if a.Incremental == nil {
		a.Incremental = boolPtr(true)
	}
	if a.IgnoreTitleKeywords == nil {
		a.IgnoreTitleKeywords = []string{"[skip review]", "[no review]", "WIP"}
	}
}

// ParseRepoConfig parses .codereview.yaml and merges it over defaults.
func ParseRepoConfig(data []byte, defaults RepoConfig) (RepoConfig, error) {
	var rc RepoConfig
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &rc); err != nil {
			return defaults, fmt.Errorf("parse repo config: %w", err)
		}
	}
	out := defaults
	if rc.Profile != "" {
		out.Profile = rc.Profile
	}
	if rc.Instructions != "" {
		out.Instructions = strings.TrimSpace(defaults.Instructions + "\n\n" + rc.Instructions)
	}
	out.PathInstructions = append(append([]PathInstruction{}, defaults.PathInstructions...), rc.PathInstructions...)
	out.PathFilters = append(append([]string{}, defaults.PathFilters...), rc.PathFilters...)
	if rc.MaxComments > 0 {
		out.MaxComments = rc.MaxComments
	}
	if rc.MinSeverity != "" {
		out.MinSeverity = rc.MinSeverity
	}
	if rc.Language != "" {
		out.Language = rc.Language
	}
	if rc.Poem != nil {
		out.Poem = rc.Poem
	}
	if rc.Model != "" {
		out.Model = rc.Model
	}
	if rc.HighLevelSummary != nil {
		out.HighLevelSummary = rc.HighLevelSummary
	}
	for _, kv := range [][2]*string{
		{&out.HighLevelSummaryPlaceholder, &rc.HighLevelSummaryPlaceholder},
		{&out.HighLevelSummaryInstructions, &rc.HighLevelSummaryInstructions},
		{&out.AutoTitlePlaceholder, &rc.AutoTitlePlaceholder},
		{&out.AutoTitleInstructions, &rc.AutoTitleInstructions},
	} {
		if *kv[1] != "" {
			*kv[0] = *kv[1]
		}
	}
	if rc.Guidelines.Enabled != nil {
		out.Guidelines.Enabled = rc.Guidelines.Enabled
	}
	if rc.Guidelines.Files != nil {
		out.Guidelines.Files = rc.Guidelines.Files
	}
	a, d := rc.AutoReview, defaults.AutoReview
	out.AutoReview = d
	if a.Enabled != nil {
		out.AutoReview.Enabled = a.Enabled
	}
	if a.Drafts != nil {
		out.AutoReview.Drafts = a.Drafts
	}
	if a.Incremental != nil {
		out.AutoReview.Incremental = a.Incremental
	}
	if a.BaseBranches != nil {
		out.AutoReview.BaseBranches = a.BaseBranches
	}
	if a.IgnoreTitleKeywords != nil {
		out.AutoReview.IgnoreTitleKeywords = a.IgnoreTitleKeywords
	}
	switch out.Profile {
	case Chill, Balanced, Assertive:
	default:
		return out, fmt.Errorf("repo config: profile %q: want chill, balanced or assertive", out.Profile)
	}
	out.fillDefaults()
	return out, nil
}

// IncludePath applies PathFilters to a file path.
func (r *RepoConfig) IncludePath(p string) bool {
	hasInclude, included := false, false
	for _, f := range r.PathFilters {
		if strings.HasPrefix(f, "!") {
			if Match(f[1:], p) {
				return false
			}
			continue
		}
		hasInclude = true
		if Match(f, p) {
			included = true
		}
	}
	return !hasInclude || included
}

// InstructionsFor returns path instructions matching p.
func (r *RepoConfig) InstructionsFor(p string) []string {
	var out []string
	for _, pi := range r.PathInstructions {
		if Match(pi.Path, p) {
			out = append(out, strings.TrimSpace(pi.Instructions))
		}
	}
	return out
}

// AutoReviewSkipReason returns why a push-triggered review should not
// run, or "" to review.
func (r *RepoConfig) AutoReviewSkipReason(title, baseBranch string, draft bool) string {
	a := r.AutoReview
	if a.Enabled != nil && !*a.Enabled {
		return "auto review is disabled in the repository config"
	}
	if draft && (a.Drafts == nil || !*a.Drafts) {
		return "pull request is a draft"
	}
	lt := strings.ToLower(title)
	for _, k := range a.IgnoreTitleKeywords {
		if k != "" && strings.Contains(lt, strings.ToLower(k)) {
			return fmt.Sprintf("title contains %q", k)
		}
	}
	if len(a.BaseBranches) > 0 {
		for _, b := range a.BaseBranches {
			if Match(b, baseBranch) {
				return ""
			}
		}
		return fmt.Sprintf("base branch %q is not in auto_review.base_branches", baseBranch)
	}
	return ""
}

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// Match reports whether a path matches a glob. Supports *, ?, ** and
// gitignore-style basename matching for patterns without a slash.
func Match(pattern, p string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return false
	}
	if !strings.Contains(strings.TrimSuffix(pattern, "/"), "/") {
		// "*.lock" matches at any depth; "vendor/" matches the directory.
		if strings.HasSuffix(pattern, "/") {
			pattern = "**/" + pattern + "**"
		} else {
			if ok, _ := path.Match(pattern, path.Base(p)); ok {
				return true
			}
			pattern = "**/" + pattern + "/**"
		}
	} else if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return globRegexp(pattern).MatchString(p)
}

func globRegexp(pattern string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache[pattern] = re
	return re
}

// Overrides are admin-panel settings for one repository. They are applied
// after .codereview.yaml, so the admin's choice wins. Nil fields leave the
// value unchanged.
type Overrides struct {
	Profile     *Profile `json:"profile,omitempty"`
	MaxComments *int     `json:"max_comments,omitempty"`
	MinSeverity *string  `json:"min_severity,omitempty"`
	// Model is a model profile name from the operator config.
	Model        *string `json:"model,omitempty"`
	AutoReview   *bool   `json:"auto_review,omitempty"`
	Drafts       *bool   `json:"drafts,omitempty"`
	Instructions string  `json:"instructions,omitempty"` // appended to repo instructions
	// Learnings are added to the repository's own (e.g. from the console).
	Learnings []Learning `json:"learnings,omitempty"`
}

// Learning is one team decision. Path, when set, is a glob limiting it to
// matching files.
type Learning struct {
	Text string `json:"text" yaml:"text"`
	Path string `json:"path,omitempty" yaml:"path"`
}

// Learning limits keep the prompt section bounded.
const (
	MaxLearnings    = 100
	MaxLearningText = 600
)

// Validate checks override values.
func (o *Overrides) Validate(profiles map[string]bool) error {
	if o == nil {
		return nil
	}
	if o.Profile != nil {
		switch *o.Profile {
		case Chill, Balanced, Assertive:
		default:
			return fmt.Errorf("profile %q: want chill, balanced or assertive", *o.Profile)
		}
	}
	if o.MaxComments != nil && (*o.MaxComments < 0 || *o.MaxComments > 200) {
		return fmt.Errorf("max_comments must be between 0 and 200")
	}
	if o.MinSeverity != nil {
		switch *o.MinSeverity {
		case "", "critical", "major", "minor", "nitpick":
		default:
			return fmt.Errorf("min_severity %q: want critical, major, minor or nitpick", *o.MinSeverity)
		}
	}
	if o.Model != nil && *o.Model != "" && !profiles[*o.Model] {
		return fmt.Errorf("model %q is not a model profile in the server config", *o.Model)
	}
	if len(o.Instructions) > 8000 {
		return fmt.Errorf("instructions are limited to 8000 characters")
	}
	if len(o.Learnings) > MaxLearnings {
		return fmt.Errorf("at most %d learnings", MaxLearnings)
	}
	for _, l := range o.Learnings {
		if t := strings.TrimSpace(l.Text); t == "" || len(t) > MaxLearningText {
			return fmt.Errorf("a learning needs text of at most %d characters", MaxLearningText)
		}
	}
	return nil
}

// Apply writes the overrides onto a repo config.
func (o *Overrides) Apply(rc *RepoConfig) {
	if o == nil {
		return
	}
	if o.Profile != nil {
		rc.Profile = *o.Profile
	}
	if o.MaxComments != nil && *o.MaxComments > 0 {
		rc.MaxComments = *o.MaxComments
	}
	if o.MinSeverity != nil {
		rc.MinSeverity = *o.MinSeverity
	}
	if o.Model != nil && *o.Model != "" {
		rc.Model = *o.Model
	}
	if o.AutoReview != nil {
		rc.AutoReview.Enabled = o.AutoReview
	}
	if o.Drafts != nil {
		rc.AutoReview.Drafts = o.Drafts
	}
	if s := strings.TrimSpace(o.Instructions); s != "" {
		rc.Instructions = strings.TrimSpace(rc.Instructions + "\n\n" + s)
	}
	rc.Learnings = append(rc.Learnings, o.Learnings...)
}

// LearningsBlock renders the learnings for a prompt ("" when none).
func (r *RepoConfig) LearningsBlock() string {
	var b strings.Builder
	n := 0
	for _, l := range r.Learnings {
		t := strings.TrimSpace(l.Text)
		if t == "" || n >= MaxLearnings {
			continue
		}
		if len(t) > MaxLearningText {
			t = t[:MaxLearningText]
		}
		b.WriteString("- ")
		if p := strings.TrimSpace(l.Path); p != "" {
			b.WriteString("[" + p + "] ")
		}
		b.WriteString(strings.ReplaceAll(t, "\n", " ") + "\n")
		n++
	}
	return b.String()
}
