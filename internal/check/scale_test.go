package check_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vicolby/june/internal/check"
)

// timed runs one check under a deadline of budget: a run that takes longer is cancelled and fails the test.
func timed(t *testing.T, budget time.Duration, env check.Env, req check.Request) check.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	res, err := check.Change(ctx, env, req)
	if err != nil {
		t.Fatalf("check did not finish within %v: %v", budget, err)
	}
	return res
}

// manyChanges returns a repository whose topic branch is based on main and whose worktree edits n Python files,
// each under a long directory name, so the list of changed paths outgrows any argv.
// It returns the expected findings, one per file.
func manyChanges(t *testing.T, n int) (check.Env, string) {
	t.Helper()
	long := strings.Repeat("d", 80)
	files := make(map[string]string, n)
	var names []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s/pkg%03d/mod%05d.py", long, i%100, i)
		files[name] = "x = 1\n"
		names = append(names, name)
	}
	env := repo(t, files)
	var want strings.Builder
	for _, name := range sortedCopy(names) {
		write(t, filepath.Join(env.Dir, name), "x = 1\n"+old)
		fmt.Fprintf(&want, "%s:2:9: %s", name, py012)
	}
	return env, want.String()
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// expectWith checks an exit 1 verdict and that the summary line states the scope that was covered.
func expectWith(t *testing.T, res check.Result, findings, scope string) {
	t.Helper()
	expect(t, res, 1, findings)
	if !strings.Contains(summaryOf(res), scope) {
		t.Errorf("summary = %q, want it to state %q", summaryOf(res), scope)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestChangeThreeThousandChangedFilesWithinBudget(t *testing.T) {
	env, want := manyChanges(t, 3000)
	res := timed(t, 5*time.Second, env, check.Request{Base: "main"})
	expectWith(t, res, want, "checked 3000 files, 3000 changed lines")
}

// Past about 7000 paths a pathspec per changed file overflows argv, on every kind of run.
func TestChangeSevenThousandChangedFiles(t *testing.T) {
	env, want := manyChanges(t, 7000)
	const budget = 10 * time.Second
	expectWith(t, timed(t, budget, env, check.Request{Base: "main"}), want, "checked 7000 files, 7000 changed lines")

	git(t, env.Dir, "add", "-A")
	staged := check.Env{Dir: env.Dir, Vars: map[string]string{"PRE_COMMIT": "1"}}
	staged.Vars["PRE_COMMIT_FROM_REF"], staged.Vars["PRE_COMMIT_TO_REF"] = "", ""
	// A staged run judges the index against HEAD, which is main here.
	expectWith(t, timed(t, budget, staged, check.Request{}), want, "checked 7000 files, 7000 changed lines")

	git(t, env.Dir, "commit", "-q", "-m", "change everything")
	refs := check.Env{Dir: env.Dir, Vars: map[string]string{"PRE_COMMIT_FROM_REF": "main", "PRE_COMMIT_TO_REF": "HEAD"}}
	expectWith(t, timed(t, budget, refs, check.Request{}), want, "checked 7000 files, 7000 changed lines")
	expectWith(t, timed(t, budget, env, check.Request{Base: "main"}), want, "checked 7000 files, 7000 changed lines")
}

// What a run costs follows what changed, not how much the repository tracks.
func TestChangeInHugeRepoCostsTheChange(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a repository of 100000 files")
	}
	env := hugeRepo(t, 100_000)
	write(t, filepath.Join(env.Dir, "d0", "s0", "f0.py"), "x = 0\n"+old)
	write(t, filepath.Join(env.Dir, "d0", "s0", "fresh.py"), old)
	// The first run warms the file system cache. The budget is for a run in a working repository, and
	// three tries keep a noisy machine from failing a run that costs the change and not the repository.
	if _, err := check.Change(context.Background(), env, check.Request{Base: "main"}); err != nil {
		t.Fatal(err)
	}
	var res check.Result
	for try := 1; ; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var err error
		res, err = check.Change(ctx, env, check.Request{Base: "main"})
		cancel()
		if err == nil {
			break
		}
		if try == 3 {
			t.Fatalf("a one-file change in a repository of 100000 files did not finish within 1s in %d tries: %v", try, err)
		}
	}
	expectWith(t, res, "d0/s0/f0.py:2:9: "+py012+"d0/s0/fresh.py:1:9: "+py012, "checked 2 files, 2 changed lines")
}

// hugeRepo makes a repository of n tracked files, a fifth of them Python, with a clean topic branch off main.
func hugeRepo(t *testing.T, n int) check.Env {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", "june")
	git(t, dir, "config", "user.email", "june@example.com")
	git(t, dir, "config", "commit.gpgsign", "false")
	var stream bytes.Buffer
	stream.WriteString("commit refs/heads/main\ncommitter june <june@example.com> 0 +0000\ndata 4\nbase\n")
	add := func(path, body string) {
		fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", path, len(body), body)
	}
	for i := 0; i < n; i++ {
		ext := "txt"
		if i%5 == 0 {
			ext = "py"
		}
		add(fmt.Sprintf("d%d/s%d/f%d.%s", i/1000, i/100, i, ext), fmt.Sprintf("x = %d\n", i))
	}
	rulesDir := filepath.Join("..", "..", "testdata", "rules", "python")
	for _, name := range []string{"PY012", "PY013", "PY014", "PY015", "PY016"} {
		add(".june/rules/"+name+".yml", readFile(t, filepath.Join(rulesDir, name+".yml")))
		add(".june/rules/rule-tests/"+name+"-test.yml", readFile(t, filepath.Join(rulesDir, "rule-tests", name+"-test.yml")))
	}
	cmd := exec.CommandContext(context.Background(), "git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, out)
	}
	git(t, dir, "checkout", "-q", "-f", "main")
	git(t, dir, "checkout", "-q", "-b", "topic")
	return check.Env{Dir: dir}
}

// Files renamed from a path that is not Python are judged against their source, however many there are:
// only the edited lines count, and none of the explicit pairs may overflow argv.
func TestChangeManyRenamesFromNonPython(t *testing.T) {
	// Below git's own limit on inexact rename detection, and far past what fits in one set of pathspecs.
	const n = 900
	body := func(i int) string { return fmt.Sprintf("import os\nimport sys\nx = %d\ny = 2\nz = 3\n", i) }
	from := func(i int) string { return fmt.Sprintf("%s/legacy%04d.txt", strings.Repeat("o", 60), i) }
	to := func(i int) string { return fmt.Sprintf("%s/legacy%04d.py", strings.Repeat("n", 60), i) }
	files := map[string]string{}
	for i := 0; i < n; i++ {
		files[from(i)] = body(i)
	}
	env := repo(t, files)
	var want strings.Builder
	for i := 0; i < n; i++ {
		if err := os.MkdirAll(filepath.Join(env.Dir, filepath.Dir(to(i))), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(env.Dir, from(i)), filepath.Join(env.Dir, to(i))); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(env.Dir, to(i)), body(i)+old)
		fmt.Fprintf(&want, "%s:6:9: %s", to(i), py012)
	}
	const scope = "checked 900 files, 900 changed lines"
	expectWith(t, timed(t, 10*time.Second, env, check.Request{Base: "main"}), want.String(), scope)
	git(t, env.Dir, "add", "-A")
	expectWith(t, timed(t, 10*time.Second, env, check.Request{Base: "main"}), want.String(), scope)
	git(t, env.Dir, "commit", "-q", "-m", "move")
	expectWith(t, timed(t, 10*time.Second, env, check.Request{Base: "main"}), want.String(), scope)
}

// A new Python file that resembles a Python file that became something else is still new:
// a diff limited to Python files would pair them and judge only the lines that differ.
func TestChangeNewFileIsNotPairedWithFileThatBecameNotPython(t *testing.T) {
	body := "import os\nimport sys\nx = 1\n" + old
	env := repo(t, map[string]string{"legacy.py": body})
	git(t, env.Dir, "mv", "legacy.py", "legacy.txt")
	write(t, filepath.Join(env.Dir, "fresh.py"), body+"y = 2\n")
	want := "fresh.py:4:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "add", "-A")
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "commit", "-q", "-m", "split")
	expect(t, against(t, env, "main"), 1, want)
}

// A new Python file that shares a redo batch with a rename from a non-Python path is still new.
// fresh.py copies a.txt's mutable default onto line 81. The full diff leaves fresh.py unpaired, so
// that line is checked. Git keeps four rename candidates per destination: s0.txt through s3.txt
// outscore a.txt for fresh.py and are claimed by t0.dat through t3.dat. decoy.py is renamed to
// decoy.txt, which is not Python, so the Python-only diff pairs decoy.py with fresh.py and fresh.py
// joins the redo batch of moved.py. Pairing a.txt with fresh.py would hide line 81, and treating
// moved.py as new would report the mutable default it copied onto line 101.
func TestChangeNewFileIsNotPairedWithSourceOfAnotherRename(t *testing.T) {
	const (
		freshViol = 80  // line 81 of fresh.py, copied from a.txt
		movedViol = 200 // line 101 of moved.py, copied from a.txt
	)
	line := func(id int) string {
		switch id {
		case freshViol:
			return old
		case movedViol:
			return "def g(b=[]): pass\n" // same length as old, so it stays one rename chunk
		default:
			return fmt.Sprintf("v%012d = 1\n", id)
		}
	}
	text := func(groups ...[]int) string {
		var b strings.Builder
		for _, group := range groups {
			for _, id := range group {
				b.WriteString(line(id))
			}
		}
		return b.String()
	}
	span := func(from, to int) []int {
		ids := make([]int, to-from)
		for i := range ids {
			ids[i] = from + i
		}
		return ids
	}
	// Every file is 200 equal lines, so a rename score is the share of those lines.
	// fresh.py shares 180 with each sN.txt, 160 with a.txt and 130 with decoy.py.
	// tN.dat shares 195 with sN.txt, moved.py shares 140 with a.txt, decoy.txt shares 190 with decoy.py.
	fs, rest := span(0, 180), span(180, 200)
	mo, pad := span(200, 240), span(240, 300)
	body := func(name string, groups ...[]int) string {
		t.Helper()
		out := text(groups...)
		if n := strings.Count(out, "\n"); n != 200 {
			t.Fatalf("%s has %d lines, want 200", name, n)
		}
		return out
	}
	base := map[string]string{
		"a.txt":    body("a.txt", span(0, 140), rest, mo),
		"decoy.py": body("decoy.py", rest, span(70, 180), span(1500, 1570)),
	}
	for n := 0; n < 4; n++ {
		base[fmt.Sprintf("s%d.txt", n)] = body(fmt.Sprintf("s%d.txt", n), fs, span(1000+n*100, 1020+n*100))
	}
	env := repo(t, base)
	for n := 0; n < 4; n++ {
		dst := fmt.Sprintf("t%d.dat", n)
		git(t, env.Dir, "mv", fmt.Sprintf("s%d.txt", n), dst)
		uniq := span(1000+n*100, 1020+n*100)
		write(t, filepath.Join(env.Dir, dst), body(dst, fs, uniq[:15], span(1400+n*100, 1405+n*100)))
	}
	git(t, env.Dir, "mv", "a.txt", "moved.py")
	write(t, filepath.Join(env.Dir, "moved.py"), body("moved.py", span(0, 80), rest, mo, pad))
	git(t, env.Dir, "mv", "decoy.py", "decoy.txt")
	write(t, filepath.Join(env.Dir, "decoy.txt"), body("decoy.txt", rest, span(70, 180), span(1500, 1560), span(1600, 1610)))
	write(t, filepath.Join(env.Dir, "fresh.py"), body("fresh.py", fs, rest))
	want := "fresh.py:81:9: " + py012
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "add", "-A")
	expect(t, against(t, env, "main"), 1, want)
	git(t, env.Dir, "commit", "-q", "-m", "split")
	expect(t, against(t, env, "main"), 1, want)
}
