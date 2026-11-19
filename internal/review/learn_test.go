package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
)

type learnFake struct {
	out string
	req llm.Request
}

func (f *learnFake) Name() string { return "fake:learn" }
func (f *learnFake) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	f.req = req
	return &llm.Response{JSON: json.RawMessage(f.out)}, nil
}

func TestLearn(t *testing.T) {
	f := &learnFake{out: `{"learning":true,"text":"Account numbers are compared without leading zeros on purpose.","path":"","reason":"team convention"}`}
	out, _, err := Learn(context.Background(), f, LearnInput{Repo: "acme/pay", Path: "a.php", Finding: "ltrim drops zeros", Reply: "we always compare without zeros",
		Author: "dev", Existing: []config.Learning{{Text: "Legacy controllers are frozen."}}})
	if err != nil || !out.Learning || !strings.Contains(out.Text, "leading zeros") || out.Model != "fake:learn" {
		t.Fatalf("learn = %+v %v", out, err)
	}
	msg := f.req.Messages[0].Content
	if !strings.Contains(msg, "<reply author=\"dev\">") || !strings.Contains(msg, "Legacy controllers are frozen.") || f.req.Schema != learnSchema {
		t.Errorf("prompt should carry the reply and existing learnings: %s", msg)
	}
	f.out = `{"learning":true,"text":"","reason":"x"}`
	if out, _, _ := Learn(context.Background(), f, LearnInput{Reply: "thanks"}); out.Learning {
		t.Error("a learning without text is not a learning")
	}
	if out, _, _ := Learn(context.Background(), f, LearnInput{Reply: "  "}); out.Learning || out.Reason != "empty reply" {
		t.Errorf("empty reply: %+v", out)
	}
}

func TestLearningsInPrompts(t *testing.T) {
	fake := &scripted{
		review: func(req llm.Request) (string, error) {
			if !strings.Contains(req.System, "Team learnings") || !strings.Contains(req.System, "- [api/legacy/**] Legacy controllers are frozen.") {
				t.Errorf("reviewer prompt should list learnings:\n%s", req.System)
			}
			return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"maintainability","title":"refactor legacy","body":"x","confidence":0.8}]}`, nil
		},
		verify:  `{"verdicts":[{"id":1,"verdict":"false_positive","reason":"contradicts a team learning","confidence":0.9}],"missed":[]}`,
		summary: `{"summary":"s","cohorts":[],"effort":1,"scores":{"quality":90,"security":100,"maintainability":90}}`,
	}
	rc := defaults()
	(&config.Overrides{Learnings: []config.Learning{{Text: "Legacy controllers are frozen.", Path: "api/legacy/**"}}}).Apply(&rc)
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	res, err := (&Engine{LLM: fake, Limits: limits(), Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: rc})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("the verifier drops findings a learning rules out: %+v", res.Findings)
	}
	for _, r := range fake.requests {
		if r.Schema == verifySchema && !strings.Contains(r.System, "contradicts a team learning is a false positive") {
			t.Errorf("verifier prompt should carry learnings")
		}
	}
	bad := config.Overrides{Learnings: []config.Learning{{Text: strings.Repeat("x", config.MaxLearningText+1)}}}
	if err := bad.Validate(nil); err == nil {
		t.Error("over-long learnings must be rejected")
	}
}
