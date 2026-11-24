package review

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// labelFake answers the label call and passes the rest to scripted.
type labelFake struct {
	*scripted
	labels string
	err    error
	calls  int
}

func (l *labelFake) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if req.Schema == labelSchema {
		l.calls++
		l.mu.Lock()
		l.requests = append(l.requests, req)
		l.mu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return &llm.Response{JSON: json.RawMessage(l.labels), Usage: llm.Usage{InputTokens: 400, OutputTokens: 30}}, nil
	}
	return l.scripted.Complete(ctx, req)
}

var projectLabels = []vcs.Label{
	{Name: "type::bug"}, {Name: "type::feature"}, {Name: "type::maintenance", Description: "Refactors, dependency bumps"},
	{Name: "component::database"}, {Name: "dependency-type::minor"}, {Name: "priority::high"}, {Name: "backend"},
	{Name: "Quality Score::85"}, {Name: "workflow::triage"},
}

func labelsConfig(mode string) config.Labels {
	return config.Labels{Mode: mode, Allow: []string{"type::*", "component::*", "dependency-type::*", "backend"}, SuggestOnly: []string{"priority::*"}, Max: 4}
}

func TestLabelGlobAndScope(t *testing.T) {
	for _, c := range []struct {
		p, n string
		ok   bool
	}{
		{"type::*", "type::bug", true}, {"type::*", "TYPE::Bug", true}, {"type::*", "subtype::bug", false},
		{"backend", "Backend", true}, {"backend", "backend-api", false}, {"bug::?ogic", "bug::logic", true}, {"a.b", "aXb", false},
	} {
		if got := labelGlob(c.p, c.n); got != c.ok {
			t.Errorf("%s ~ %s: %v", c.p, c.n, got)
		}
	}
	if labelScope("type::bug") != "type" || labelScope("a::b::c") != "a::b" || labelScope("backend") != "" {
		t.Error("scope")
	}
	cands := labelCandidates(labelsConfig(config.LabelsApply), projectLabels)
	names := map[string]bool{}
	for _, c := range cands {
		names[c.Name] = c.suggestOnly
	}
	if len(cands) != 7 || !names["priority::high"] || names["type::bug"] {
		t.Errorf("candidates: %+v", cands)
	}
	if _, ok := names["Quality Score::85"]; ok {
		t.Error("score labels are never candidates")
	}
	if lit := LiteralLabels(labelsConfig(config.LabelsSuggest)); len(lit) != 1 || lit[0].Name != "backend" {
		t.Errorf("literal labels (Bitbucket): %+v", lit)
	}
}

func TestFilterLabels(t *testing.T) {
	cands := labelCandidates(labelsConfig(config.LabelsApply), projectLabels)
	li := &LabelInput{Current: []string{"component::api-old", "Quality Score::85", "type::maintenance"}, BotAdded: []string{"type::maintenance"}}
	// A person set component::api-old (not a candidate, but its scope counts).
	picked := []LabelPick{
		{Name: "type::bug", Reason: "fixes null handling"},
		{Name: "type::feature", Reason: "second type"},
		{Name: "component::database", Reason: "migration"},
		{Name: "invented::label", Reason: "x"},
		{Name: "priority::high", Reason: "outage"},
		{Name: "backend", Reason: "server code"},
		{Name: "dependency-type::minor", Reason: "bump"},
	}
	got, skipped := filterLabels(picked, cands, li, 4)
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "type::bug,priority::high,backend,dependency-type::minor" {
		t.Errorf("kept: %v", names)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Label] = s.Reason
	}
	if !strings.Contains(reasons["type::feature"], "one label per scope") || !strings.Contains(reasons["component::database"], "a person set another component") ||
		!strings.Contains(reasons["invented::label"], "not one of the allowed") {
		t.Errorf("skipped: %+v", skipped)
	}
	for _, p := range got {
		if p.Name == "priority::high" && !p.SuggestOnly {
			t.Error("priority is suggest-only")
		}
	}
	// The limit.
	if got, skipped := filterLabels(picked, cands, &LabelInput{}, 2); len(got) != 2 || !strings.Contains(skipped[len(skipped)-1].Reason, "limit of 2") {
		t.Errorf("max: %+v %+v", got, skipped)
	}
}

func TestLabelChanges(t *testing.T) {
	li := &LabelInput{Current: []string{"type::maintenance", "backend", "workflow::triage"}, BotAdded: []string{"type::maintenance"}, CanApply: true}
	lr := &LabelResult{Mode: config.LabelsApply, Suggested: []LabelPick{{Name: "type::bug"}, {Name: "backend"}, {Name: "priority::high", SuggestOnly: true}}}
	add, remove := LabelChanges(lr, li)
	if strings.Join(add, ",") != "type::bug" || strings.Join(remove, ",") != "type::maintenance" {
		t.Errorf("add %v remove %v (never a person's backend or workflow::triage, never suggest-only)", add, remove)
	}
	if bot := BotLabelsAfter(li, add, remove); strings.Join(bot, ",") != "type::bug" {
		t.Errorf("bot labels after: %v", bot)
	}
	lr.Reused = true
	if _, remove := LabelChanges(lr, li); len(remove) != 0 {
		t.Error("the same head removes nothing")
	}
	lr.Mode = config.LabelsSuggest
	if add, _ := LabelChanges(lr, li); add != nil {
		t.Error("suggest sets nothing")
	}
	li.CanApply = false
	lr.Mode = config.LabelsApply
	if add, _ := LabelChanges(lr, li); add != nil {
		t.Error("Bitbucket sets nothing")
	}
}

func labelReviewFake(labels string, err error) *labelFake {
	return &labelFake{scripted: &scripted{
		review: func(req llm.Request) (string, error) {
			return `{"files":[{"path":"main.go","summary":"Bumps the client."}],"comments":[]}`, nil
		},
		summary: `{"summary":"Bumps a dependency.","cohorts":[],"effort":1,"scores":{"quality":95,"security":95,"maintainability":95}}`,
	}, labels: labels, err: err}
}

func TestEngineLabels(t *testing.T) {
	fake := labelReviewFake(`{"labels":[{"name":"type::maintenance","reason":"dependency bump"},{"name":"dependency-type::minor","reason":"1.4 to 1.5"}]}`, nil)
	rc := defaults()
	rc.Labels = labelsConfig(config.LabelsSuggest)
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	in := Input{PR: &vcs.PR{Title: "chore: bump client", HeadSHA: "h1"}, Files: testFiles(t), Repo: rc, Labels: &LabelInput{Available: projectLabels}}
	res, err := e.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Labels == nil || len(res.Labels.Suggested) != 2 || fake.calls != 1 || res.LabelsUsage.InputTokens != 400 {
		t.Fatalf("labels: %+v calls %d", res.Labels, fake.calls)
	}
	body := SummaryMarkdown(res, SummaryOptions{})
	if !strings.Contains(body, "🏷️ **Suggested labels:** `type::maintenance` (dependency bump) · `dependency-type::minor` (1.4 to 1.5)") {
		t.Errorf("summary:\n%s", body)
	}
	if !strings.Contains(fake.requestsText(), "type::maintenance: Refactors, dependency bumps") {
		t.Error("descriptions reach the model")
	}

	// The same head again: the stored choice, no call.
	in.Labels.Prev, in.Labels.PrevSHA = res.Labels.Suggested, "h1"
	res, _ = e.Run(context.Background(), in)
	if fake.calls != 1 || !res.Labels.Reused || len(res.Labels.Suggested) != 2 {
		t.Errorf("reused: %+v calls %d", res.Labels, fake.calls)
	}
	// Off: no call, no line.
	in.Repo.Labels.Mode = config.LabelsOff
	in.Labels.PrevSHA = ""
	res, _ = e.Run(context.Background(), in)
	if res.Labels != nil || fake.calls != 1 {
		t.Error("off picks nothing")
	}
}

func TestLabelFailureKeepsReview(t *testing.T) {
	fake := labelReviewFake("", errors.New("model overloaded"))
	rc := defaults()
	rc.Labels = labelsConfig(config.LabelsApply)
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	res, err := e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "h"}, Files: testFiles(t), Repo: rc, Labels: &LabelInput{Available: projectLabels, CanApply: true}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scores == nil || !strings.Contains(strings.Join(res.Errors, " "), "labels: model overloaded") || len(res.Labels.Suggested) != 0 {
		t.Errorf("scores %+v errors %v labels %+v", res.Scores, res.Errors, res.Labels)
	}
	// No allowed label exists: noted, no call.
	none := labelReviewFake(`{"labels":[]}`, nil)
	e.LLM = none
	res, _ = e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "h"}, Files: testFiles(t), Repo: rc, Labels: &LabelInput{Available: []vcs.Label{{Name: "other"}}}})
	if none.calls != 0 || len(res.Labels.Skipped) != 1 {
		t.Errorf("no candidates: %+v calls %d", res.Labels, none.calls)
	}
}

func (s *scripted) requestsText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, r := range s.requests {
		for _, m := range r.Messages {
			b.WriteString(m.Content)
		}
	}
	return b.String()
}
