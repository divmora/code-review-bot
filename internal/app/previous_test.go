package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/divmora/code-review-bot/internal/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

// prevClient serves only what previousFindings uses.
type prevClient struct {
	vcs.Client
	changed  []string
	files    map[string]string // path → content at head; missing: gone
	fetched  []string
	noCompar bool // a force-push dropped the old commit
}

func (c *prevClient) ChangedPaths(context.Context, *vcs.PR, string, string) ([]string, error) {
	if c.noCompar {
		return nil, vcs.ErrNotFound
	}
	return c.changed, nil
}

func (c *prevClient) FileContent(_ context.Context, _ *vcs.PR, path, _ string) ([]byte, error) {
	c.fetched = append(c.fetched, path)
	b, ok := c.files[path]
	if !ok {
		return nil, vcs.ErrNotFound
	}
	return []byte(b), nil
}

func TestPreviousFindingsChanged(t *testing.T) {
	keep := review.PrevFinding{Path: "auth.go", End: 2, Severity: review.Major, Category: "security", Title: "t1"}
	fixed := review.PrevFinding{Path: "retry.go", End: 2, Severity: review.Major, Category: "bug", Title: "t2"}
	untouched := review.PrevFinding{Path: "jobs.go", End: 2, Severity: review.Major, Category: "bug", Title: "t3"}
	gone := review.PrevFinding{Path: "old.go", End: 2, Severity: review.Minor, Category: "bug", Title: "t4"}
	keep.CodeHash, keep.CodeLines = review.HashCode("if token == want {"), 1
	fixed.CodeHash, fixed.CodeLines = review.HashCode("for {", "retry()"), 2
	untouched.CodeHash, untouched.CodeLines = review.HashCode("m[k] = v"), 1
	st, _ := json.Marshal(review.State{Findings: []review.PrevFinding{keep, fixed, untouched, gone}})
	state := &vcs.BotState{ReviewedSHA: "old", State: st}
	c := &prevClient{changed: []string{"auth.go", "retry.go", "old.go"}, files: map[string]string{
		"auth.go":  "package a\n\n// a comment added above\n    if token == want {\n",
		"retry.go": "for i := 0; i < 3; i++ {\n\tretry()\n}\n",
	}}
	r := &Runner{Logger: quiet}
	out := r.previousFindings(context.Background(), c, &vcs.PR{HeadSHA: "new"}, state)
	got := map[string]bool{}
	for _, p := range out {
		got[p.Path] = p.Changed
	}
	if len(out) != 4 || got["auth.go"] || !got["retry.go"] || got["jobs.go"] || !got["old.go"] {
		t.Errorf("changed = %v", got)
	}
	for _, p := range c.fetched {
		if p == "jobs.go" {
			t.Error("a file that didn't change isn't fetched")
		}
	}
	// Without the compare (force-push), every file with a finding is
	// checked; a file that didn't change keeps its code.
	c.noCompar, c.fetched = true, nil
	c.files["jobs.go"] = "m[k] = v\n"
	out = r.previousFindings(context.Background(), c, &vcs.PR{HeadSHA: "new"}, state)
	if out[2].Changed || len(c.fetched) != 4 {
		t.Errorf("force-push: %+v %v", out, c.fetched)
	}
	c.noCompar = false

	// Same head as the last review: nothing changed, nothing fetched.
	c.fetched = nil
	state.ReviewedSHA = "new"
	if out := r.previousFindings(context.Background(), c, &vcs.PR{HeadSHA: "new"}, state); len(out) != 4 || out[1].Changed || len(c.fetched) != 0 {
		t.Errorf("same head: %+v %v", out, c.fetched)
	}
	// Older summaries without findings: unknown.
	st, _ = json.Marshal(review.State{FileSummaries: map[string]string{}})
	if out := r.previousFindings(context.Background(), c, &vcs.PR{HeadSHA: "new"}, &vcs.BotState{ReviewedSHA: "old", State: st}); out != nil {
		t.Errorf("no findings kept: %+v", out)
	}
}
