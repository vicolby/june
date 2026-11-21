package check

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type span struct{ from, to int }

// touchedSet is the lines a change added or edited, by slash-separated path relative to the tree root.
type touchedSet map[string][]span

func (t touchedSet) has(file string, line int) bool {
	for _, s := range t[file] {
		if s.from <= line && line <= s.to {
			return true
		}
	}
	return false
}

// pythonFiles lists the touched files that june reads, sorted.
func (t touchedSet) pythonFiles() []string {
	var files []string
	for f := range t {
		if path.Ext(f) == ".py" {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

const listFix = "write one path:7 or path:7-12 per line, with paths relative to the repository root"

// readTouched parses the supplied list. A nil reader means the touched set is unknowable.
// The returned message is a complete "<cause>. Fix: <instruction>" refusal.
func readTouched(env Env, r io.Reader) (touchedSet, *string) {
	fail := func(format string, a ...any) (touchedSet, *string) {
		msg := fmt.Sprintf(format, a...)
		return nil, &msg
	}
	if r == nil {
		return fail("the touched lines are unknown. Fix: pass --changed-lines FILE, or - to read the list from stdin")
	}

	set := touchedSet{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		file, sp, err := parseEntry(line)
		if err != nil {
			return fail("changed-lines line %d, %q, is not valid: %v. Fix: %s", n, line, err, listFix)
		}
		set[file] = append(set[file], sp)
	}
	if err := sc.Err(); err != nil {
		return fail("the changed-lines list cannot be read (%v). Fix: %s", err, listFix)
	}
	for _, f := range set.pythonFiles() {
		if !fileExists(env.Dir, f) {
			return fail("changed-lines names %s, which is not a file in the tree. Fix: regenerate the list from the current tree", f)
		}
	}
	return set, nil
}

func parseEntry(line string) (string, span, error) {
	i := strings.LastIndex(line, ":")
	if i <= 0 || i == len(line)-1 {
		return "", span{}, fmt.Errorf("expected path:N or path:N-M")
	}
	file, lines := line[:i], line[i+1:]

	from, to, isRange := strings.Cut(lines, "-")
	a, err := strconv.Atoi(from)
	if err != nil || a < 1 {
		return "", span{}, fmt.Errorf("%q is not a line number from 1", from)
	}
	b := a
	if isRange {
		if b, err = strconv.Atoi(to); err != nil || b < a {
			return "", span{}, fmt.Errorf("%q is not a line number from %d", to, a)
		}
	}

	clean := path.Clean(filepath.ToSlash(file))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", span{}, fmt.Errorf("the path %q is not inside the repository", file)
	}
	return clean, span{a, b}, nil
}

func fileExists(dir, rel string) bool {
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}
