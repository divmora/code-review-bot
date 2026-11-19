package diff

import (
	"strings"
	"testing"
)

const patch = `@@ -1,4 +1,5 @@ package main
 package main
-import "fmt"
+import (
+	"fmt"
+)

 func main() {
@@ -10,2 +11,3 @@ func main() {
 	a := 1
+	b := 2
 	_ = a
\ No newline at end of file`

func TestParsePatch(t *testing.T) {
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	f := File{Path: "main.go", Hunks: hunks}
	added := f.AddedLines()
	for _, n := range []int{2, 3, 4, 12} {
		if !added[n] {
			t.Errorf("line %d should be added; got %v", n, added)
		}
	}
	if len(added) != 4 {
		t.Errorf("want 4 added lines, got %d", len(added))
	}
	if f.DeletedCount() != 1 {
		t.Errorf("want 1 deleted, got %d", f.DeletedCount())
	}
	if h := f.HunkFor(13); h == nil || h.NewStart != 11 {
		t.Errorf("HunkFor(13) = %+v", h)
	}
	if h := f.HunkFor(9); h != nil {
		t.Errorf("line 9 is outside every hunk, got %+v", h)
	}
	if hunks[0].Section != "package main" {
		t.Errorf("section = %q", hunks[0].Section)
	}
	if !strings.Contains(f.Annotated(), "   12 + | \tb := 2") {
		t.Errorf("annotated output missing numbered line:\n%s", f.Annotated())
	}
}

const multi = `diff --git a/old.txt b/new.txt
similarity index 90%
rename from old.txt
rename to new.txt
--- a/old.txt
+++ b/new.txt
@@ -1 +1 @@
-hello
+hello world
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package gone
-
diff --git a/img.png b/img.png
new file mode 100644
Binary files /dev/null and b/img.png differ
diff --git a/added.go b/added.go
new file mode 100644
--- /dev/null
+++ b/added.go
@@ -0,0 +1,2 @@
+package added
+// diff --git inside content must not split
`

func TestParseMulti(t *testing.T) {
	files, err := ParseMulti(multi)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d: %+v", len(files), files)
	}
	if f := files[0]; f.Status != Renamed || f.OldPath != "old.txt" || f.Path != "new.txt" || f.AddedCount() != 1 {
		t.Errorf("rename parsed wrong: %+v", f)
	}
	if f := files[1]; f.Status != Deleted || f.Path != "gone.go" || f.DeletedCount() != 2 {
		t.Errorf("delete parsed wrong: %+v", f)
	}
	if f := files[2]; !f.Binary || f.Status != Added {
		t.Errorf("binary parsed wrong: %+v", f)
	}
	if f := files[3]; f.Status != Added || f.AddedCount() != 2 {
		t.Errorf("added parsed wrong: %+v", f)
	}
}
