package vcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNeutraliseQuickActions(t *testing.T) {
	in := strings.Join([]string{
		"The token check can be bypassed.",
		"/approve",
		"  /merge",
		"see /etc/hosts in the middle of a line",
		"```suggestion:-0+0",
		"/* kept: code in a closed block */",
		"```",
		"```",
		"/close (an unclosed block is not code to GitLab)",
	}, "\n")
	out := NeutraliseQuickActions(in)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "/") && !strings.HasPrefix(l, "/* kept") {
			t.Errorf("a line still starts with /: %q", l)
		}
	}
	for _, want := range []string{`\/approve`, `  \/merge`, "/* kept: code in a closed block */", `\/close`, "see /etc/hosts"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if NeutraliseQuickActions("no slash here") != "no slash here" {
		t.Error("text without commands is unchanged")
	}
}

func TestNeutraliseMentions(t *testing.T) {
	in := "Ping @all and @alice-b, mail ops@example.com, run `@codereview review`.\n```\n@decorator\n```\n~~~\n@also_code\n~~~\n@bob"
	out := NeutraliseMentions(in)
	for _, want := range []string{"@" + zwsp + "all", "@" + zwsp + "alice-b", "ops@example.com", "`@codereview review`", "\n@decorator\n", "\n@also_code\n", "@" + zwsp + "bob"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

// Issue #19: a model-written finding body with /approve must not reach
// GitLab as a line starting with /.
func TestGitLabPostsNoQuickActions(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		for _, k := range []string{"body", "description"} {
			if v, ok := m[k].(string); ok {
				bodies = append(bodies, v)
			}
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := NewGitLab(srv.URL, "t", false)
	pr := &PR{Ref: Ref{Platform: GitLab, Repo: "g/p", Number: 7}, HeadSHA: "h"}
	evil := "Looks fine.\n/approve\n/merge\ncc @all"
	ctx := context.Background()
	if _, err := c.PostInline(ctx, pr, "", []Comment{{Path: "a.go", Line: 3, Body: evil}}); err != nil {
		t.Fatal(err)
	}
	_ = c.PostComment(ctx, pr, evil)
	_ = c.UpsertSummary(ctx, pr, &BotState{SummaryID: "9"}, evil)
	desc := "Author text\n/label ~x\n" + evil
	_ = c.UpdatePR(ctx, pr, nil, &desc)
	if len(bodies) != 4 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	for _, b := range bodies {
		for _, l := range strings.Split(b, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "/") {
				t.Errorf("posted a line starting with /: %q in %q", l, b)
			}
		}
	}
	if strings.Contains(bodies[0], "@all") || !strings.Contains(bodies[3], "Author text") {
		t.Errorf("mentions in comments, author text in the description: %q / %q", bodies[0], bodies[3])
	}
}
