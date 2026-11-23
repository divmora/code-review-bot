package review

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

func TestCodeStillThere(t *testing.T) {
	files := []diff.File{{Path: "a.go", Hunks: []diff.Hunk{{Lines: []diff.Line{
		{Kind: diff.Context, NewNo: 10, Text: "func f() {"},
		{Kind: diff.Add, NewNo: 11, Text: "\tq := \"SELECT * FROM t WHERE id=\" + id"},
		{Kind: diff.Del, Text: "\told()"},
		{Kind: diff.Add, NewNo: 12, Text: "\tdb.Query(q)"},
	}}}}}
	hash, n := findingCode(files, Finding{Path: "a.go", StartLine: 11, EndLine: 12})
	if hash == "" || n != 2 {
		t.Fatalf("code of the finding: %q %d", hash, n)
	}
	p := PrevFinding{Path: "a.go", CodeHash: hash, CodeLines: n}
	moved := "package a\n\n// moved down and re-indented\nfunc g() {}\nfunc f() {\n        q := \"SELECT * FROM t WHERE id=\" + id\n\n        db.Query(q)\n}\n"
	if !CodeStillThere([]byte(moved), p) {
		t.Error("moved or re-indented code is still there")
	}
	fixed := "func f() {\n\tdb.Query(\"SELECT * FROM t WHERE id=$1\", id)\n}\n"
	if CodeStillThere([]byte(fixed), p) {
		t.Error("changed code is not")
	}
	if h, _ := findingCode(files, Finding{Path: "a.go", StartLine: 30, EndLine: 31}); h != "" {
		t.Error("lines outside the diff have no code")
	}
}

// Issue #15: three Major findings, all threads resolved; the next
// incremental review reported nothing. Only the one whose code changed is
// fixed, and only its dimension rises.
func TestReconcileFixedAndDismissed(t *testing.T) {
	prev := []PrevFinding{
		{Path: "auth.go", End: 40, Severity: Major, Category: "security", Title: "Token compared with ==", Fingerprint: "aaa", CodeHash: "x", CodeLines: 1},
		{Path: "retry.go", End: 12, Severity: Major, Category: "reliability", Title: "Retry never stops", Fingerprint: "bbb", Changed: true},
		{Path: "jobs.go", End: 7, Severity: Major, Category: "bug", Title: "Nil map write", Fingerprint: "ccc"},
		{Path: "jobs.go", End: 90, Severity: Minor, Category: "bug", Title: "Off by one in pager", Fingerprint: "ddd"},
	}
	existing := []vcs.PostedComment{
		{Path: "auth.go", Line: 40, Title: "Token compared with ==", Fingerprint: "aaa", Resolved: true},
		{Path: "retry.go", Line: 12, Title: "Retry never stops", Fingerprint: "bbb", Resolved: true},
		{Path: "jobs.go", Line: 7, Title: "Nil map write", Fingerprint: "ccc", Resolved: true},
	}
	res := &Result{Repeated: []Finding{{Path: "jobs.go", StartLine: 91, EndLine: 91, Severity: Minor, Category: "bug", Title: "Off by one in pager", Fingerprint: "ddd"}}}
	in := Input{PR: &vcs.PR{HeadSHA: "new"}, PrevFindings: prev, Existing: existing,
		PrevScores: &Scores{Quality: 58, Security: 55, Maintainability: 85}, PrevScoresSHA: "old"}
	(&Engine{}).reconcile(res, in)
	pf := res.Previous
	if pf == nil || pf.Fixed != 1 || pf.Dismissed != 2 || pf.StillOpen != 1 || len(res.Carried) != 2 {
		t.Fatalf("previous = %+v, carried %d", pf, len(res.Carried))
	}
	if pf.FixedList[0].Title != "Retry never stops" || len(pf.DismissedList) != 2 {
		t.Errorf("lists: %+v", pf)
	}

	// The model proposes top scores and even explains them. Security's
	// only finding was dismissed: it stays. Quality had one of its three
	// fixed: it rises, but the dismissed Major still caps it at 70.
	res.Scores = &Scores{Quality: 95, Security: 100, Maintainability: 95}
	*res.Scores = finalScores(*res.Scores, res.scoreFindings())
	(&Engine{}).anchor(res, in, map[string]string{"security": "no open findings", "quality": "all threads resolved"})
	if res.Scores.Security != 55 || res.Scores.Quality != 70 {
		t.Errorf("dismissed findings keep the scores: %+v %+v", res.Scores, res.ScoreChanges)
	}
	if len(res.ScoreChanges) == 0 || res.ScoreChanges[0] != (ScoreChange{Name: "Quality", From: 58, To: 70, Reason: "1 finding(s) fixed; 1 dismissed without a change, still counted"}) {
		t.Errorf("the reason says what was fixed and dismissed: %+v", res.ScoreChanges)
	}

	// With the security finding fixed too, Security rises (capped by nothing
	// left in it) and says why.
	prev[0].Changed = true
	res = &Result{Repeated: res.Repeated, Scores: &Scores{Quality: 95, Security: 100, Maintainability: 95}}
	in.PrevFindings = prev
	(&Engine{}).reconcile(res, in)
	*res.Scores = finalScores(*res.Scores, res.scoreFindings())
	(&Engine{}).anchor(res, in, nil)
	var sec *ScoreChange
	for i := range res.ScoreChanges {
		if res.ScoreChanges[i].Name == "Security" {
			sec = &res.ScoreChanges[i]
		}
	}
	if res.Scores.Security != 100 || sec == nil || sec.Reason != "1 finding(s) fixed" {
		t.Errorf("a fixed finding raises its score: %+v %+v", res.Scores, res.ScoreChanges)
	}
}

func TestPreviousFindingsInTheSummary(t *testing.T) {
	carriedFrom := PrevFinding{Path: "auth.go", End: 40, Severity: Major, Category: "security", Title: "Token compared with ==", Fingerprint: "aaa", CodeHash: "keep", CodeLines: 1}
	res := &Result{Summary: "x", Reviewed: []string{"a.go"}, HeadSHA: "abc",
		Scores:   &Scores{Quality: 90, Security: 55, Maintainability: 90},
		Findings: []Finding{{Path: "a.go", StartLine: 2, EndLine: 2, Severity: Minor, Category: "bug", Title: "new one", Fingerprint: "eee"}},
		Carried:  []Finding{{Path: "auth.go", EndLine: 40, Severity: Major, Category: "security", Title: "Token compared with ==", Fingerprint: "aaa", carried: &carriedFrom}},
		Previous: &PreviousFindings{Fixed: 1, Dismissed: 1, DismissedList: []FindingRef{{Path: "auth.go", Line: 40, Severity: Major, Category: "security", Title: "Token compared with =="}}}}
	files := []diff.File{{Path: "a.go", Hunks: []diff.Hunk{{Lines: []diff.Line{{Kind: diff.Add, NewNo: 2, Text: "x := 1"}}}}}}
	res.open = res.openState(files)
	body := SummaryMarkdown(res, SummaryOptions{BotName: "codereview"})
	if !strings.Contains(body, "Earlier findings: 1 fixed, 1 dismissed, 0 still open.") || !strings.Contains(body, "- Dismissed: Token compared with ==") {
		t.Errorf("summary:\n%s", body)
	}
	m := regexp.MustCompile(`<!-- codereview:state=([A-Za-z0-9+/=]+) -->`).FindStringSubmatch(body)
	raw, _ := base64.StdEncoding.DecodeString(m[1])
	var st State
	if err := json.Unmarshal(raw, &st); err != nil || len(st.Findings) != 2 {
		t.Fatalf("state: %v %s", err, raw)
	}
	if st.Findings[0].CodeHash == "" || st.Findings[1].CodeHash != "keep" || st.OpenFindings["security"] != 1 {
		t.Errorf("new findings get their code, carried ones keep theirs, and count: %s", raw)
	}
	out, _ := json.Marshal(res)
	if !strings.Contains(string(out), `"previous_findings":{"fixed":1,"dismissed":1,"still_open":0`) {
		t.Errorf("JSON result: %s", out)
	}
}

func TestRunCarriesUnfixedFindings(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[]}`, nil
	}, summary: `{"summary":"s","cohorts":[],"effort":1,"scores":{"quality":95,"security":100,"maintainability":95},"score_reasons":{"security":"no open findings"}}`}
	off := false
	lim := limits()
	lim.Verify = &off
	// A Security finding from the last review, in a file this push didn't
	// touch, its thread resolved.
	prev := []PrevFinding{{Path: "auth.go", End: 40, Severity: Major, Category: "security", Title: "Token compared with ==", Fingerprint: "aaa", CodeHash: "h", CodeLines: 1}}
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3" // no secret for the scanner
	res, err := (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{
		Files: files, Repo: defaults(), PR: &vcs.PR{HeadSHA: "new"},
		PrevScores: &Scores{Quality: 90, Security: 55, Maintainability: 90}, PrevScoresSHA: "old", PrevFindings: prev,
		Existing: []vcs.PostedComment{{Path: "auth.go", Line: 40, Title: "Token compared with ==", Fingerprint: "aaa", Resolved: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scores == nil || res.Scores.Security != 55 || res.Previous == nil || res.Previous.Dismissed != 1 {
		t.Errorf("a resolved but unfixed finding keeps Security: %+v %+v", res.Scores, res.Previous)
	}
	if len(res.open) != 1 || res.open[0].CodeHash != "h" {
		t.Errorf("it stays in the state for the next review: %+v", res.open)
	}
	if len(res.Findings) != 0 {
		t.Errorf("carried findings aren't posted again: %+v", res.Findings)
	}
}
