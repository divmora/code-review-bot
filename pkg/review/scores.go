package review

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
