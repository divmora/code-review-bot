package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/llm"
	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeLLM is an OpenAI-compatible server. Review calls get a comment on
// line 3 of app.go; walkthrough calls get a summary.
func fakeLLM(t *testing.T, reviewJSON string) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sys, user := body.Messages[0].Content, body.Messages[len(body.Messages)-1].Content
		mu.Lock()
		prompts = append(prompts, sys+"\n---\n"+user)
		mu.Unlock()
		content := `{"summary":"Adds a handler.","cohorts":[{"label":"App","files":["app.go"],"summary":"New handler."}],"effort":2,"poem":""}`
		if strings.Contains(sys, "senior software engineer doing a code review") {
			content = reviewJSON
		}
		b, _ := json.Marshal(content)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`, b)
	}))
	t.Cleanup(srv.Close)
	return srv, &prompts
}

const appPatch = "@@ -1,3 +1,6 @@\n package app\n \n-func A() {}\n+func A() {\n+\tpanic(\"todo\")\n+}\n+// trailing\n"

// fakeGitHub serves the endpoints the runner uses and records writes.
type fakeGitHub struct {
	mu          sync.Mutex
	head        string
	reviews     []map[string]any
	singles     []map[string]any
	issueNotes  []string
	patched     []string
	rejectBatch bool
	summaryID   int
	summary     string
	inline      []string
	compare     []string
	prPatches   []map[string]any
	statuses    []map[string]any
	state       string // "" = open
	merged      bool
}

func (g *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/repos/o/r/pulls/7":
			state := g.state
			if state == "" {
				state = "open"
			}
			_, _ = fmt.Fprintf(w, `{"title":"feat: handler","body":"adds A","draft":false,"state":%q,"merged":%v,"html_url":"https://github.com/o/r/pull/7","user":{"login":"dev"},"head":{"ref":"feat","sha":%q},"base":{"ref":"main","sha":"base1"}}`, state, g.merged, g.head)
		case r.Method == "GET" && p == "/repos/o/r/pulls/7/files":
			b, _ := json.Marshal(appPatch)
			_, _ = fmt.Fprintf(w, `[{"filename":"app.go","status":"modified","patch":%s,"changes":4},{"filename":"go.sum","status":"modified","patch":"@@ -1 +1 @@\n-a\n+b","changes":2}]`, b)
		case r.Method == "GET" && strings.HasPrefix(p, "/repos/o/r/contents/"):
			if strings.HasSuffix(p, ".codereview.yaml") {
				if r.URL.Query().Get("ref") != "base1" {
					t.Errorf("repo config must be read from the base commit, got ref=%s", r.URL.Query().Get("ref"))
				}
				_, _ = io.WriteString(w, "profile: assertive\ninstructions: Never panic in library code.\n")
				return
			}
			if strings.HasSuffix(p, "/AGENTS.md") && !strings.Contains(p, "/contents/app/") {
				if r.URL.Query().Get("ref") != "base1" {
					t.Errorf("guidelines must come from the base commit, got ref=%s", r.URL.Query().Get("ref"))
				}
				_, _ = io.WriteString(w, "Library code must never panic; return errors.")
				return
			}
			if !strings.HasSuffix(p, "/app.go") {
				w.WriteHeader(404)
				return
			}
			_, _ = io.WriteString(w, "package app\n\nfunc A() {\n\tpanic(\"todo\")\n}\n// trailing\n")
		case r.Method == "GET" && p == "/repos/o/r/issues/7/comments":
			if g.summary != "" {
				b, _ := json.Marshal(g.summary)
				_, _ = fmt.Fprintf(w, `[{"id":1,"body":"hello"},{"id":%d,"body":%s}]`, g.summaryID, b)
			} else {
				_, _ = io.WriteString(w, `[]`)
			}
		case r.Method == "GET" && p == "/repos/o/r/pulls/7/comments":
			var items []string
			for _, b := range g.inline {
				j, _ := json.Marshal(b)
				items = append(items, `{"id":5,"body":`+string(j)+`}`)
			}
			_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case r.Method == "GET" && strings.HasPrefix(p, "/repos/o/r/compare/"):
			var items []string
			for _, f := range g.compare {
				items = append(items, fmt.Sprintf(`{"filename":%q}`, f))
			}
			_, _ = io.WriteString(w, `{"files":[`+strings.Join(items, ",")+`]}`)
		case r.Method == "POST" && p == "/repos/o/r/pulls/7/reviews":
			if g.rejectBatch {
				w.WriteHeader(422)
				_, _ = io.WriteString(w, `{"message":"Unprocessable"}`)
				return
			}
			g.reviews = append(g.reviews, body)
			for _, c := range body["comments"].([]any) {
				g.inline = append(g.inline, c.(map[string]any)["body"].(string))
			}
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && p == "/repos/o/r/pulls/7/comments":
			if body["line"].(float64) == 6 {
				w.WriteHeader(422)
				return
			}
			g.singles = append(g.singles, body)
			g.inline = append(g.inline, body["body"].(string))
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && p == "/repos/o/r/issues/7/comments":
			g.issueNotes = append(g.issueNotes, body["body"].(string))
			g.summary, g.summaryID = body["body"].(string), 99
			_, _ = io.WriteString(w, `{"id":99}`)
		case r.Method == "PATCH" && p == fmt.Sprintf("/repos/o/r/issues/comments/%d", g.summaryID):
			g.patched = append(g.patched, body["body"].(string))
			g.summary = body["body"].(string)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && strings.HasPrefix(p, "/repos/o/r/statuses/"):
			body["sha"] = strings.TrimPrefix(p, "/repos/o/r/statuses/")
			g.statuses = append(g.statuses, body)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "PATCH" && p == "/repos/o/r/pulls/7":
			g.prPatches = append(g.prPatches, body)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	})
}

func newRunner(t *testing.T, llmURL string) *Runner {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = map[string]llm.ModelConfig{"fake": {Provider: "openai-compatible", Model: "m", BaseURL: llmURL, MaxRetries: -1}}
	cfg.Model = "fake"
	return &Runner{Cfg: cfg, Logger: quiet}
}

func TestGitHubEndToEnd(t *testing.T) {
	reviewJSON := `{"files":[{"path":"app.go","summary":"A now panics."}],"comments":[
		{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"panic in library code","body":"Return an error instead.","suggestion":"\treturn errors.New(\"todo\")","confidence":0.9},
		{"path":"app.go","start_line":6,"end_line":6,"severity":"nitpick","category":"style","title":"stray comment","body":"Remove it.","confidence":0.9}
	]}`
	llmSrv, prompts := fakeLLM(t, reviewJSON)
	gh := &fakeGitHub{head: "head1"}
	ghSrv := httptest.NewServer(gh.handler(t))
	defer ghSrv.Close()

	r := newRunner(t, llmSrv.URL)
	client := vcs.NewGitHub(ghSrv.URL, vcs.StaticToken("t"))
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}

	out, err := r.Review(context.Background(), client, ref, Options{Auto: true, Post: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.SkipReason != "" {
		t.Fatalf("skipped: %s", out.SkipReason)
	}
	// assertive profile from repo config keeps the nitpick.
	if out.Posted != 2 || len(gh.reviews) != 1 {
		t.Fatalf("posted=%d reviews=%d", out.Posted, len(gh.reviews))
	}
	if !strings.Contains((*prompts)[0], "Never panic in library code") && !strings.Contains((*prompts)[0], "app.go") {
		t.Errorf("prompt missing context")
	}
	if strings.Contains((*prompts)[0], "go.sum") {
		t.Errorf("lockfile sent to model")
	}
	if !strings.Contains((*prompts)[0], "Library code must never panic") {
		t.Errorf("AGENTS.md rules did not reach the model")
	}
	if got := out.Result.Guidelines; len(got) != 1 || got[0] != "AGENTS.md" {
		t.Errorf("guidelines used = %v", got)
	}
	first := gh.reviews[0]["comments"].([]any)[0].(map[string]any)
	if first["side"] != "RIGHT" || first["line"].(float64) != 4 || !strings.Contains(first["body"].(string), "```suggestion\n\treturn errors.New") {
		t.Errorf("inline comment wrong: %v", first)
	}
	if len(gh.issueNotes) != 1 || !strings.HasPrefix(gh.issueNotes[0], vcs.SummaryMarker) || !strings.Contains(gh.issueNotes[0], "reviewed_sha=head1") {
		t.Fatalf("summary not posted correctly: %v", gh.issueNotes)
	}
	// The summary block is appended to the description; the author's text stays.
	if len(gh.prPatches) != 1 {
		t.Fatalf("description not updated: %v", gh.prPatches)
	}
	desc, _ := gh.prPatches[0]["body"].(string)
	if !strings.HasPrefix(desc, "adds A\n\n<!-- codereview:description:start -->") || gh.prPatches[0]["title"] != nil {
		t.Errorf("description patch = %v (title must stay without the placeholder)", gh.prPatches[0])
	}

	// Same commit again: auto review skips.
	out, err = r.Review(context.Background(), client, ref, Options{Auto: true, Post: true})
	if err != nil || !strings.Contains(out.SkipReason, "already reviewed") {
		t.Fatalf("want already-reviewed skip, got %+v %v", out, err)
	}

	// New commit that does not touch app.go: incremental review skips the
	// model, keeps the old summary for app.go and edits the summary in place.
	gh.head = "head2"
	gh.compare = []string{"README.md"}
	n := len(*prompts)
	out, err = r.Review(context.Background(), client, ref, Options{Auto: true, Post: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(*prompts) != n {
		t.Errorf("incremental review with no relevant changes should not call the model for review")
	}
	if len(gh.patched) != 1 || !strings.Contains(gh.patched[0], "reviewed_sha=head2") || out.Result.FileSummaries["app.go"] != "A now panics." {
		t.Errorf("summary not updated in place: patched=%d summaries=%v", len(gh.patched), out.Result.FileSummaries)
	}

	// Full review: identical findings are recognised as already posted.
	gh.head = "head3"
	out, err = r.Review(context.Background(), client, ref, Options{Full: true, Post: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.AlreadyPosted != 2 || out.Posted != 0 || len(gh.reviews) != 1 {
		t.Errorf("duplicates should not be reposted: already=%d posted=%d reviews=%d", out.Result.AlreadyPosted, out.Posted, len(gh.reviews))
	}
}

func TestGitHubRejectedCommentsGoToSummary(t *testing.T) {
	reviewJSON := `{"files":[],"comments":[
		{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"one","body":"x","confidence":0.9},
		{"path":"app.go","start_line":6,"end_line":6,"severity":"major","category":"bug","title":"two","body":"y","confidence":0.9}
	]}`
	llmSrv, _ := fakeLLM(t, reviewJSON)
	gh := &fakeGitHub{head: "h", rejectBatch: true}
	ghSrv := httptest.NewServer(gh.handler(t))
	defer ghSrv.Close()
	r := newRunner(t, llmSrv.URL)
	client := vcs.NewGitHub(ghSrv.URL, vcs.StaticToken("t"))
	out, err := r.Review(context.Background(), client, vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}, Options{Post: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Posted != 1 || len(out.Rejected) != 1 || out.Rejected[0].Title != "two" {
		t.Fatalf("posted=%d rejected=%+v", out.Posted, out.Rejected)
	}
	if !strings.Contains(gh.summary, "Additional comments (1)") || !strings.Contains(gh.summary, "two") {
		t.Errorf("rejected comment should be listed in summary:\n%s", gh.summary)
	}
}

func TestLocalReview(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nfunc A() {}\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nfunc A() {\n\tpanic(\"todo\")\n}\n// trailing\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "new.go"), []byte("package app\n\nvar k = \"AKIA"+"ABCDEFGHIJKLMNOP\"\n"), 0o644)

	llmSrv, prompts := fakeLLM(t, `{"files":[{"path":"app.go","summary":"panics"}],"comments":[{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"panic","body":"no","confidence":0.9}]}`)
	r := newRunner(t, llmSrv.URL)
	client := &vcs.LocalClient{Dir: dir}
	out, err := r.Review(context.Background(), client, vcs.Ref{Platform: vcs.Local, Host: "local", Repo: dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, f := range out.Result.Findings {
		titles = append(titles, f.Path+":"+f.Title)
	}
	got := strings.Join(titles, ",")
	if got != "new.go:Possible hard-coded AWS access key ID,app.go:panic" {
		t.Errorf("findings = %s", got)
	}
	if !strings.Contains((*prompts)[0], `path="new.go" status="added"`) {
		t.Errorf("untracked file not reviewed")
	}
}

func TestVerifyModelResolution(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1")
	if c, err := r.VerifyLLM(); err != nil || c != nil {
		t.Fatalf("unset verify_model should use the review model: %v %v", c, err)
	}
	r.Cfg.Review.VerifyModel = "fake" // a profile
	c, err := r.VerifyLLM()
	if err != nil || c == nil || c.Name() != "openai-compatible:m" {
		t.Fatalf("profile: %v %v", c, err)
	}
	r.VerifyModelOverride = "ollama:qwen:7b" // a spec, overriding the config
	if c, err := r.VerifyLLM(); err != nil || c.Name() != "ollama:qwen:7b" {
		t.Fatalf("override: %v %v", c, err)
	}
	r.VerifyModelOverride = "no-such-profile"
	if _, err := r.VerifyLLM(); err == nil || !strings.Contains(err.Error(), "verify_model") {
		t.Errorf("bad ref should fail clearly: %v", err)
	}
}

func TestCommitStatusLifecycle(t *testing.T) {
	llmSrv, _ := fakeLLM(t, `{"files":[],"comments":[{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"panic","body":"x","confidence":0.9}]}`)
	gh := &fakeGitHub{head: "head1"}
	ghSrv := httptest.NewServer(gh.handler(t))
	defer ghSrv.Close()
	client := vcs.NewGitHub(ghSrv.URL, vcs.StaticToken("t"))
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}

	r := newRunner(t, llmSrv.URL)
	if _, err := r.Review(context.Background(), client, ref, Options{Post: true, CommitStatus: true}); err != nil {
		t.Fatal(err)
	}
	if len(gh.statuses) != 2 || gh.statuses[0]["state"] != "pending" || gh.statuses[1]["state"] != "success" || gh.statuses[1]["sha"] != "head1" {
		t.Fatalf("statuses = %v", gh.statuses)
	}
	if d, _ := gh.statuses[1]["description"].(string); !strings.HasPrefix(d, "Reviewed: 1 comment · Quality") {
		t.Errorf("success description = %q", d)
	}

	// A failing review (model unreachable) on a fresh PR must not leave the
	// commit "running".
	gh2 := &fakeGitHub{head: "head9"}
	ghSrv2 := httptest.NewServer(gh2.handler(t))
	defer ghSrv2.Close()
	broken := newRunner(t, "http://127.0.0.1:1")
	if _, err := broken.Review(context.Background(), vcs.NewGitHub(ghSrv2.URL, vcs.StaticToken("t")), ref, Options{Post: true, CommitStatus: true}); err == nil {
		t.Fatal("expected the review to fail")
	}
	if len(gh2.statuses) != 2 || gh2.statuses[0]["state"] != "pending" || gh2.statuses[1]["state"] != "failure" {
		t.Errorf("failed review statuses = %v", gh2.statuses)
	}
}

func TestBitbucketEndToEnd(t *testing.T) {
	llmSrv, _ := fakeLLM(t, `{"files":[{"path":"app.go","summary":"A panics."}],"comments":[{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"panic in library code","body":"Return an error.","suggestion":"\treturn errors.New(\"todo\")","confidence":0.9}]}`)
	var mu sync.Mutex
	var comments, puts, statuses []map[string]any
	bb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7":
			_, _ = io.WriteString(w, `{"title":"Add A","description":"adds A","source":{"branch":{"name":"feat"},"commit":{"hash":"1111111111111111111111111111111111111111"}},"destination":{"branch":{"name":"main"},"commit":{"hash":"2222222222222222222222222222222222222222"}},"author":{"nickname":"dev"},"links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/7"}}}`)
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7/diff":
			_, _ = io.WriteString(w, "diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n"+appPatch)
		case r.Method == "GET" && strings.HasPrefix(p, "/repositories/acme/web/src/"):
			if strings.HasSuffix(p, "/app.go") {
				_, _ = io.WriteString(w, "package app\n\nfunc A() {\n\tpanic(\"todo\")\n}\n// trailing\n")
				return
			}
			w.WriteHeader(404)
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7/comments":
			_, _ = io.WriteString(w, `{"values":[]}`)
		case r.Method == "POST" && p == "/repositories/acme/web/pullrequests/7/comments":
			comments = append(comments, body)
			_, _ = io.WriteString(w, `{"id":1}`)
		case r.Method == "PUT" && p == "/repositories/acme/web/pullrequests/7":
			puts = append(puts, body)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && strings.HasSuffix(p, "/statuses/build"):
			statuses = append(statuses, body)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer bb.Close()

	r := newRunner(t, llmSrv.URL)
	client := vcs.NewBitbucket(bb.URL, vcs.BitbucketAuth{Token: "t"})
	ref := vcs.Ref{Platform: vcs.Bitbucket, Host: "bitbucket.org", Repo: "acme/web", Number: 7}
	out, err := r.Review(context.Background(), client, ref, Options{Post: true, CommitStatus: true, ScoreLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Posted != 1 || len(comments) != 2 {
		t.Fatalf("posted=%d comments=%d", out.Posted, len(comments))
	}
	inline := comments[0]
	raw := inline["content"].(map[string]any)["raw"].(string)
	if inline["inline"].(map[string]any)["path"] != "app.go" || !strings.Contains(raw, "**Suggested change:**") || strings.Contains(raw, "```suggestion") {
		t.Errorf("inline comment = %v", inline)
	}
	summary := comments[1]["content"].(map[string]any)["raw"].(string)
	if !strings.HasPrefix(summary, "[//]: # (codereview:summary)") || strings.Contains(summary, "<details>") {
		t.Errorf("summary not converted for Bitbucket:\n%s", summary)
	}
	if len(puts) != 1 || puts[0]["title"] != "Add A" || !strings.Contains(puts[0]["description"].(string), "[//]: # (codereview:description:start)") {
		t.Errorf("description update = %v", puts)
	}
	if len(statuses) != 2 || statuses[0]["state"] != "INPROGRESS" || statuses[1]["state"] != "SUCCESSFUL" {
		t.Errorf("build statuses = %v", statuses)
	}
}

func TestClosedPullRequests(t *testing.T) {
	llmSrv, prompts := fakeLLM(t, `{"files":[],"comments":[]}`)
	for _, c := range []struct {
		state  string
		merged bool
		want   string
	}{{"closed", false, "pull request is closed"}, {"closed", true, "pull request is merged"}} {
		gh := &fakeGitHub{head: "head1", state: c.state, merged: c.merged}
		srv := httptest.NewServer(gh.handler(t))
		r := newRunner(t, llmSrv.URL)
		client := vcs.NewGitHub(srv.URL, vcs.StaticToken("t"))
		ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}
		out, err := r.Review(context.Background(), client, ref, Options{Auto: true, Post: true})
		srv.Close()
		if err != nil || out.SkipReason != c.want {
			t.Errorf("%s/merged=%v: skip=%q err=%v", c.state, c.merged, out.SkipReason, err)
		}
		if len(gh.reviews)+len(gh.issueNotes) != 0 || len(gh.prPatches) != 0 {
			t.Errorf("nothing may be posted on a %s PR", c.want)
		}
	}
	if len(*prompts) != 0 {
		t.Errorf("no model calls for closed PRs, got %d", len(*prompts))
	}
}

func TestNoDescriptionBlockWhenNothingReviewed(t *testing.T) {
	gh := &fakeGitHub{head: "head1"}
	srv := httptest.NewServer(gh.handler(t))
	defer srv.Close()
	r := newRunner(t, "http://127.0.0.1:1")
	client := vcs.NewGitHub(srv.URL, vcs.StaticToken("t"))
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}
	r.updatePR(context.Background(), client, ref, &review.Result{TooLarge: true, Summary: "too many files"}, "@codereview summary", "@codereview", true)
	r.updatePR(context.Background(), client, ref, &review.Result{Summary: "nothing new"}, "@codereview summary", "@codereview", true)
	if len(gh.prPatches) != 0 {
		t.Errorf("the description must not change when nothing was reviewed: %v", gh.prPatches)
	}
}
