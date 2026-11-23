package diag

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

func FuzzEscapeControls(f *testing.F) {
	for _, seed := range []string{"", "plain", "a\nb", "\x1b[31mred\x1b[0m", "\x00\x7f\u0085", "tab\there", "\xff\xfe", "\u202e"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got := escapeControls(text)
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Fatalf("escapeControls(%q) = %q, which still has the control %U", text, got, r)
			}
		}
		if utf8.ValidString(text) && !utf8.ValidString(got) {
			t.Fatalf("escapeControls(%q) = %q, which is not valid UTF-8", text, got)
		}
		if again := escapeControls(got); again != got {
			t.Fatalf("escapeControls is not idempotent on %q: %q then %q", text, got, again)
		}
	})
}
