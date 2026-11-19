package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestParseBitbucketURL(t *testing.T) {
	ref, err := ParseURL("https://bitbucket.org/acme/web-app/pull-requests/42/diff")
	if err != nil || ref.Platform != Bitbucket || ref.Repo != "acme/web-app" || ref.Number != 42 || ref.Host != "bitbucket.org" {
		t.Fatalf("ref = %+v %v", ref, err)
	}
	// A self-hosted path that merely looks similar is not Bitbucket Cloud.
	if ref, err := ParseURL("https://git.example.com/acme/web-app/pull-requests/42"); err == nil && ref.Platform == Bitbucket {
		t.Error("only bitbucket.org is Bitbucket Cloud")
	}
}

func TestBitbucketMarkdownRoundTrip(t *testing.T) {
	body := SummaryMarker + "\n## Walkthrough\n\nText\n\n<details>\n<summary>📋 Review details</summary>\n\n- a\n</details>\n\n---\n<sub>Reviewed</sub>\n\n" +
		ReviewedSHATag("abc123") + "\n" + StateTag([]byte(`{"file_summaries":{}}`)) + "\n"
	bb := toBitbucketMarkdown(body)
	if strings.Contains(bb, "<!--") || strings.Contains(bb, "<details>") || strings.Contains(bb, "<sub>") {
		t.Fatalf("HTML left for Bitbucket:\n%s", bb)
	}
	if !strings.HasPrefix(bb, "[//]: # (codereview:summary)") || !strings.Contains(bb, "**📋 Review details**") {
		t.Errorf("converted:\n%s", bb)
	}
	back := fromBitbucketMarkdown(bb)
	if !isSummary(back) || reviewedSHA(back) != "abc123" {
		t.Errorf("markers not restored:\n%s", back)
	}
	st := &BotState{}
	parseSummary("1", back, st)
	if string(st.State) != `{"file_summaries":{}}` {
		t.Errorf("state = %q", st.State)
	}
}

type fakeBB struct {
	mu       sync.Mutex
	posted   []map[string]any
	puts     map[string]map[string]any
	statuses []map[string]any
	auth     []string
}

func (f *fakeBB) server(t *testing.T) *httptest.Server {
	f.puts = map[string]map[string]any{}
	summary := toBitbucketMarkdown(SummaryMarker + "\nold\n" + ReviewedSHATag("aaaa1111"))
	inline := toBitbucketMarkdown("_⚠️ Potential issue_ | _🟠 Major_\n\n**Enforce the credit-limit rule**\n\n" + FingerprintTag("abc1"))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		p := strings.TrimPrefix(r.URL.Path, "/2.0")
		switch {
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7":
			_, _ = io.WriteString(w, `{"title":"Add A","description":"Author text","draft":false,"source":{"branch":{"name":"feat"},"commit":{"hash":"1111111"}},"destination":{"branch":{"name":"main"},"commit":{"hash":"2222222"}},"author":{"nickname":"dev"},"links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/7"}}}`)
		case r.Method == "GET" && p == "/repositories/acme/web/commit/1111111":
			_, _ = io.WriteString(w, `{"hash":"1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		case r.Method == "GET" && p == "/repositories/acme/web/commit/2222222":
			_, _ = io.WriteString(w, `{"hash":"2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7/diff":
			_, _ = io.WriteString(w, "diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n@@ -1,3 +1,5 @@\n package app\n \n-func A() {}\n+func A() {\n+\tpanic(\"todo\")\n+}\n")
		case r.Method == "GET" && strings.HasPrefix(p, "/repositories/acme/web/src/"):
			if strings.HasSuffix(p, "/app.go") {
				_, _ = io.WriteString(w, "package app\n\nfunc A() {\n\tpanic(\"todo\")\n}\n")
				return
			}
			w.WriteHeader(404)
		case r.Method == "GET" && p == "/repositories/acme/web/pullrequests/7/comments":
			if r.URL.Query().Get("page") == "2" {
				ib, _ := json.Marshal(inline)
				_, _ = io.WriteString(w, `{"values":[{"id":6,"content":{"raw":`+string(ib)+`},"inline":{"path":"app.go","to":4}}]}`)
				return
			}
			sb, _ := json.Marshal(summary)
			next := "http://" + r.Host + "/2.0/repositories/acme/web/pullrequests/7/comments?page=2"
			_, _ = io.WriteString(w, `{"values":[{"id":5,"content":{"raw":`+string(sb)+`}},{"id":9,"deleted":true,"content":{"raw":"x"}}],"next":"`+next+`"}`)
		case r.Method == "GET" && strings.HasPrefix(p, "/repositories/acme/web/diffstat/"):
			_, _ = io.WriteString(w, `{"values":[{"new":{"path":"app.go"},"old":{"path":"app.go"}},{"new":null,"old":{"path":"gone.go"}}]}`)
		case r.Method == "POST" && p == "/repositories/acme/web/pullrequests/7/comments":
			f.posted = append(f.posted, body)
			_, _ = io.WriteString(w, `{"id":10}`)
		case r.Method == "PUT":
			f.puts[p] = body
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && strings.HasSuffix(p, "/statuses/build"):
			body["path"] = p
			f.statuses = append(f.statuses, body)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
}

func TestBitbucketClient(t *testing.T) {
	f := &fakeBB{}
	srv := f.server(t)
	defer srv.Close()
	c := NewBitbucket(srv.URL+"/2.0", BitbucketAuth{Token: "tok"})
	ctx := context.Background()
	ref := Ref{Platform: Bitbucket, Host: "bitbucket.org", Repo: "acme/web", Number: 7}

	pr, err := c.GetPR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.HeadSHA) != 40 || len(pr.BaseSHA) != 40 || pr.BaseBranch != "main" || pr.Author != "dev" || pr.Body != "Author text" {
		t.Fatalf("pr = %+v", pr)
	}
	files, err := c.Files(ctx, pr)
	if err != nil || len(files) != 1 || files[0].Path != "app.go" || files[0].AddedCount() != 3 {
		t.Fatalf("files = %+v %v", files, err)
	}
	if _, err := c.FileContent(ctx, pr, ".codereview.yaml", pr.BaseSHA); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	st, err := c.BotState(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	if st.SummaryID != "5" || st.ReviewedSHA != "aaaa1111" || !st.Fingerprints["abc1"] || len(st.Comments) != 1 || st.Comments[0].Title != "Enforce the credit-limit rule" {
		t.Errorf("bot state = %+v", st)
	}
	paths, err := c.ChangedPaths(ctx, pr, "aaaa", pr.HeadSHA)
	if err != nil || strings.Join(paths, ",") != "app.go,gone.go" {
		t.Errorf("changed paths = %v %v", paths, err)
	}

	if _, err := c.PostInline(ctx, pr, "", []Comment{{Path: "app.go", Line: 4, Body: "**Bug**\n\n" + FingerprintTag("abc2")}}); err != nil {
		t.Fatal(err)
	}
	inl := f.posted[0]["inline"].(map[string]any)
	raw := f.posted[0]["content"].(map[string]any)["raw"].(string)
	if inl["path"] != "app.go" || inl["to"].(float64) != 4 || strings.Contains(raw, "<!--") || !strings.Contains(raw, "[//]: # (codereview:fp=abc2)") {
		t.Errorf("inline post = %v", f.posted[0])
	}
	if err := c.UpsertSummary(ctx, pr, st, SummaryMarker+"\nnew"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.puts["/repositories/acme/web/pullrequests/7/comments/5"]; !ok {
		t.Errorf("summary should be edited in place: %v", f.puts)
	}
	desc := "Author text\n\n<!-- codereview:description:start -->\n## Summary\n<!-- codereview:description:end -->"
	if err := c.UpdatePR(ctx, pr, nil, &desc); err != nil {
		t.Fatal(err)
	}
	put := f.puts["/repositories/acme/web/pullrequests/7"]
	if put["title"] != "Add A" || strings.Contains(put["description"].(string), "<!--") {
		t.Errorf("PR update = %v", put)
	}
	if err := c.SetCommitStatus(ctx, pr, pr.HeadSHA, CommitStatus{State: StatusSuccess, Description: "Reviewed"}); err != nil {
		t.Fatal(err)
	}
	s := f.statuses[0]
	if s["state"] != "SUCCESSFUL" || s["key"] != "codereview" || s["url"] != "https://bitbucket.org/acme/web/pull-requests/7" || !strings.Contains(s["path"].(string), pr.HeadSHA) {
		t.Errorf("status = %v", s)
	}
	if err := c.SetScoreLabels(ctx, pr, nil); err != nil {
		t.Error("labels are a no-op on Bitbucket")
	}
	for _, a := range f.auth {
		if a != "Bearer tok" {
			t.Fatalf("auth header = %q", a)
		}
	}

	// Basic auth with an Atlassian API token.
	f2 := &fakeBB{}
	srv2 := f2.server(t)
	defer srv2.Close()
	if _, err := NewBitbucket(srv2.URL+"/2.0", BitbucketAuth{Email: "a@x.com", APIToken: "t"}).GetPR(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f2.auth[0], "Basic ") {
		t.Errorf("basic auth = %q", f2.auth[0])
	}
	if _, err := NewBitbucket(srv2.URL+"/2.0", BitbucketAuth{}).GetPR(ctx, ref); err == nil || !strings.Contains(err.Error(), "BITBUCKET_TOKEN") {
		t.Errorf("missing credentials should explain: %v", err)
	}
}
