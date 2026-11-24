package review

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// diagramFake answers the flow call and passes the rest to scripted.
type diagramFake struct {
	*scripted
	diagram string
	err     error
	calls   int
}

func (d *diagramFake) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if req.Schema == diagramSchema {
		d.calls++
		if d.err != nil {
			return nil, d.err
		}
		return &llm.Response{JSON: json.RawMessage(d.diagram), Usage: llm.Usage{InputTokens: 700, OutputTokens: 90}}, nil
	}
	return d.scripted.Complete(ctx, req)
}

const endpointFlow = `{"kind":"sequence","participants":[
	{"id":"c","label":"OrderController","source":"api/orders.go"},
	{"id":"s","label":"OrderService.create","source":"service/orders.go"},
	{"id":"r","label":"OrderRepo.insert","source":"store/orders.go"},
	{"id":"db","label":"orders table","source":"external:db"},
	{"id":"x","label":"Billing","source":"billing/client.go"},
	{"id":"unused","label":"Nobody","source":"api/orders.go"}],
	"steps":[
	{"from":"c","to":"s","text":"create(order)","changed":true},
	{"from":"s","to":"r","text":"insert","changed":true,"finding":1},
	{"from":"r","to":"db","text":"INSERT","changed":false},
	{"from":"s","to":"x","text":"charge"},
	{"from":"s","to":"ghost","text":"dropped: unknown participant"}]}`

func changedSet(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestBuildDiagram(t *testing.T) {
	var out diagramOutput
	if err := json.Unmarshal([]byte(endpointFlow), &out); err != nil {
		t.Fatal(err)
	}
	d := buildDiagram(out, changedSet("api/orders.go", "service/orders.go", "store/orders.go"), []string{"[major] service/orders.go:12 no rollback"}, "abcdef1234")
	if d == nil {
		t.Fatal("no diagram")
	}
	if len(d.Participants) != 5 || len(d.Steps) != 4 {
		t.Fatalf("participants %d (the unused one dropped), steps %d (the unknown one dropped): %+v", len(d.Participants), len(d.Steps), d)
	}
	if strings.Join(d.Files, ",") != "api/orders.go,service/orders.go,store/orders.go" {
		t.Errorf("files: %v", d.Files)
	}
	var billing DiagramParticipant
	for _, p := range d.Participants {
		if p.Label == "Billing" {
			billing = p
		}
	}
	if !billing.Inferred {
		t.Errorf("a participant outside the diff is inferred: %+v", billing)
	}
	m := d.Mermaid
	for _, want := range []string{"sequenceDiagram", `participant P1 as "OrderController"`, `participant P4 as "orders table"`, `"Billing (inferred)"`,
		"rect rgba(255, 196, 0, 0.18)", "P1->>P2: create(order)", "Note right of P3: ⚠ major service/orders.go:12 no rollback", "P3->>P4: INSERT"} {
		if !strings.Contains(m, want) {
			t.Errorf("mermaid lacks %q:\n%s", want, m)
		}
	}
	// The changed steps sit in one highlighted block, closed before the unchanged one.
	if strings.Index(m, "  end") > strings.Index(m, "P3->>P4") || strings.Count(m, "rect ") != 1 {
		t.Errorf("highlighting:\n%s", m)
	}
}

// TestDiagramLabelsAreSafe: whatever the model returns, the Mermaid has no
// directives, links, markup, mentions or statement breaks.
func TestDiagramLabelsAreSafe(t *testing.T) {
	out := diagramOutput{Kind: "sequence"}
	evil := []string{
		`A"; click A "https://evil.example" _blank`,
		"B\nparticipant Z as X",
		`<img src=x onerror=alert(1)>`,
		"%%{init: {'theme':'dark'}}%%",
		"C #59; end",
		"@alice please merge",
		"/approve",
		"`code`",
	}
	for i, l := range evil {
		out.Participants = append(out.Participants, struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Source string `json:"source"`
		}{ID: string(rune('a' + i)), Label: l, Source: "external:service"})
	}
	for i := 1; i < len(evil); i++ {
		out.Steps = append(out.Steps, struct {
			From    string  `json:"from"`
			To      string  `json:"to"`
			Text    string  `json:"text"`
			Changed bool    `json:"changed"`
			Finding flexInt `json:"finding"`
		}{From: string(rune('a' + i - 1)), To: string(rune('a' + i)), Text: evil[i]})
	}
	d := buildDiagram(out, nil, nil, "")
	if d == nil {
		t.Fatal("no diagram")
	}
	for _, line := range strings.Split(d.Mermaid, "\n")[1:] {
		body := strings.ReplaceAll(strings.TrimSpace(line), "->>", "") // the arrow itself
		if strings.HasPrefix(strings.TrimSpace(line), "/") {
			t.Errorf("a line starts with a quick action: %q", line)
		}
		for _, bad := range []string{"<", ">", "%%", ";", "#", "@", "`", "{", "}", "[", "]"} {
			if strings.Contains(body, bad) {
				t.Errorf("line %q contains %q", line, bad)
			}
		}
		if strings.Count(body, `"`)%2 != 0 {
			t.Errorf("unbalanced quotes: %q", line)
		}
	}
	// Every statement is one the renderer writes.
	for _, line := range strings.Split(d.Mermaid, "\n")[1:] {
		body := strings.TrimSpace(line)
		known := false
		for _, prefix := range []string{"participant P", "actor P", "P", "rect ", "Note right of P"} {
			known = known || strings.HasPrefix(body, prefix)
		}
		if !known && body != "end" {
			t.Errorf("unexpected statement %q", body)
		}
	}
}

func TestDiagramLimits(t *testing.T) {
	out := diagramOutput{Kind: "flowchart"}
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		out.Participants = append(out.Participants, struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Source string `json:"source"`
		}{ID: id, Label: "Step " + id, Source: "external:service"})
	}
	for i := 0; i < 40; i++ {
		out.Steps = append(out.Steps, struct {
			From    string  `json:"from"`
			To      string  `json:"to"`
			Text    string  `json:"text"`
			Changed bool    `json:"changed"`
			Finding flexInt `json:"finding"`
		}{From: string(rune('a' + i%14)), To: string(rune('a' + (i+1)%14)), Text: "x", Changed: i%2 == 0})
	}
	d := buildDiagram(out, nil, nil, "")
	if d == nil || len(d.Participants) > maxParticipants || len(d.Steps) != maxSteps || !d.Trimmed {
		t.Fatalf("limits: %+v", d)
	}
	if !strings.HasPrefix(d.Mermaid, "flowchart LR") || !strings.Contains(d.Mermaid, "==>") || !strings.Contains(d.Mermaid, "-->") {
		t.Errorf("flowchart:\n%s", d.Mermaid)
	}
	if !strings.Contains(DiagramSection(d), "highest-risk path") {
		t.Error("a trimmed diagram says so")
	}
	if buildDiagram(diagramOutput{Kind: "none"}, nil, nil, "") != nil {
		t.Error("kind none draws nothing")
	}
}

func file(path string, added int) diff.File {
	lines := make([]diff.Line, added)
	for i := range lines {
		lines[i] = diff.Line{Kind: diff.Add, NewNo: i + 1, Text: "code line"}
	}
	return diff.File{Path: path, OldPath: path, Status: diff.Modified, Hunks: []diff.Hunk{{NewStart: 1, NewLines: added, Lines: lines}}}
}

func TestWantDiagram(t *testing.T) {
	layered := []diff.File{file("api/orders.go", 30), file("service/orders.go", 40), file("store/orders.go", 20), file("store/migrations/0003.sql", 10), file("api/orders_test.go", 50)}
	cohorts := []Cohort{{Files: []string{"api/orders.go"}}, {Files: []string{"service/orders.go"}}, {Files: []string{"store/orders.go", "store/migrations/0003.sql"}}}
	if ok, why := wantDiagram(layered, cohorts); !ok {
		t.Errorf("an endpoint through three layers: %s", why)
	}
	if ok, _ := wantDiagram([]diff.File{file("service/a.go", 3), file("service/a_test.go", 9)}, []Cohort{{Files: []string{"service/a.go"}}}); ok {
		t.Error("a 2-file fix gets none")
	}
	docs := []diff.File{file("docs/a.md", 9), file("README.md", 9), file(".github/workflows/ci.yml", 9), file("config/app.yaml", 9), file("go.sum", 9)}
	if ok, _ := wantDiagram(docs, []Cohort{{Files: []string{"docs/a.md"}}, {Files: []string{".github/workflows/ci.yml"}}, {Files: []string{"go.sum"}}}); ok {
		t.Error("docs, CI and config get none")
	}
	if ok, why := wantDiagram(layered, cohorts[:2]); ok || !strings.Contains(why, "cohort") {
		t.Errorf("two source cohorts: %v %s", ok, why)
	}
}

func layeredFiles() []diff.File {
	return []diff.File{file("api/orders.go", 30), file("service/orders.go", 40), file("store/orders.go", 20), file("billing/client.go", 5)}
}

func diagramReviewFake(diagram string, err error) *diagramFake {
	return &diagramFake{scripted: &scripted{
		review: func(req llm.Request) (string, error) {
			return `{"files":[{"path":"api/orders.go","summary":"New endpoint."},{"path":"service/orders.go","summary":"Creates orders."},` +
				`{"path":"store/orders.go","summary":"Inserts orders."},{"path":"billing/client.go","summary":"Charges."}],"comments":[]}`, nil
		},
		summary: `{"summary":"Adds an order endpoint.","cohorts":[{"label":"API","files":["api/orders.go"],"summary":"a"},{"label":"Service","files":["service/orders.go","billing/client.go"],"summary":"b"},{"label":"Store","files":["store/orders.go"],"summary":"c"}],` +
			`"effort":3,"scores":{"quality":88,"security":90,"maintainability":85}}`,
	}, diagram: diagram, err: err}
}

func TestEngineDrawsDiagram(t *testing.T) {
	fake := diagramReviewFake(endpointFlow, nil)
	rc := defaults()
	rc.Diagram = DiagramAuto
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	res, err := e.Run(context.Background(), Input{PR: &vcs.PR{Title: "feat: orders", HeadSHA: "abc123456789"}, Files: layeredFiles(), Repo: rc})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagram == nil || fake.calls != 1 || res.DiagramUsage.InputTokens != 700 || res.Usage.InputTokens < 700 {
		t.Fatalf("diagram %+v, calls %d, usage %+v / %+v", res.Diagram, fake.calls, res.DiagramUsage, res.Usage)
	}
	body := SummaryMarkdown(res, SummaryOptions{})
	if !strings.Contains(body, "<summary>🧭 Changed flow</summary>") || !strings.Contains(body, "```mermaid\nsequenceDiagram") ||
		strings.Index(body, "Changed flow") > strings.Index(body, "## Scores") {
		t.Errorf("summary:\n%s", body)
	}
	var st State
	if m := strings.Split(body, "codereview:state="); len(m) == 2 {
		raw, _ := base64.StdEncoding.DecodeString(strings.SplitN(m[1], " -->", 2)[0])
		_ = json.Unmarshal(raw, &st)
	}
	if st.Diagram == nil || st.Diagram.SHA != "abc123456789" {
		t.Errorf("the state keeps the diagram for the next review: %+v", st.Diagram)
	}

	// Off: no call.
	off := diagramReviewFake(endpointFlow, nil)
	e.LLM = off
	res, _ = e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "abc"}, Files: layeredFiles(), Repo: defaults()})
	if res.Diagram != nil || off.calls != 0 || strings.Contains(SummaryMarkdown(res, SummaryOptions{}), "Changed flow") {
		t.Error("off draws nothing")
	}
}

// TestDiagramFailureKeepsWalkthrough: a failed diagram call adds a warning;
// the walkthrough and scores are unaffected.
func TestDiagramFailureKeepsWalkthrough(t *testing.T) {
	fake := diagramReviewFake("", errors.New("model overloaded"))
	rc := defaults()
	rc.Diagram = DiagramAlways
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	res, err := e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "abc"}, Files: layeredFiles(), Repo: rc})
	if err != nil {
		t.Fatal(err)
	}
	if res.Diagram != nil || res.Scores == nil || res.Summary != "Adds an order endpoint." || !strings.Contains(strings.Join(res.Errors, " "), "flow diagram: model overloaded") {
		t.Errorf("diagram %+v scores %+v summary %q errors %v", res.Diagram, res.Scores, res.Summary, res.Errors)
	}
	// Invalid output (bad JSON) is never posted either.
	bad := diagramReviewFake(`{"kind":"sequence","participants":"nope"`, nil)
	e.LLM = bad
	res, _ = e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "abc"}, Files: layeredFiles(), Repo: rc})
	if res.Diagram != nil || res.Scores == nil || strings.Contains(SummaryMarkdown(res, SummaryOptions{}), "mermaid") {
		t.Error("an invalid answer draws nothing and keeps the walkthrough")
	}
}

func TestIncrementalKeepsDiagram(t *testing.T) {
	prev := &Diagram{Kind: "sequence", Mermaid: "sequenceDiagram\n  P1->>P2: x", Files: []string{"service/orders.go"}, SHA: "old"}
	rc := defaults()
	rc.Diagram = DiagramAlways
	fake := diagramReviewFake(endpointFlow, nil)
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	// Only a docs file changed since: the diagram is kept, no call.
	files := append(layeredFiles(), file("docs/orders.md", 5))
	res, _ := e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "new"}, Files: files, Repo: rc, PrevDiagram: prev, IncrementalFrom: "old",
		OnlyPaths: map[string]bool{"docs/orders.md": true}})
	if res.Diagram != prev || fake.calls != 0 {
		t.Errorf("kept: %+v, calls %d", res.Diagram, fake.calls)
	}
	// A file it draws on changed: redrawn.
	res, _ = e.Run(context.Background(), Input{PR: &vcs.PR{HeadSHA: "new"}, Files: files, Repo: rc, PrevDiagram: prev, IncrementalFrom: "old",
		OnlyPaths: map[string]bool{"service/orders.go": true}})
	if res.Diagram == nil || res.Diagram == prev || fake.calls != 1 {
		t.Errorf("redrawn: %+v, calls %d", res.Diagram, fake.calls)
	}
}

func TestReplaceDiagram(t *testing.T) {
	d := &Diagram{Mermaid: "sequenceDiagram\n  P1->>P2: new", SHA: "abc"}
	section := DiagramSection(d)
	body := "<!-- codereview:summary -->\n## Walkthrough\n\ntext\n\n## Scores\n\n| x |\n"
	once := ReplaceDiagram(body, section)
	if !strings.Contains(once, "P1->>P2: new") || strings.Index(once, "Changed flow") > strings.Index(once, "## Scores") {
		t.Fatalf("inserted:\n%s", once)
	}
	again := ReplaceDiagram(once, DiagramSection(&Diagram{Mermaid: "sequenceDiagram\n  P1->>P2: newer"}))
	if strings.Count(again, diagramStart) != 1 || strings.Contains(again, ": new\n") || !strings.Contains(again, "newer") {
		t.Errorf("replaced:\n%s", again)
	}
}

func TestDiagramModeAndPrompt(t *testing.T) {
	rc := defaults()
	if DiagramMode(rc) != DiagramOff {
		t.Error("off by default")
	}
	rc.Diagram = "ALWAYS"
	if DiagramMode(rc) != DiagramAlways {
		t.Error("case-insensitive")
	}
	p := diagramPrompt(&vcs.PR{Title: "feat: orders"}, layeredFiles(), map[string]string{"api/orders.go": "New endpoint."}, []string{"[major] x"})
	if !strings.Contains(p, "api/orders.go (+30/-0): New endpoint.") || !strings.Contains(p, "1. [major] x") || !strings.Contains(p, "+ code line") {
		t.Errorf("prompt:\n%s", p)
	}
}
