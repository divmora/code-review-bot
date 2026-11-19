package review

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
)

// Guideline is a repository guideline file such as AGENTS.md.
type Guideline struct {
	Path    string `json:"path"`
	Dir     string `json:"dir"` // "" for the repository root
	Content string `json:"-"`
}

// DefaultGuidelineFiles are read from the repository root. AGENTS.md is
// also looked up in each changed file's folders (nearest wins).
var DefaultGuidelineFiles = []string{"AGENTS.md", "CLAUDE.md", ".github/copilot-instructions.md", ".cursorrules"}

const (
	maxGuidelineBytes      = 12 << 10 // per file
	maxGuidelineTotalBytes = 24 << 10 // all files (~6k tokens)
)

// LoadGuidelines fetches root guideline files and nested AGENTS.md files
// for the folders of the changed paths. fetch returns the file at the
// target branch; errors (missing files) are skipped. fetch is called
// concurrently and must be safe for that.
func LoadGuidelines(ctx context.Context, files []string, changed []string, fetch func(ctx context.Context, p string) ([]byte, error)) []Guideline {
	type want struct{ path, dir string }
	var wants []want
	seen := map[string]bool{}
	add := func(p, dir string) {
		if !seen[p] {
			seen[p] = true
			wants = append(wants, want{p, dir})
		}
	}
	for _, f := range files {
		add(strings.TrimPrefix(f, "/"), "")
	}
	// Nested AGENTS.md: every ancestor folder of a changed file.
	dirs := map[string]bool{}
	for _, c := range changed {
		for d := path.Dir(c); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	sorted := make([]string, 0, len(dirs))
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	if len(sorted) > 60 { // keep API calls bounded on huge PRs
		sorted = sorted[:60]
	}
	for _, d := range sorted {
		add(d+"/AGENTS.md", d)
	}

	out := make([]*Guideline, len(wants))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, w := range wants {
		wg.Add(1)
		go func(i int, w want) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := fetch(ctx, w.path)
			if err != nil || len(data) == 0 {
				return
			}
			s := string(data)
			if len(s) > maxGuidelineBytes {
				s = s[:maxGuidelineBytes] + "\n[truncated]"
			}
			out[i] = &Guideline{Path: w.path, Dir: w.dir, Content: strings.TrimSpace(s)}
		}(i, w)
	}
	wg.Wait()
	var res []Guideline
	total := 0
	for _, g := range out {
		if g == nil || g.Content == "" {
			continue
		}
		if total+len(g.Content) > maxGuidelineTotalBytes {
			break
		}
		total += len(g.Content)
		res = append(res, *g)
	}
	return res
}

// rootGuidelines renders root guideline files for the system prompt.
func rootGuidelines(gs []Guideline) string {
	var b strings.Builder
	for _, g := range gs {
		if g.Dir != "" {
			continue
		}
		b.WriteString("<repository_guidelines source=\"" + g.Path + "\">\n" + g.Content + "\n</repository_guidelines>\n")
	}
	return b.String()
}

// nestedFor returns nested AGENTS.md content that applies to a file,
// nearest folder first.
func nestedFor(gs []Guideline, file string) []string {
	var hits []Guideline
	for _, g := range gs {
		if g.Dir != "" && strings.HasPrefix(file, g.Dir+"/") {
			hits = append(hits, g)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return len(hits[i].Dir) > len(hits[j].Dir) })
	out := make([]string, len(hits))
	for i, g := range hits {
		out[i] = "Guidelines from " + g.Path + ":\n" + g.Content
	}
	return out
}

// ConfigChanges returns changed paths that are review configuration or
// guideline files: the repo config file, the root guideline files, or any
// nested AGENTS.md.
func ConfigChanges(changed []string, repoConfigFile string, guidelineFiles []string) []string {
	special := map[string]bool{strings.TrimPrefix(repoConfigFile, "/"): true}
	for _, f := range guidelineFiles {
		special[strings.TrimPrefix(f, "/")] = true
	}
	var out []string
	for _, p := range changed {
		if special[p] || path.Base(p) == "AGENTS.md" {
			out = append(out, p)
		}
	}
	return out
}
