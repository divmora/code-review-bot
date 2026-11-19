package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/pkg/diff"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// scripted returns review JSON for review calls and walkthrough JSON for
// summary calls.
type scripted struct {
	mu       sync.Mutex
	review   func(req llm.Request) (string, error)
	summary  string
	verify   string // verification JSON; empty confirms nothing and adds nothing
	requests []llm.Request
}

func (s *scripted) Name() string { return "fake:model" }
func (s *scripted) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if req.Schema == reviewSchema {
		out, err := s.review(req)
		if err != nil {
			return nil, err
		}
		return &llm.Response{JSON: json.RawMessage(out), Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
	}
	if req.Schema == verifySchema {
		out := s.verify
		if out == "" {
			out = `{"verdicts":[],"missed":[]}`
		}
		return &llm.Response{JSON: json.RawMessage(out)}, nil
	}
	if req.Schema == summarySchema {
		if s.summary == "" {
			return nil, errors.New("no summary scripted")
		}
		return &llm.Response{JSON: json.RawMessage(s.summary)}, nil
	}
	return &llm.Response{Text: "answer"}, nil
}

var goPatch = `@@ -1,5 +1,7 @@
 package main

 func main() {
-	println("hi")
+	x := load()
+	println(x)
+	key := "` + fakeAWSKey + `"
 }
@@ -40,3 +42,3 @@ func other() {
 	a := 1
-	b := 2
+	b := 3
 	_ = a + b`

// fakeAWSKey is split so this source file does not trip the scanner.
var fakeAWSKey = "AKIA" + "ABCDEFGHIJKLMNOP"

func testFiles(t *testing.T) []diff.File {
	t.Helper()
	h, err := diff.ParsePatch(goPatch)
	if err != nil {
		t.Fatal(err)
	}
	lockH, _ := diff.ParsePatch("@@ -1 +1 @@\n-a\n+b")
	return []diff.File{
		{Path: "main.go", OldPath: "main.go", Status: diff.Modified, Hunks: h},
		{Path: "go.sum", OldPath: "go.sum", Status: diff.Modified, Hunks: lockH},
	}
}

func defaults() config.RepoConfig {
	rc, _ := config.ParseRepoConfig(nil, config.RepoConfig{})
	return rc
}

func limits() config.Review {
	return config.Review{Concurrency: 2, BatchTokens: 40000, MaxFiles: 100, FullFileMaxLines: 800}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRunValidatesAndRanks(t *testing.T) {
	fake := &scripted{
		review: func(req llm.Request) (string, error) {
			return `{"files":[{"path":"main.go","summary":"Loads x and prints it."}],"comments":[
				{"path":"main.go","start_line":4,"end_line":5,"severity":"major","category":"bug","title":"load() error ignored","body":"Handle the error.","suggestion":"x, err := load()\nif err != nil { return }","confidence":0.9},
				{"path":"main.go","start_line":1,"end_line":1,"severity":"major","category":"bug","title":"untouched line","body":"x","confidence":0.9},
				{"path":"./main.go","start_line":"43","end_line":"43","severity":"minor","category":"maintainability","title":"magic number","body":"Name it.","confidence":"0.8"},
				{"path":"main.go","start_line":44,"end_line":44,"severity":"nitpick","category":"style","title":"nit","body":"nit","confidence":0.9},
				{"path":"nope.go","start_line":1,"end_line":1,"severity":"critical","category":"bug","title":"wrong file","body":"x","confidence":1},
				{"path":"main.go","start_line":43,"end_line":43,"severity":"minor","category":"bug","title":"low confidence","body":"x","confidence":0.2}
			]}`, nil
		},
		summary: `{"summary":"Adds loading.","cohorts":[{"label":"Main","files":["main.go","ghost.go"],"summary":"Loading."}],"effort":"2","poem":""}`,
	}
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	res, err := e.Run(context.Background(), Input{PR: &vcs.PR{Title: "feat: load", HeadSHA: "abc"}, Files: testFiles(t), Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, f := range res.Findings {
		titles = append(titles, f.Title)
	}
	got := strings.Join(titles, " | ")
	// Secret (critical) first, then major, then minor. The nitpick is below
	// balanced's minimum; untouched line, wrong file and low confidence drop.
	want := "Possible hard-coded AWS access key ID | load() error ignored | magic number"
	if got != want {
		t.Fatalf("findings:\n got %s\nwant %s", got, want)
	}
	if res.Dropped != 4 {
		t.Errorf("dropped = %d, want 4", res.Dropped)
	}
	if res.Findings[1].Suggestion == "" || res.Findings[1].StartLine != 4 {
		t.Errorf("multi-line suggestion lost: %+v", res.Findings[1])
	}
	if res.Findings[2].OldLine != 0 {
		t.Errorf("added line should have no old line: %+v", res.Findings[2])
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Path != "go.sum" {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if len(res.Cohorts) != 1 || len(res.Cohorts[0].Files) != 1 {
		t.Errorf("cohorts should drop ghost files: %+v", res.Cohorts)
	}
	if res.Effort != 2 || res.FileSummaries["main.go"] == "" {
		t.Errorf("walkthrough not applied: %+v", res)
	}
	// Prompt includes the annotated diff and the PR title.
	user := fake.requests[0].Messages[0].Content
	if !strings.Contains(user, "    5 + | \tprintln(x)") || !strings.Contains(user, "feat: load") {
		t.Errorf("prompt missing diff/PR context:\n%s", user)
	}
	if strings.Contains(user, "go.sum") {
		t.Errorf("lockfile should not reach the model")
	}

	md := SummaryMarkdown(res, SummaryOptions{BotName: "bot", Posted: 3, Commands: true})
	if !strings.Contains(md, "`@bot review`") {
		t.Errorf("commands footer missing")
	}
	if strings.Contains(SummaryMarkdown(res, SummaryOptions{}), "@codereview") {
		t.Errorf("CLI summaries must not advertise chat commands")
	}
	for _, s := range []string{vcs.SummaryMarker, "## Walkthrough", "| **Main**<br>`main.go` |", "reviewed_sha=abc", "codereview:state="} {
		if !strings.Contains(md, s) {
			t.Errorf("summary missing %q:\n%s", s, md)
		}
	}
	body := CommentBody(res.Findings[1], vcs.GitLab)
	if !strings.Contains(body, "```suggestion:-1+0") || !strings.Contains(body, "codereview:fp=") {
		t.Errorf("gitlab comment body wrong:\n%s", body)
	}
}

func TestKnownFingerprintsAndCap(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[
			{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"A","body":"a","confidence":0.9},
			{"path":"main.go","start_line":5,"end_line":5,"severity":"major","category":"bug","title":"B","body":"b","confidence":0.9},
			{"path":"main.go","start_line":43,"end_line":43,"severity":"minor","category":"bug","title":"C","body":"c","confidence":0.9}
		]}`, nil
	}}
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	rc := defaults()
	rc.MaxComments = 1
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "}" // drop the secret line content
	first, err := e.Run(context.Background(), Input{Files: files, Repo: rc})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Findings) != 1 || len(first.Overflow) != 2 {
		t.Fatalf("cap: findings=%d overflow=%d", len(first.Findings), len(first.Overflow))
	}
	known := map[string]bool{first.Findings[0].Fingerprint: true}
	second, _ := e.Run(context.Background(), Input{Files: files, Repo: rc, Known: known})
	if second.AlreadyPosted != 1 || second.Findings[0].Title == first.Findings[0].Title {
		t.Errorf("known fingerprint should be skipped: %+v", second.Findings)
	}
}

func TestPartialAndTotalFailure(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	fake := &scripted{review: func(req llm.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if strings.Contains(req.Messages[0].Content, `path="b.go"`) {
			return "", errors.New("boom")
		}
		return `{"files":[],"comments":[]}`, nil
	}}
	mk := func(p string) diff.File {
		h, _ := diff.ParsePatch("@@ -1 +1 @@\n-a\n+" + strings.Repeat("x", 300))
		return diff.File{Path: p, OldPath: p, Status: diff.Modified, Hunks: h}
	}
	lim := limits()
	lim.BatchTokens = 120 // one file per batch
	e := &Engine{LLM: fake, Limits: lim, Logger: quiet}
	res, err := e.Run(context.Background(), Input{Files: []diff.File{mk("a.go"), mk("b.go")}, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) < 1 || !strings.Contains(res.Errors[0], "b.go") || len(res.Reviewed) != 2 {
		t.Errorf("partial failure not reported: %+v", res.Errors)
	}
	if res.Summary == "" || len(res.Cohorts) != 2 {
		t.Errorf("walkthrough fallback missing: %+v", res)
	}
	_, err = e.Run(context.Background(), Input{Files: []diff.File{mk("b.go")}, Repo: defaults()})
	if err == nil {
		t.Errorf("all batches failing must be an error")
	}
}

func TestIncrementalAndTooLarge(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) { return `{"files":[],"comments":[]}`, nil }}
	e := &Engine{LLM: fake, Limits: limits(), Logger: quiet}
	files := testFiles(t)
	res, err := e.Run(context.Background(), Input{
		Files: files, Repo: defaults(),
		OnlyPaths:         map[string]bool{"other.go": true},
		PrevFileSummaries: map[string]string{"main.go": "old summary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unchanged) != 1 || res.FileSummaries["main.go"] != "old summary" || len(fake.requests) != 0 {
		t.Errorf("incremental: %+v, calls=%d", res, len(fake.requests))
	}
	lim := limits()
	lim.MaxFiles = 0
	e.Limits.MaxFiles = 0
	res, _ = e.Run(context.Background(), Input{Files: files, Repo: defaults()})
	if !res.TooLarge {
		t.Errorf("want TooLarge")
	}
}

func TestSplitLargeFile(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("@@ -0,0 +1,400 @@\n")
	for i := 0; i < 400; i++ {
		sb.WriteString("+" + strings.Repeat("y", 60) + "\n")
	}
	h, _ := diff.ParsePatch(sb.String())
	f := diff.File{Path: "big.go", OldPath: "big.go", Status: diff.Added, Hunks: h}
	e := &Engine{Limits: config.Review{BatchTokens: 2000}}
	units := e.buildUnits(Input{Repo: defaults()}, []*diff.File{&f}, nil)
	if len(units) < 3 {
		t.Fatalf("want the file split into parts, got %d", len(units))
	}
	for _, u := range units {
		if u.tokens > 2000 {
			t.Errorf("unit over budget: %d", u.tokens)
		}
		if !strings.Contains(u.text, "part=") {
			t.Errorf("parts should be labelled")
		}
	}
}

func TestCleanSuggestion(t *testing.T) {
	cases := map[string]string{
		"x := 1":                       "x := 1",
		"```go\nx := 1\n```":           "x := 1",
		"```suggestion\nx := 1\n```\n": "x := 1",
		"a\n```\nb":                    "",
	}
	for in, want := range cases {
		if got := cleanSuggestion(in); got != want {
			t.Errorf("cleanSuggestion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContextExcerptForLargeFiles(t *testing.T) {
	var src strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&src, "line %d\n", i)
	}
	h1, _ := diff.ParsePatch("@@ -100,3 +100,3 @@\n line 100\n-old\n+line 101\n line 102\n")
	h2, _ := diff.ParsePatch("@@ -130,1 +130,1 @@\n-x\n+line 130\n")
	h3, _ := diff.ParsePatch("@@ -1500,1 +1500,1 @@\n-y\n+line 1500\n")
	hunks := append(append(h1, h2...), h3...)
	e := &Engine{Limits: config.Review{FullFileMaxLines: 800, ContextLines: 20}}
	block := e.contextBlock(src.String(), hunks)
	// Windows around 100-102 and 130 overlap and merge; 1500 is separate.
	if !strings.Contains(block, `total_lines="2000" ranges="80-150,1480-1520"`) {
		t.Fatalf("ranges wrong:\n%s", block[:min(300, len(block))])
	}
	if !strings.Contains(block, "   80 | line 80") || !strings.Contains(block, "  ... |") || strings.Contains(block, "| line 79\n") {
		t.Errorf("excerpt content wrong")
	}
	small := e.contextBlock("a\nb\n", hunks)
	if !strings.Contains(small, "<full_content lines=\"2\">") {
		t.Errorf("small files are sent whole: %s", small)
	}
	if e.contextBlock("", hunks) != "" {
		t.Error("no content, no block")
	}
}

func TestVerificationPass(t *testing.T) {
	fake := &scripted{
		review: func(llm.Request) (string, error) {
			return `{"files":[],"comments":[
				{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"real bug","body":"x","confidence":0.9},
				{"path":"main.go","start_line":5,"end_line":5,"severity":"major","category":"bug","title":"misread term","body":"x","confidence":0.9},
				{"path":"main.go","start_line":43,"end_line":43,"severity":"major","category":"security","title":"depends on config","body":"x","confidence":0.9}
			]}`, nil
		},
		verify: `{"verdicts":[
				{"id":1,"verdict":"confirmed","reason":"ok"},
				{"id":2,"verdict":"false_positive","reason":"direct means admin menu here"},
				{"id":3,"verdict":"question","reason":"depends on blocked roles config"}
			],"missed":[
				{"path":"main.go","start_line":6,"end_line":6,"severity":"major","category":"security","title":"admin accounts not covered","body":"y","confidence":0.8},
				{"path":"main.go","start_line":6,"end_line":6,"severity":"minor","category":"style","title":"too small to add","body":"y","confidence":0.8},
				{"path":"main.go","start_line":1,"end_line":1,"severity":"critical","category":"bug","title":"untouched line","body":"y","confidence":0.9}
			]}`,
		summary: `{"summary":"s","cohorts":[],"effort":2,"scores":{"quality":95,"security":97,"maintainability":90}}`,
	}
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3" // no secret
	withAdd := limits()
	withAdd.VerifyCanAdd = true
	e := &Engine{LLM: fake, Limits: withAdd, Logger: quiet}
	res, err := e.Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.Findings {
		got = append(got, f.Title+"/"+f.Kind+"/"+string(f.Severity)+"/"+f.Source)
	}
	// Default policy: the disputed MAJOR finding is kept as a question.
	want := "real bug//major/llm,admin accounts not covered//major/verifier,misread term/question/minor/llm,depends on config/question/minor/llm"
	if strings.Join(got, ",") != want {
		t.Fatalf("findings:\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	v := res.Verification
	if v == nil || !v.Ran || v.Confirmed != 1 || v.FalsePositives != 0 || v.Questions != 2 || v.Added != 1 {
		t.Errorf("verification = %+v", v)
	}
	for _, f := range res.Findings {
		if f.Title == "misread term" && !strings.Contains(f.Body, "A second review disputes this: direct means admin menu here") {
			t.Errorf("disputed finding should carry the verifier's reason: %q", f.Body)
		}
	}

	// verify_can_drop: all removes it and lists it for auditing.
	lim := limits()
	lim.VerifyCanDrop = "all"
	lim.VerifyCanAdd = true
	res, err = (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	v = res.Verification
	if v.FalsePositives != 1 || len(v.Removed) != 1 || v.Removed[0].Title != "misread term" || v.Removed[0].Was != Major || v.Removed[0].Reason == "" {
		t.Errorf("removed = %+v", v.Removed)
	}
	// Model scores are capped by findings: a major security issue caps security at 70.
	if res.Scores == nil || res.Scores.Security != 70 || res.Scores.Quality != 70 || res.Scores.Maintainability != 90 {
		t.Errorf("scores = %+v", res.Scores)
	}
	body := CommentBody(res.Findings[2], "github")
	if !strings.Contains(body, "❓ Question") {
		t.Errorf("question rendering:\n%s", body)
	}
	md := SummaryMarkdown(res, SummaryOptions{})
	if !strings.Contains(md, "## Scores") || !strings.Contains(md, "🟠 **70**") || !strings.Contains(md, "1 false positives removed") || !strings.Contains(md, "Removed by verification") {
		t.Errorf("summary missing scores/verification:\n%s", md)
	}
}

func TestVerifyDisabledAndFallbackScores(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"critical","category":"security","title":"sqli","body":"x","confidence":0.9}]}`, nil
	}}
	off := false
	lim := limits()
	lim.Verify = &off
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	res, err := (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.requests {
		if r.Schema == verifySchema {
			t.Error("verify: false must skip the verification call")
		}
	}
	// No walkthrough scripted, so scores come from findings: critical security caps at 40.
	if res.Scores == nil || res.Scores.Security > 40 || res.Scores.Quality < 90 {
		t.Errorf("fallback scores = %+v", res.Scores)
	}
}

func TestScoreCapsAndBands(t *testing.T) {
	fs := []Finding{
		{Category: "bug", Severity: Minor},
		{Category: "maintainability", Severity: Major},
		{Category: "security", Severity: Critical, Kind: KindQuestion}, // questions do not cap
	}
	c := scoreCaps(fs)
	if c.Quality != 88 || c.Maintainability != 70 || c.Security != 100 {
		t.Errorf("caps = %+v", c)
	}
	s := clampScores(Scores{Quality: 150, Security: 0, Maintainability: 99}, c)
	if s.Quality != 88 || s.Security != 1 || s.Maintainability != 70 {
		t.Errorf("clamped = %+v", s)
	}
	if ScoreBand(80) != "green" || ScoreBand(79) != "orange" || ScoreBand(49) != "red" {
		t.Error("bands")
	}
}

// namedScripted is a scripted client with its own name.
type namedScripted struct {
	*scripted
	name string
}

func (n namedScripted) Name() string { return n.name }

func TestSeparateVerifierModel(t *testing.T) {
	reviewer := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"maybe","body":"x","confidence":0.9}]}`, nil
	}, verify: `{"verdicts":[{"id":1,"verdict":"confirmed","reason":"reviewer must not verify itself"}],"missed":[]}`}
	verifier := namedScripted{&scripted{verify: `{"verdicts":[{"id":1,"verdict":"false_positive","reason":"wrong"}],"missed":[]}`}, "fake:verifier"}
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	lim := limits()
	lim.VerifyCanDrop = "all"
	e := &Engine{LLM: reviewer, VerifyLLM: verifier, Limits: lim, Logger: quiet}
	res, err := e.Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 || res.Verification.FalsePositives != 1 || res.Verification.Model != "fake:verifier" {
		t.Errorf("separate verifier not used: findings=%d verification=%+v", len(res.Findings), res.Verification)
	}
	for _, r := range reviewer.requests {
		if r.Schema == verifySchema {
			t.Error("the review model must not run the verification when a verifier is set")
		}
	}
}

func TestVerifierCannotAddByDefault(t *testing.T) {
	fake := &scripted{
		review: func(llm.Request) (string, error) {
			return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"real","body":"x","confidence":0.9}]}`, nil
		},
		verify: `{"verdicts":[{"id":1,"verdict":"confirmed","reason":"ok"}],"missed":[{"path":"main.go","start_line":5,"end_line":5,"severity":"critical","category":"security","title":"Command injection","body":"invented","confidence":0.9}]}`,
	}
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	res, err := (&Engine{LLM: fake, Limits: limits(), Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Title != "real" || res.Verification.Added != 0 {
		t.Errorf("verifier additions must be ignored by default: %+v", res.Findings)
	}
}

func TestMergeOverlapping(t *testing.T) {
	f := func(title string, line int, sev Severity, cat, kind string) Finding {
		return Finding{Path: "a.php", StartLine: line, EndLine: line, Severity: sev, Category: cat, Kind: kind, Title: title, Confidence: 0.8}
	}
	res := &Result{}
	got := mergeOverlapping(res, []Finding{
		f("Original PDF overwritten during validation", 32, Major, "bug", ""),
		f("Do not overwrite the upload during validation", 31, Minor, "bug", KindQuestion), // restates the issue
		f("Null check missing on order", 40, Major, "bug", ""),
		f("Wrong currency rounding", 41, Major, "bug", ""), // different bug on the next line: kept
		f("Index out of range on page", 41, Minor, "reliability", ""),
	})
	var titles []string
	for _, x := range got {
		titles = append(titles, x.Title)
	}
	want := "Original PDF overwritten during validation|Null check missing on order|Wrong currency rounding|Index out of range on page"
	if strings.Join(titles, "|") != want || res.Dropped != 1 {
		t.Errorf("merged = %v (dropped %d)", titles, res.Dropped)
	}
}

func TestPostedBefore(t *testing.T) {
	existing := []vcs.PostedComment{
		{Path: "OrderRepository.php", Line: 289, Title: "Enforce the credit-limit rule on order submission"},
		{Path: "Pdf.php", Line: 31, Title: "Do not overwrite the upload during validation", Question: true},
	}
	cases := []struct {
		f    Finding
		want bool
	}{
		// Same issue, reworded, a line away: already posted.
		{Finding{Path: "OrderRepository.php", StartLine: 287, EndLine: 288, Title: "Enforce the credit-limit restriction at submission"}, true},
		// Restated as an issue next to the earlier question: already posted.
		{Finding{Path: "Pdf.php", StartLine: 32, EndLine: 32, Title: "Original PDF overwritten during validation"}, true},
		// Different topic at the same place: new.
		{Finding{Path: "OrderRepository.php", StartLine: 289, EndLine: 289, Title: "Null order crashes the formatter"}, false},
		// Same title, far away: new.
		{Finding{Path: "OrderRepository.php", StartLine: 400, EndLine: 400, Title: "Enforce the credit-limit rule on order submission"}, false},
		// Other file: new.
		{Finding{Path: "Other.php", StartLine: 289, EndLine: 289, Title: "Enforce the credit-limit rule on order submission"}, false},
	}
	for _, c := range cases {
		if got := postedBefore(c.f, existing); got != c.want {
			t.Errorf("%s:%d %q: got %v want %v", c.f.Path, c.f.EndLine, c.f.Title, got, c.want)
		}
	}
}

func TestRepeatedFindingsStillCountForScores(t *testing.T) {
	fake := &scripted{review: func(llm.Request) (string, error) {
		return `{"files":[],"comments":[{"path":"main.go","start_line":4,"end_line":4,"severity":"critical","category":"security","title":"SQL injection in search query","body":"x","confidence":0.9}]}`, nil
	}}
	off := false
	lim := limits()
	lim.Verify = &off
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	existing := []vcs.PostedComment{{Path: "main.go", Line: 4, Title: "SQL injection in the search query builder"}}
	res, err := (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults(), Existing: existing})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 || res.AlreadyPosted != 1 || len(res.Repeated) != 1 {
		t.Fatalf("repeat not recognised: findings=%d already=%d", len(res.Findings), res.AlreadyPosted)
	}
	if res.Scores == nil || res.Scores.Security > 40 {
		t.Errorf("a still-present critical issue must keep security capped: %+v", res.Scores)
	}
}

// TestSpeculativeAndLoosenedChecks reproduces a real review: a speculative
// "SQL injection" through an internal constant was posted as a question and
// dragged Security to 60, while loosened validation checks went unmentioned.
func TestSpeculativeAndLoosenedChecks(t *testing.T) {
	fake := &scripted{
		review: func(req llm.Request) (string, error) {
			for _, want := range []string{"attacker or user controls", "Loosened checks", `kind "question"`} {
				if !strings.Contains(req.System, want) {
					t.Errorf("reviewer prompt should contain %q", want)
				}
			}
			return `{"files":[],"comments":[
				{"path":"main.go","start_line":4,"end_line":4,"severity":"major","category":"security","title":"SQL injection via column name","body":"if ever exposed","confidence":0.7},
				{"path":"main.go","start_line":5,"end_line":5,"severity":"major","category":"bug","title":"wrong total","body":"x","confidence":0.6},
				{"path":"main.go","start_line":6,"end_line":6,"severity":"major","category":"security","title":"rows without an id now pass the check","body":"used to fail","confidence":0.8,"kind":"question"}
			]}`, nil
		},
		verify: `{"verdicts":[
				{"id":1,"verdict":"speculative","reason":"the column is a hardcoded literal","confidence":0.6},
				{"id":2,"verdict":"false_positive","reason":"the total is recomputed below","confidence":0.9},
				{"id":3,"verdict":"question","reason":"intent unclear","confidence":0.7}
			],"missed":[]}`,
		summary: `{"summary":"s","cohorts":[],"effort":2,"scores":{"quality":70,"security":60,"maintainability":90}}`,
	}
	files := testFiles(t)[:1]
	files[0].Hunks[0].Lines[6].Text = "\tx := 3"
	res, err := (&Engine{LLM: fake, Limits: limits(), Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.Findings {
		got = append(got, f.Title+"/"+f.Kind+"/"+string(f.Severity))
	}
	if strings.Join(got, ",") != "rows without an id now pass the check/question/minor" {
		t.Fatalf("speculative and confidently-false majors must be dropped, the loosened check kept as a question: %v", got)
	}
	v := res.Verification
	if v.FalsePositives != 2 || len(v.Removed) != 2 || !strings.HasPrefix(v.Removed[0].Reason, "Speculative") {
		t.Errorf("removed findings should be listed: %+v", v.Removed)
	}
	if res.Scores.Security != 85 || res.Scores.Quality != 85 || res.Scores.Maintainability != 90 {
		t.Errorf("scores with no remaining issue must not stay low: %+v", res.Scores)
	}

	// verify_can_drop: minor keeps the old behaviour: disputed majors become questions.
	lim := limits()
	lim.VerifyCanDrop = "minor"
	res, _ = (&Engine{LLM: fake, Limits: lim, Logger: quiet}).Run(context.Background(), Input{Files: files, Repo: defaults()})
	if len(res.Findings) != 3 || res.Verification.FalsePositives != 0 {
		t.Errorf("minor mode must not drop majors: %+v", res.Findings)
	}
}

func TestCanDrop(t *testing.T) {
	for _, c := range []struct {
		mode    string
		sev     Severity
		verdict string
		conf    float64
		want    bool
	}{
		{"", Minor, "false_positive", 0, true},
		{"", Major, "false_positive", 0.5, false},
		{"", Major, "false_positive", 0.8, true},
		{"", Critical, "speculative", 0.1, true},
		{"confident", Critical, "false_positive", 0.79, false},
		{"minor", Critical, "speculative", 1, false},
		{"all", Critical, "false_positive", 0, true},
	} {
		if got := canDrop(c.mode, c.sev, c.verdict, c.conf); got != c.want {
			t.Errorf("canDrop(%q,%s,%s,%.2f) = %v", c.mode, c.sev, c.verdict, c.conf, got)
		}
	}
}

func TestScoreFloors(t *testing.T) {
	fs := []Finding{{Category: "security", Severity: Major, Kind: KindQuestion}, {Category: "bug", Severity: Nitpick}}
	if s := finalScores(Scores{Quality: 40, Security: 30, Maintainability: 95}, fs); s.Security != 85 || s.Quality != 85 || s.Maintainability != 95 {
		t.Errorf("questions and nitpicks do not support low scores: %+v", s)
	}
	fs = append(fs, Finding{Category: "security", Severity: Major})
	if s := finalScores(Scores{Quality: 90, Security: 30, Maintainability: 95}, fs); s.Security != 30 {
		t.Errorf("a remaining major security issue keeps the model's low score: %+v", s)
	}
}
