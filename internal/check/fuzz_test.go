package check

import (
	"path"
	"strings"
	"testing"
)

func FuzzParseHunk(f *testing.F) {
	for _, seed := range []string{"@@ -1,2 +3,4 @@", "@@ -1 +1 @@", "@@ -1,2 +3,0 @@", "@@ -1 +x @@", "@@", "", "@@ -1 +-5,3 @@", "@@ -1 +9223372036854775807,9223372036854775807 @@"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		sp, ok := parseHunk(header)
		if ok && (sp.from < 1 || sp.to < sp.from) {
			t.Fatalf("parseHunk(%q) = %+v, want 1 <= from <= to", header, sp)
		}
	})
}

func FuzzParsePatch(f *testing.F) {
	for _, seed := range []string{
		"diff --git a/a.py b/a.py\n--- a/a.py\n+++ b/a.py\n@@ -1 +1,2 @@\n+x\n+y\n",
		"diff --git a/a b/a\n+++ /dev/null\n@@ -1 +0,0 @@\n",
		"+++ \"b/\\303\\251.py\"\n@@ -0,0 +1 @@\n",
		"@@ -1 +1 @@\n",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, patch string) {
		for file, spans := range parsePatch(patch) {
			if file == "" {
				t.Fatalf("parsePatch(%q) touched the empty path", patch)
			}
			for _, sp := range spans {
				if sp.from < 1 || sp.to < sp.from {
					t.Fatalf("parsePatch(%q) gave %s the span %+v, want 1 <= from <= to", patch, file, sp)
				}
			}
		}
	})
}

func FuzzParseEntry(f *testing.F) {
	for _, seed := range []string{"a.py:1", "a.py:3-9", "a.py:0", "a.py:9-3", "../a.py:1", "/a.py:1", "a:b.py:2", ":1", "a.py:", "a.py:1-", "a.py:-1", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		file, sp, err := parseEntry(line)
		if err != nil {
			return
		}
		if sp.from < 1 || sp.to < sp.from {
			t.Fatalf("parseEntry(%q) = %+v, want 1 <= from <= to", line, sp)
		}
		if file == "" || file != path.Clean(file) || path.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") {
			t.Fatalf("parseEntry(%q) accepted the path %q, which is not a clean path inside the repository", line, file)
		}
	})
}
