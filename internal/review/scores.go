package review

import (
	"fmt"
	"strings"
)

// Scores rate the change from 1 (poor) to 100 (excellent).
type Scores struct {
	Quality         int `json:"quality"`
	Security        int `json:"security"`
	Maintainability int `json:"maintainability"`
}

// ScoreNames are used for labels and display, in order.
var ScoreNames = []string{"Quality", "Security", "Maintainability"}

// Get returns a score by display name.
func (s Scores) Get(name string) int {
	switch name {
	case "Quality":
		return s.Quality
	case "Security":
		return s.Security
	case "Maintainability":
		return s.Maintainability
	}
	return 0
}

const scoreRubric = `- scores: rate the change from 1 to 100 on three axes, judging only what this PR changes:
  quality (correctness, edge cases, error handling, tests), security (vulnerabilities, secrets, access control,
  data exposure; 100 if the change has no security impact), maintainability (readability, structure, duplication,
  naming, documentation). 90-100: excellent, nothing to fix. 75-89: good, minor issues. 50-74: real problems that
  should be fixed before merging. Below 50: serious defects. Be consistent with the findings you are given.`

// scoreCaps limits scores by the findings: a PR with a critical security
// finding cannot score high on security, whatever the model says.
func scoreCaps(fs []Finding) Scores {
	c := Scores{Quality: 100, Security: 100, Maintainability: 100}
	capAt := func(v *int, max int) {
		if max < *v {
			*v = max
		}
	}
	for _, f := range fs {
		if f.Kind == KindQuestion {
			continue
		}
		var target *int
		switch f.Category {
		case "security":
			target = &c.Security
		case "maintainability", "style", "docs":
			target = &c.Maintainability
		default: // bug, performance, reliability, tests
			target = &c.Quality
		}
		switch f.Severity {
		case Critical:
			capAt(target, 40)
		case Major:
			capAt(target, 70)
		case Minor:
			capAt(target, 88)
		}
	}
	return c
}

// fallbackScores derives scores from findings when the model gives none.
func fallbackScores(fs []Finding) Scores {
	s := Scores{Quality: 95, Security: 100, Maintainability: 95}
	penalty := map[Severity]int{Critical: 45, Major: 20, Minor: 6, Nitpick: 1}
	for _, f := range fs {
		if f.Kind == KindQuestion {
			continue
		}
		p := penalty[f.Severity]
		switch f.Category {
		case "security":
			s.Security -= p
		case "maintainability", "style", "docs":
			s.Maintainability -= p
		default:
			s.Quality -= p
		}
	}
	return clampScores(s, scoreCaps(fs))
}

// scoreFloors raises scores the findings do not support: a dimension with no
// remaining issue of at least minor severity (questions and findings the
// verifier removed do not count) scores at least 85, so a disputed or
// speculative finding cannot drag a score down on its own.
func scoreFloors(fs []Finding) Scores {
	f := Scores{Quality: 85, Security: 85, Maintainability: 85}
	for _, x := range fs {
		if x.Kind == KindQuestion || !x.Severity.AtLeast(Minor) {
			continue
		}
		switch x.Category {
		case "security":
			f.Security = 1
		case "maintainability", "style", "docs":
			f.Maintainability = 1
		default:
			f.Quality = 1
		}
	}
	return f
}

// finalScores applies the floors, then the caps.
func finalScores(s Scores, fs []Finding) Scores {
	fl := scoreFloors(fs)
	s.Quality, s.Security, s.Maintainability = max(s.Quality, fl.Quality), max(s.Security, fl.Security), max(s.Maintainability, fl.Maintainability)
	return clampScores(s, scoreCaps(fs))
}

func clampScores(s, caps Scores) Scores {
	fix := func(v, max int) int {
		if v < 1 {
			v = 1
		}
		if v > 100 {
			v = 100
		}
		if v > max {
			v = max
		}
		return v
	}
	return Scores{
		Quality:         fix(s.Quality, caps.Quality),
		Security:        fix(s.Security, caps.Security),
		Maintainability: fix(s.Maintainability, caps.Maintainability),
	}
}

// ScoreBand returns green (>= 80), orange (>= 50) or red.
func ScoreBand(v int) string {
	switch {
	case v >= 80:
		return "green"
	case v >= 50:
		return "orange"
	}
	return "red"
}

// Score anchoring (issue #9): each run's scores start from the previous
// review's, so they move only when the change does.

// scoreAnchorThreshold is the smallest change a score makes from the
// previous review; smaller ones keep the previous value.
const scoreAnchorThreshold = 5

// scoreDimensions are the score keys, in display order, with their names.
var scoreDimensions = [][2]string{{"quality", "Quality"}, {"security", "Security"}, {"maintainability", "Maintainability"}}

// ScoreChange is a score that moved from the previous review, and why.
type ScoreChange struct {
	Name   string `json:"name"` // Quality, Security, Maintainability
	From   int    `json:"from"`
	To     int    `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// dimensionOf maps a finding's category to the score it affects.
func dimensionOf(category string) string {
	switch category {
	case "security":
		return "security"
	case "maintainability", "style", "docs":
		return "maintainability"
	}
	return "quality"
}

// OpenFindings counts findings that affect a score (minor or worse, not
// questions) by score key.
func OpenFindings(fs []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		if f.Kind == KindQuestion || !f.Severity.AtLeast(Minor) {
			continue
		}
		out[dimensionOf(f.Category)]++
	}
	return out
}

func (s Scores) getKey(k string) int {
	switch k {
	case "quality":
		return s.Quality
	case "security":
		return s.Security
	}
	return s.Maintainability
}

func (s *Scores) setKey(k string, v int) {
	switch k {
	case "quality":
		s.Quality = v
	case "security":
		s.Security = v
	default:
		s.Maintainability = v
	}
}

// anchorScores keeps each score at its previous value unless the change
// supports a new one:
//   - a change smaller than scoreAnchorThreshold keeps the previous value;
//   - a score drops only with an open finding in its dimension;
//   - a score rises only when that dimension has fewer open findings than
//     before (or none), or the model said why.
//
// The floors and caps from the findings are applied last and still win.
// prevOpen is nil when the previous counts aren't known (older summaries).
func anchorScores(prev, proposed Scores, fs []Finding, prevOpen map[string]int, reasons map[string]string) (Scores, []ScoreChange) {
	open := OpenFindings(fs)
	next := proposed
	for _, d := range scoreDimensions {
		k := d[0]
		p, v := prev.getKey(k), proposed.getKey(k)
		switch {
		case abs(v-p) < scoreAnchorThreshold:
			v = p
		case v < p && open[k] == 0:
			v = p
		case v > p && open[k] > 0 && strings.TrimSpace(reasons[k]) == "" && (prevOpen == nil || open[k] >= prevOpen[k]):
			v = p
		}
		next.setKey(k, v)
	}
	next = finalScores(next, fs)
	var changes []ScoreChange
	for _, d := range scoreDimensions {
		k, name := d[0], d[1]
		from, to := prev.getKey(k), next.getKey(k)
		if from == to {
			continue
		}
		reason := strings.TrimSpace(reasons[k])
		if len(reason) > 200 {
			reason = reason[:200]
		}
		if reason == "" {
			switch {
			case to < from:
				reason = fmt.Sprintf("%d open finding(s)", open[k])
			case open[k] == 0:
				reason = "no open findings"
			default:
				reason = "fewer open findings"
			}
		}
		changes = append(changes, ScoreChange{Name: name, From: from, To: to, Reason: reason})
	}
	return next, changes
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
