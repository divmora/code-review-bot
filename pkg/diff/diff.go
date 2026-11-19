// Package diff parses unified diffs and tracks which new-side lines can carry
// review comments.
package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Status is the change type of a file.
type Status string

const (
	Added    Status = "added"
	Modified Status = "modified"
	Deleted  Status = "deleted"
	Renamed  Status = "renamed"
)

// LineKind marks a line in a hunk.
type LineKind byte

const (
	Context LineKind = ' '
	Add     LineKind = '+'
	Del     LineKind = '-'
)

// Line is one line of a hunk. OldNo is 0 for added lines, NewNo is 0 for
// deleted lines.
type Line struct {
	Kind  LineKind
	OldNo int
	NewNo int
	Text  string
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Section            string
	Lines              []Line
}

// File is one changed file.
type File struct {
	OldPath string
	Path    string // new path; equals OldPath unless renamed
	Status  Status
	Binary  bool
	// Truncated is set when the platform withheld the patch (too large).
	Truncated bool
	Hunks     []Hunk
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// ParsePatch parses the hunks of a single file's patch (the part starting
// at the first "@@" line, as returned by the GitHub and GitLab APIs).
func ParsePatch(patch string) ([]Hunk, error) {
	var hunks []Hunk
	var cur *Hunk
	oldNo, newNo := 0, 0
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		if strings.HasPrefix(raw, "@@") {
			m := hunkHeader.FindStringSubmatch(raw)
			if m == nil {
				return nil, fmt.Errorf("line %d: bad hunk header %q", i+1, raw)
			}
			h := Hunk{
				OldStart: atoi(m[1], 0), OldLines: atoi(m[2], 1),
				NewStart: atoi(m[3], 0), NewLines: atoi(m[4], 1),
				Section: strings.TrimSpace(m[5]),
			}
			hunks = append(hunks, h)
			cur = &hunks[len(hunks)-1]
			oldNo, newNo = h.OldStart, h.NewStart
			continue
		}
		if cur == nil {
			continue // file headers before the first hunk
		}
		if raw == "" {
			// A trailing empty string from Split, or a context line whose
			// leading space was stripped by an editor.
			if i == len(lines)-1 {
				continue
			}
			raw = " "
		}
		switch raw[0] {
		case '+':
			cur.Lines = append(cur.Lines, Line{Kind: Add, NewNo: newNo, Text: raw[1:]})
			newNo++
		case '-':
			cur.Lines = append(cur.Lines, Line{Kind: Del, OldNo: oldNo, Text: raw[1:]})
			oldNo++
		case ' ':
			cur.Lines = append(cur.Lines, Line{Kind: Context, OldNo: oldNo, NewNo: newNo, Text: raw[1:]})
			oldNo++
			newNo++
		case '\\':
			// "\ No newline at end of file"
		default:
			// Lines after the hunk body (e.g. next "diff --git"); stop this hunk.
			cur = nil
		}
	}
	return hunks, nil
}

// ParseMulti parses a full `git diff` output containing many files.
func ParseMulti(text string) ([]File, error) {
	var files []File
	text = strings.ReplaceAll(text, "\r\n", "\n")
	chunks := splitKeep(text, "diff --git ")
	for _, c := range chunks {
		if !strings.HasPrefix(c, "diff --git ") {
			continue
		}
		f, err := parseGitFile(c)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

func parseGitFile(chunk string) (File, error) {
	var f File
	f.Status = Modified
	header, body, _ := strings.Cut(chunk, "\n@@")
	for _, l := range strings.Split(header, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			a, b := splitGitPaths(strings.TrimPrefix(l, "diff --git "))
			f.OldPath, f.Path = a, b
		case strings.HasPrefix(l, "new file mode"):
			f.Status = Added
		case strings.HasPrefix(l, "deleted file mode"):
			f.Status = Deleted
		case strings.HasPrefix(l, "rename from "):
			f.OldPath = strings.TrimPrefix(l, "rename from ")
			f.Status = Renamed
		case strings.HasPrefix(l, "rename to "):
			f.Path = strings.TrimPrefix(l, "rename to ")
			f.Status = Renamed
		case strings.HasPrefix(l, "Binary files ") || strings.HasPrefix(l, "GIT binary patch"):
			f.Binary = true
		case strings.HasPrefix(l, "--- "):
			if p := trimSide(strings.TrimPrefix(l, "--- ")); p != "" {
				f.OldPath = p
			}
		case strings.HasPrefix(l, "+++ "):
			if p := trimSide(strings.TrimPrefix(l, "+++ ")); p != "" {
				f.Path = p
			}
		}
	}
	if f.Status == Deleted {
		f.Path = f.OldPath
	}
	if body != "" || strings.Contains(chunk, "\n@@") {
		hunks, err := ParsePatch("@@" + body)
		if err != nil {
			return f, fmt.Errorf("%s: %w", f.Path, err)
		}
		f.Hunks = hunks
	}
	return f, nil
}

// splitGitPaths splits "a/x b/y" (no quoting support beyond the common case).
func splitGitPaths(s string) (string, string) {
	if i := strings.Index(s, " b/"); i >= 0 && strings.HasPrefix(s, "a/") {
		return s[2:i], s[i+3:]
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return s, s
}

func trimSide(p string) string {
	p = strings.TrimSpace(p)
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// splitKeep splits s before every line that starts with sep.
func splitKeep(s, sep string) []string {
	var out []string
	parts := strings.Split(s, "\n"+sep)
	for i, p := range parts {
		if i > 0 {
			p = sep + p
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// AddedLines returns the new-side line numbers that were added.
func (f *File) AddedLines() map[int]bool {
	out := map[int]bool{}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == Add {
				out[l.NewNo] = true
			}
		}
	}
	return out
}

// AddedCount and DeletedCount report change size.
func (f *File) AddedCount() int   { return f.count(Add) }
func (f *File) DeletedCount() int { return f.count(Del) }

func (f *File) count(k LineKind) int {
	n := 0
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == k {
				n++
			}
		}
	}
	return n
}

// HunkFor returns the hunk whose new-side range contains line, or nil.
func (f *File) HunkFor(line int) *Hunk {
	for i := range f.Hunks {
		h := &f.Hunks[i]
		if line >= h.NewStart && line < h.NewStart+max(h.NewLines, 1) {
			return h
		}
	}
	return nil
}

// LineAt returns the new-side line in a hunk, or nil if it is not visible.
func (h *Hunk) LineAt(newNo int) *Line {
	for i := range h.Lines {
		if h.Lines[i].Kind != Del && h.Lines[i].NewNo == newNo {
			return &h.Lines[i]
		}
	}
	return nil
}

// Annotated renders the file's hunks with new-side line numbers so a model
// can cite exact lines. Deleted lines carry no number.
//
//	@@ -10,3 +10,4 @@ func main()
//	  10   | ctx
//	  11 + | added
//	     - | removed
func (f *File) Annotated() string {
	var b strings.Builder
	for _, h := range f.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@ %s\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines, h.Section)
		for _, l := range h.Lines {
			switch l.Kind {
			case Add:
				fmt.Fprintf(&b, "%5d + | %s\n", l.NewNo, l.Text)
			case Del:
				fmt.Fprintf(&b, "      - | %s\n", l.Text)
			default:
				fmt.Fprintf(&b, "%5d   | %s\n", l.NewNo, l.Text)
			}
		}
	}
	return b.String()
}
