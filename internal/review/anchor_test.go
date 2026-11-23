package review

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/pkg/vcs"
)

func TestAnchorScores(t *testing.T) {
	prev := Scores{Quality: 82, Security: 95, Maintainability: 80}
	bug := func(sev Severity) Finding { return Finding{Severity: sev, Category: "bug"} }
	for _, c := range []struct {
		name     string
		proposed Scores
		fs       []Finding
		prevOpen map[string]int
		reasons  map[string]string
		want     Scores
	}{
		{"small wobble keeps the previous value", Scores{Quality: 79, Security: 98, Maintainability: 84}, []Finding{bug(Minor)}, map[string]int{"quality": 1}, nil,
			Scores{Quality: 82, Security: 95, Maintainability: 85}}, // maintainability: the floor (no open finding) still wins
		{"no drop without an open finding", Scores{Quality: 68, Security: 95, Maintainability: 80}, nil, nil, nil,
			Scores{Quality: 85, Security: 95, Maintainability: 85}},
		{"a drop with an open finding", Scores{Quality: 66, Security: 95, Maintainability: 80}, []Finding{bug(Major)}, map[string]int{}, nil,
			Scores{Quality: 66, Security: 95, Maintainability: 85}},
		{"no rise while as many findings are open", Scores{Quality: 82, Security: 95, Maintainability: 80}, []Finding{bug(Major), bug(Minor)}, map[string]int{"quality": 2}, nil,
			Scores{Quality: 70, Security: 95, Maintainability: 85}}, // capped at 70 by the major finding
		{"a rise when findings were resolved", Scores{Quality: 88, Security: 95, Maintainability: 80}, []Finding{bug(Minor)}, map[string]int{"quality": 3}, nil,
			Scores{Quality: 88, Security: 95, Maintainability: 85}},
		{"a rise the model explains", Scores{Quality: 88, Security: 95, Maintainability: 80}, []Finding{bug(Minor)}, map[string]int{"quality": 1}, map[string]string{"quality": "the retry bug is fixed"},
			Scores{Quality: 88, Security: 95, Maintainability: 85}},
		{"caps still win", Scores{Quality: 82, Security: 95, Maintainability: 80}, []Finding{{Severity: Critical, Category: "security"}}, map[string]int{"security": 1}, nil,
			Scores{Quality: 85, Security: 40, Maintainability: 85}},
	} {
		got, _ := anchorScores(prev, c.proposed, c.fs, c.prevOpen, c.reasons, nil)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	_, changes := anchorScores(prev, Scores{Quality: 66, Security: 95, Maintainability: 80}, []Finding{bug(Major)}, nil, map[string]string{"quality": "new major finding in auth.go"}, nil)
	if len(changes) < 1 || changes[0] != (ScoreChange{Name: "Quality", From: 82, To: 66, Reason: "new major finding in auth.go"}) {
		t.Errorf("changes: %+v", changes)
	}
	_, changes = anchorScores(prev, Scores{Quality: 66, Security: 95, Maintainability: 80}, []Finding{bug(Major)}, nil, nil, nil)
	if changes[0].Reason != "1 open finding(s)" {
		t.Errorf("a reason is given when the model gave none: %+v", changes)
	}
}

func TestEngineAnchor(t *testing.T) {
	e := &Engine{}
	prev := &Scores{Quality: 82, Security: 95, Maintainability: 85}
	pr := &vcs.PR{HeadSHA: "abc"}

	// The same commit reviewed again keeps the scores, whatever the model says.
	res := &Result{Scores: &Scores{Quality: 60, Security: 70, Maintainability: 50}, Findings: []Finding{{Severity: Major, Category: "bug"}}}
	e.anchor(res, Input{PR: pr, PrevScores: prev, PrevScoresSHA: "abc"}, nil)
	if *res.Scores != *prev || len(res.ScoreChanges) != 0 {
		t.Errorf("same commit: %+v %+v", res.Scores, res.ScoreChanges)
	}
	// A new commit with no new findings in a dimension doesn't lower it.
	res = &Result{Scores: &Scores{Quality: 72, Security: 88, Maintainability: 86}}
	e.anchor(res, Input{PR: pr, PrevScores: prev, PrevScoresSHA: "old"}, nil)
	if res.Scores.Quality != 85 || res.Scores.Security != 95 {
		t.Errorf("incremental, nothing new: %+v", res.Scores)
	}
	// No previous scores (or --full, which passes none): from scratch.
	res = &Result{Scores: &Scores{Quality: 60, Security: 70, Maintainability: 50}}
	e.anchor(res, Input{PR: pr}, nil)
	if res.Scores.Quality != 60 || res.ScoreChanges != nil {
		t.Errorf("from scratch: %+v", res.Scores)
	}
}

func TestScoresKeptInTheSummary(t *testing.T) {
	res := &Result{Summary: "x", Reviewed: []string{"a.go"}, HeadSHA: "abc123",
		Scores:       &Scores{Quality: 76, Security: 95, Maintainability: 88},
		ScoreChanges: []ScoreChange{{Name: "Quality", From: 82, To: 76, Reason: "new major finding in auth.go"}},
		Findings:     []Finding{{Severity: Major, Category: "bug", Path: "auth.go", Title: "t"}}}
	body := SummaryMarkdown(res, SummaryOptions{BotName: "codereview"})
	if !strings.Contains(body, "Since the last review: Quality 82 → 76 (new major finding in auth.go)") {
		t.Errorf("the change is shown:\n%s", body)
	}
	// The state carries the scores for the next review.
	m := regexp.MustCompile(`<!-- codereview:state=([A-Za-z0-9+/=]+) -->`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no state in the summary:\n%s", body)
	}
	st, _ := base64.StdEncoding.DecodeString(m[1])
	var s State
	if err := json.Unmarshal(st, &s); err != nil || s.Scores == nil || *s.Scores != *res.Scores || s.ScoresSHA != "abc123" || s.OpenFindings["quality"] != 1 {
		t.Fatalf("state: %v %+v (%s)", err, s, st)
	}
	// Older summaries: the scores table is read instead.
	if sc, ok := SummaryScores(body); !ok || *sc != *res.Scores {
		t.Errorf("scores from the summary table: %v %+v", ok, sc)
	}
	if _, ok := SummaryScores("no scores here"); ok {
		t.Error("no table, no scores")
	}
}
