package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Fixed versus dismissed (issue #15). A finding open at the previous
// review that this review doesn't report again was either fixed (the code
// at its location changed) or not: its thread was resolved with no change
// (dismissed), or it is in code this review didn't look at again (still
// open). Only fixed findings stop counting towards the scores.

// PrevFinding is a finding open at a review, kept in the summary's state
// for the next review.
type PrevFinding struct {
	Path        string   `json:"path"`
	Start       int      `json:"start,omitempty"`
	End         int      `json:"end"`
	Severity    Severity `json:"sev"`
	Category    string   `json:"cat"`
	Title       string   `json:"title"`
	Fingerprint string   `json:"fp,omitempty"`
	// CodeHash and CodeLines identify the code at the finding when it was
	// reported (CodeLines non-blank lines, see codeHash); empty when it
	// wasn't in the diff.
	CodeHash  string `json:"code,omitempty"`
	CodeLines int    `json:"lines,omitempty"`

	// Changed is set by the caller before a review: the code at the
	// finding changed (or the file is gone) since the previous review.
	Changed bool `json:"-"`
}

// FindingRef names a finding in the JSON result.
type FindingRef struct {
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
}

// PreviousFindings is what became of the previous review's open findings.
type PreviousFindings struct {
	// Fixed: not reported again, and the code at them changed.
	Fixed int `json:"fixed"`
	// Dismissed: not reported again, code unchanged, thread resolved.
	Dismissed int `json:"dismissed"`
	// StillOpen: reported again, or code unchanged and thread open.
	StillOpen     int          `json:"still_open"`
	FixedList     []FindingRef `json:"fixed_findings,omitempty"`
	DismissedList []FindingRef `json:"dismissed_findings,omitempty"`
	fixedBy       map[string]int
	dismissedBy   map[string]int
	carriedBy     map[string]int
}

// maxPrevFindings bounds the findings kept in the summary's state.
const maxPrevFindings = 80

// normCode is the code's non-blank lines, trimmed: what a re-indent or a
// blank line doesn't change.
func normCode(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func codeHash(lines []string) string {
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:8])
}

// HashCode is what a PrevFinding keeps for these lines of code.
func HashCode(lines ...string) string { return codeHash(normCode(lines)) }

// CodeStillThere reports whether content still holds the code a
// PrevFinding was reported on, anywhere in the file (moved code counts).
func CodeStillThere(content []byte, p PrevFinding) bool {
	if p.CodeHash == "" || p.CodeLines <= 0 {
		return false
	}
	lines := normCode(strings.Split(string(content), "\n"))
	for i := 0; i+p.CodeLines <= len(lines); i++ {
		if codeHash(lines[i:i+p.CodeLines]) == p.CodeHash {
			return true
		}
	}
	return false
}

// findingCode is the new-side code of a finding's lines from the diff.
func findingCode(files []diff.File, f Finding) (hash string, n int) {
	start := f.StartLine
	if start <= 0 {
		start = f.EndLine
	}
	for i := range files {
		if files[i].Path != f.Path {
			continue
		}
		var got []string
		want := f.EndLine - start + 1
		for _, h := range files[i].Hunks {
			for _, l := range h.Lines {
				if l.NewNo >= start && l.NewNo <= f.EndLine && l.Kind != diff.Del {
					got = append(got, l.Text)
				}
			}
		}
		if len(got) != want { // not all of it is in the diff
			return "", 0
		}
		code := normCode(got)
		if len(code) == 0 {
			return "", 0
		}
		return codeHash(code), len(code)
	}
	return "", 0
}

func affectsScore(f Finding) bool { return f.Kind != KindQuestion && f.Severity.AtLeast(Minor) }

func ref(p PrevFinding) FindingRef {
	return FindingRef{Path: p.Path, Line: p.End, Severity: p.Severity, Category: p.Category, Title: p.Title}
}

// reportedAgain: a finding of this review is about the same code and topic.
func reportedAgain(p PrevFinding, fs []Finding) bool {
	for _, f := range fs {
		if p.Fingerprint != "" && f.Fingerprint == p.Fingerprint {
			return true
		}
	}
	for _, f := range fs {
		if postedBefore(f, []vcs.PostedComment{{Path: p.Path, Line: p.End, Title: p.Title}}) {
			return true
		}
	}
	return false
}

// resolvedThread: the bot's comment for p is on a resolved thread.
func resolvedThread(p PrevFinding, existing []vcs.PostedComment) bool {
	for _, e := range existing {
		if !e.Resolved || e.Path != p.Path {
			continue
		}
		if p.Fingerprint != "" && e.Fingerprint == p.Fingerprint {
			return true
		}
		if e.Line >= p.End-2 && e.Line <= p.End+2 && sameTopic(Finding{Path: p.Path, StartLine: p.End, EndLine: p.End, Title: p.Title},
			Finding{Path: e.Path, StartLine: e.Line, EndLine: e.Line, Title: e.Title}) {
			return true
		}
	}
	return false
}

// reconcile sorts the previous review's open findings into fixed,
// dismissed and still open. Findings not reported again whose code didn't
// change are carried: they keep counting towards the scores.
func (e *Engine) reconcile(res *Result, in Input) {
	if in.PrevFindings == nil {
		return
	}
	pf := &PreviousFindings{fixedBy: map[string]int{}, dismissedBy: map[string]int{}, carriedBy: map[string]int{}}
	now := res.allFindings()
	for _, p := range in.PrevFindings {
		k := dimensionOf(p.Category)
		switch {
		case reportedAgain(p, now):
			pf.StillOpen++
		case p.Changed:
			pf.Fixed++
			pf.fixedBy[k]++
			pf.FixedList = append(pf.FixedList, ref(p))
		default:
			res.Carried = append(res.Carried, Finding{Path: p.Path, StartLine: p.Start, EndLine: p.End, Severity: p.Severity,
				Category: p.Category, Title: p.Title, Fingerprint: p.Fingerprint, carried: &p})
			pf.carriedBy[k]++
			if resolvedThread(p, in.Existing) {
				pf.Dismissed++
				pf.dismissedBy[k]++
				pf.DismissedList = append(pf.DismissedList, ref(p))
			} else {
				pf.StillOpen++
			}
		}
	}
	res.Previous = pf
}

// scoreFindings are the findings the scores follow: everything still wrong
// with the change, including earlier findings carried over.
func (r *Result) scoreFindings() []Finding {
	return append(r.allFindings(), r.Carried...)
}

// openState is what the summary's state keeps for the next review: the
// findings that count towards the scores, with the code they point at.
func (r *Result) openState(files []diff.File) []PrevFinding {
	var out []PrevFinding
	for _, f := range r.scoreFindings() {
		if !affectsScore(f) || len(out) >= maxPrevFindings {
			continue
		}
		if f.carried != nil {
			c := *f.carried
			c.Changed = false
			out = append(out, c)
			continue
		}
		hash, n := findingCode(files, f)
		out = append(out, PrevFinding{Path: f.Path, Start: f.StartLine, End: f.EndLine, Severity: f.Severity, Category: f.Category,
			Title: f.Title, Fingerprint: f.Fingerprint, CodeHash: hash, CodeLines: n})
	}
	return out
}
