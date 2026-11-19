package review

import (
	"strings"
	"testing"
)

func TestDescriptionBlockAndApply(t *testing.T) {
	res := &Result{Summary: "s", Highlights: []Highlight{{Type: "Bug Fixes", Items: []string{"Fix role 8 routing"}}}}
	block := DescriptionBlock(res, "codereview")
	if !strings.HasPrefix(block, DescriptionStart) || !strings.Contains(block, "## Summary by codereview") || !strings.Contains(block, "  * Fix role 8 routing") {
		t.Fatalf("block:\n%s", block)
	}

	// Appended below the author's text, which is kept verbatim.
	got, changed := ApplyDescription("Author text.\n", block, "@codereview summary", true)
	if !changed || !strings.HasPrefix(got, "Author text.\n\n"+DescriptionStart) {
		t.Errorf("append:\n%s", got)
	}
	// A second run replaces only the block; identical content means no update.
	again, changed := ApplyDescription(got, block, "@codereview summary", true)
	if changed || again != got {
		t.Error("re-applying the same block must be a no-op")
	}
	newBlock := DescriptionBlock(&Result{Highlights: []Highlight{{Type: "Refactor", Items: []string{"x"}}}}, "codereview")
	replaced, changed := ApplyDescription(got+"\nLater author note.", newBlock, "", true)
	if !changed || strings.Contains(replaced, "role 8") || !strings.Contains(replaced, "Later author note.") || strings.Count(replaced, DescriptionStart) != 1 {
		t.Errorf("replace:\n%s", replaced)
	}
	// Placeholder pins the position, even with auto-append off.
	placed, changed := ApplyDescription("Intro\n@codereview summary\nOutro", block, "@codereview summary", false)
	if !changed || !strings.HasPrefix(placed, "Intro\n"+DescriptionStart) || !strings.HasSuffix(placed, "\nOutro") {
		t.Errorf("placeholder:\n%s", placed)
	}
	// Auto-append off and no placeholder: untouched.
	if _, changed := ApplyDescription("Author text", block, "@codereview summary", false); changed {
		t.Error("high_level_summary: false must not append")
	}
	if StripDescriptionBlock(got) != "Author text." {
		t.Errorf("strip = %q", StripDescriptionBlock(got))
	}
}

func TestApplyTitle(t *testing.T) {
	if got, ok := ApplyTitle("WIP @CodeReview", "fix(phone): route role 8 to DTA Admin", "@codereview"); !ok || got != "fix(phone): route role 8 to DTA Admin" {
		t.Errorf("placeholder title: %q %v", got, ok)
	}
	if _, ok := ApplyTitle("Make DD approval final", "fix: x", "@codereview"); ok {
		t.Error("titles without the placeholder are never changed")
	}
}
