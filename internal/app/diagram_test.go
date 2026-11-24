package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/divmora/code-review-bot/internal/config"
	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// fakeLLMWithFlow is fakeLLM that also answers the flow diagram call.
func fakeLLMWithFlow(t *testing.T, reviewJSON, flowJSON string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sys := body.Messages[0].Content
		content := `{"summary":"Adds a handler.","cohorts":[{"label":"App","files":["app.go"],"summary":"New handler."}],"effort":2,"poem":"","scores":{"quality":80,"security":90,"maintainability":85}}`
		switch {
		case strings.Contains(sys, "senior software engineer doing a code review"):
			content = reviewJSON
		case strings.Contains(sys, "You draw the flow"):
			content = flowJSON
		}
		b, _ := json.Marshal(content)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`, b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDiagramOnly is "@bot diagram": it draws from the last review's state,
// replaces only the diagram in the summary, and respects the admin's off.
func TestDiagramOnly(t *testing.T) {
	flow := `{"kind":"sequence","participants":[{"id":"h","label":"Handler A","source":"app.go"},{"id":"db","label":"store","source":"external:db"}],
		"steps":[{"from":"h","to":"db","text":"save","changed":true}]}`
	llmSrv := fakeLLMWithFlow(t, `{"files":[{"path":"app.go","summary":"A now panics."}],"comments":[]}`, flow)
	gh := &fakeGitHub{head: "head1"}
	ghSrv := httptest.NewServer(gh.handler(t))
	defer ghSrv.Close()
	r := newRunner(t, llmSrv.URL)
	client := vcs.NewGitHub(ghSrv.URL, vcs.StaticToken("t"))
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r", Number: 7}

	if _, err := r.Diagram(context.Background(), client, ref, true, nil); err == nil || !strings.Contains(err.Error(), "review the pull request first") {
		t.Fatalf("before any review: %v", err)
	}
	if _, err := r.Review(context.Background(), client, ref, Options{Post: true}); err != nil {
		t.Fatal(err)
	}
	if len(gh.issueNotes) != 1 || strings.Contains(gh.issueNotes[0], "Changed flow") {
		t.Fatalf("the review (diagram off by default) has no diagram: %v", gh.issueNotes)
	}
	off := "off"
	if _, err := r.Diagram(context.Background(), client, ref, true, &config.Overrides{Diagram: &off}); !errors.Is(err, ErrDiagramOff) {
		t.Errorf("the admin's off: %v", err)
	}
	out, err := r.Diagram(context.Background(), client, ref, true, nil)
	if err != nil || out.Diagram == nil || out.Usage.InputTokens == 0 {
		t.Fatalf("diagram: %+v %v", out, err)
	}
	if len(gh.patched) != 1 {
		t.Fatalf("the summary is edited in place: %d", len(gh.patched))
	}
	body := gh.patched[0]
	if !strings.Contains(body, "<summary>🧭 Changed flow</summary>") || !strings.Contains(body, `participant P1 as "Handler A"`) || !strings.Contains(body, "## Walkthrough") ||
		!strings.Contains(body, "reviewed_sha=head1") {
		t.Errorf("summary after diagram-only:\n%s", body)
	}
	// The state carries the diagram, so the next incremental review can keep it.
	var st review.State
	m := regexp.MustCompile(`codereview:state=([A-Za-z0-9+/=]+) -->`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no state tag")
	}
	raw, _ := base64.StdEncoding.DecodeString(m[1])
	if json.Unmarshal(raw, &st) != nil || st.Diagram == nil || len(st.FileSummaries) == 0 {
		t.Errorf("state: %+v", st)
	}
	if strings.Count(body, "codereview:state=") != 1 {
		t.Error("one state tag")
	}
}
