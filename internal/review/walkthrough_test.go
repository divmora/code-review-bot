package review

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// Issue #14: a string where the schema wants a list or object dropped the
// whole walkthrough, and scores were then guessed from the findings.
func TestDecodeSummaryLenient(t *testing.T) {
	out, dropped, err := decodeSummary([]byte(`{
		"summary": "Adds retries",
		"cohorts": "main.go and util.go",
		"highlights": "[{\"type\":\"Bug Fixes\",\"items\":[\"retry on 503\"]}]",
		"score_reasons": "quality went up",
		"effort": "2",
		"scores": {"quality": 80, "security": "95", "maintainability": 88}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dropped, ",") != "cohorts,score_reasons" {
		t.Errorf("dropped = %v", dropped)
	}
	if out.Summary != "Adds retries" || out.Effort != 2 || out.Cohorts != nil {
		t.Errorf("out = %+v", out)
	}
	if len(out.Highlights) != 1 || out.Highlights[0].Items[0] != "retry on 503" {
		t.Errorf("a list sent as a JSON string is decoded: %+v", out.Highlights)
	}
	if out.Scores.Quality != 80 || out.Scores.Security != 95 || out.Scores.Maintainability != 88 {
		t.Errorf("scores = %+v", out.Scores)
	}
	if _, _, err := decodeSummary([]byte(`"not an object"`)); err == nil {
		t.Error("a non-object fails")
	}
}

func TestWalkthroughWithAStringField(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[]}`, nil
	}, summary: `{"summary":"s","cohorts":"all files","effort":1,"scores":{"quality":90,"security":100,"maintainability":90}}`}
	res, err := (&Engine{LLM: fake, Limits: limits(), Logger: quiet}).Run(context.Background(), Input{Files: testFiles(t)[:1], Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("one bad field doesn't make the review partial: %v", res.Errors)
	}
	if res.Scores == nil || res.Scores.Quality != 90 || len(res.Cohorts) == 0 {
		t.Errorf("the rest of the walkthrough is kept: %+v %+v", res.Scores, res.Cohorts)
	}
}

func TestNoScoresKeepsThePreviousInTheState(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"t","body":"x","confidence":0.9}]}`, nil
	}, summary: `not json`}
	off := false
	lim := limits()
	lim.Verify = &off
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	prev := &Scores{Quality: 70, Security: 95, Maintainability: 85}
	res, err := (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{
		Files: files, Repo: defaults(), PR: &vcs.PR{HeadSHA: "new"},
		PrevScores: prev, PrevScoresSHA: "old", PrevOpen: map[string]int{"quality": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scores != nil || !strings.Contains(res.ScoresNote, "unchanged from the last review") {
		t.Fatalf("scores = %+v, note %q", res.Scores, res.ScoresNote)
	}
	body := SummaryMarkdown(res, SummaryOptions{BotName: "codereview"})
	if strings.Contains(body, "## Scores") || !strings.Contains(body, "_No scores this time:") {
		t.Errorf("summary:\n%s", body)
	}
	m := regexp.MustCompile(`<!-- codereview:state=([A-Za-z0-9+/=]+) -->`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no state")
	}
	raw, _ := base64.StdEncoding.DecodeString(m[1])
	var st State
	if json.Unmarshal(raw, &st) != nil || st.Scores == nil || *st.Scores != *prev || st.ScoresSHA != "old" || st.OpenFindings["quality"] != 1 {
		t.Errorf("the previous scores are carried for the next review: %s", raw)
	}
}
