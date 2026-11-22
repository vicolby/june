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

// firstIn is the first touched line of file within from..to, if any.
func (t touchedSet) firstIn(file string, from, to int) (int, bool) {
	best := 0
	for _, s := range t[file] {
		lo, hi := max(s.from, from), min(s.to, to)
		if lo <= hi && (best == 0 || lo < best) {
			best = lo
		}
	}
	return best, best != 0
}

// pythonFiles includes excluded paths retained for summary counts, sorted.
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

// lineCount is the number of distinct touched lines in file, however the spans overlap.
func (t touchedSet) lineCount(file string) int {
	spans := append([]span(nil), t[file]...)
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	n, next := 0, 0
	for _, s := range spans {
		from := max(s.from, next)
		if s.to >= from {
			n += s.to - from + 1
			next = s.to + 1
		}
	}
	return n
}

const listFix = "write one path:7 or path:7-12 per line, with paths relative to the repository root"

// readTouched parses the supplied list.
// The returned message is a complete "<cause>. Fix: <instruction>" refusal.
func readTouched(env Env, r io.Reader, skip *excluder) (touchedSet, *string) {
	fail := func(format string, a ...any) (touchedSet, *string) {
		msg := fmt.Sprintf(format, a...)
		return nil, &msg
	}
	set := touchedSet{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		file, sp, err := parseEntry(line)
		if err != nil {
			return fail("changed-lines line %d is not valid: %v. Fix: %s", n, err, listFix)
		}
		set[file] = append(set[file], sp)
	}
	if err := sc.Err(); err != nil {
		return fail("the changed-lines list cannot be read. Fix: %s", listFix)
	}
	for _, f := range set.pythonFiles() {
		if skip.excluded(f) {
			continue
		}
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
		return "", span{}, fmt.Errorf("expected a line number from 1")
	}
	b := a
	if isRange {
		if b, err = strconv.Atoi(to); err != nil || b < a {
			return "", span{}, fmt.Errorf("expected a line number from %d", a)
		}
	}

	clean := path.Clean(filepath.ToSlash(file))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", span{}, fmt.Errorf("the path is not inside the repository")
	}
	return clean, span{a, b}, nil
}

func fileExists(dir, rel string) bool {
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}
