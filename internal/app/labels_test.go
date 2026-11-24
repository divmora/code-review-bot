package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// fakeLLMLabels is fakeLLM that also answers the label call.
func fakeLLMLabels(t *testing.T, labelsJSON *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sys := body.Messages[0].Content
		content := `{"summary":"Adds a handler.","cohorts":[{"label":"App","files":["app.go"],"summary":"New handler."}],"effort":2,"poem":"","scores":{"quality":80,"security":90,"maintainability":85}}`
		switch {
		case strings.Contains(sys, "senior software engineer doing a code review"):
			content = `{"files":[{"path":"app.go","summary":"A now panics."}],"comments":[]}`
		case strings.Contains(sys, "You pick labels"):
			content = *labelsJSON
		}
		b, _ := json.Marshal(content)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`, b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// labelHost adds GitHub's label endpoints to fakeGitHub.
type labelHost struct {
	mu      sync.Mutex
	project []string
	current []string
	created []string
}

func (l *labelHost) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		switch p := r.URL.Path; {
		case r.Method == "GET" && p == "/repos/o/r/labels":
			var out []map[string]string
			for _, n := range l.project {
				out = append(out, map[string]string{"name": n})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "GET" && p == "/repos/o/r/issues/7/labels":
			var out []map[string]string
			for _, n := range l.current {
				out = append(out, map[string]string{"name": n})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "POST" && p == "/repos/o/r/issues/7/labels":
			var body struct{ Labels []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, n := range body.Labels {
				found := false
				for _, x := range l.project {
					found = found || x == n
				}
				if !found {
					l.created = append(l.created, n) // GitHub would create it
				}
				l.current = append(l.current, n)
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "DELETE" && strings.HasPrefix(p, "/repos/o/r/issues/7/labels/"):
			name := strings.TrimPrefix(p, "/repos/o/r/issues/7/labels/")
			var keep []string
			for _, x := range l.current {
				if x != name {
					keep = append(keep, x)
				}
			}
			l.current = keep
			_, _ = w.Write([]byte(`[]`))
		default:
			l.mu.Unlock()
			next.ServeHTTP(w, r)
			l.mu.Lock()
		}
	})
}

// TestApplyLabels: apply mode sets only the project's allowed labels,
// keeps a person's, never creates one, removes only the bot's own on a new
// head, and a re-run on the same head changes nothing (issue #35).
func TestApplyLabels(t *testing.T) {
	labels := `{"labels":[{"name":"type::bug","reason":"panics on input"},{"name":"priority::high","reason":"crash"},{"name":"made-up","reason":"x"}]}`
	llmSrv := fakeLLMLabels(t, &labels)
	gh := &fakeGitHub{head: "head1"}
	lh := &labelHost{project: []string{"type::bug", "type::feature", "priority::high", "backend"}, current: []string{"backend"}}
	ghSrv := httptest.NewServer(lh.wrap(gh.handler(t)))
	defer ghSrv.Close()
	r := newRunner(t, llmSrv.URL)
	client := vcs.NewGitHub(ghSrv.URL, vcs.StaticToken("t"))
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}
	ov := &config.Overrides{Labels: &config.Labels{Mode: config.LabelsApply, Allow: []string{"type::*", "backend"}, SuggestOnly: []string{"priority::*"}}}

	out, err := r.Review(context.Background(), client, ref, Options{Post: true, Overrides: ov})
	if err != nil {
		t.Fatal(err)
	}
	cur := func() string {
		lh.mu.Lock()
		defer lh.mu.Unlock()
		c := append([]string{}, lh.current...)
		sort.Strings(c)
		return strings.Join(c, ",")
	}
	if cur() != "backend,type::bug" || len(lh.created) != 0 {
		t.Fatalf("labels %s, created %v (priority is suggest-only; made-up isn't a project label)", cur(), lh.created)
	}
	lr := out.Result.Labels
	if lr == nil || strings.Join(lr.Applied, ",") != "type::bug" {
		t.Errorf("result: %+v", lr)
	}
	if !strings.Contains(gh.issueNotes[0], "**Labels:** `type::bug` (panics on input)") || !strings.Contains(gh.issueNotes[0], "**Suggested labels:** `priority::high`") {
		t.Errorf("summary:\n%s", gh.issueNotes[0])
	}

	// Same head again: nothing changes, and no new choice is asked for.
	labels = `{"labels":[{"name":"type::feature","reason":"would flip"}]}`
	out, err = r.Review(context.Background(), client, ref, Options{Post: true, Full: true, Overrides: ov})
	if err != nil {
		t.Fatal(err)
	}
	if cur() != "backend,type::bug" || !out.Result.Labels.Reused {
		t.Errorf("same head: %s %+v", cur(), out.Result.Labels)
	}

	// A new head where the change is now a feature: the bot swaps its own
	// type::bug, and the person's backend stays.
	gh.head = "head2"
	out, err = r.Review(context.Background(), client, ref, Options{Post: true, Full: true, Overrides: ov})
	if err != nil {
		t.Fatal(err)
	}
	if cur() != "backend,type::feature" || strings.Join(out.Result.Labels.Removed, ",") != "type::bug" {
		t.Errorf("new head: %s %+v", cur(), out.Result.Labels)
	}
}
