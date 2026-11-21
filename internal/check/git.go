package check

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// gitTouched finds the lines added or edited since the merge base of base and HEAD:
// committed changes, staged and unstaged changes, and untracked files.
// Paths are relative to env.Dir. The returned message is a complete "<cause>. Fix: <instruction>" refusal.
// A cancelled context returns its error.
func gitTouched(ctx context.Context, env Env, base string) (touchedSet, *string, error) {
	refuse := func(format string, a ...any) (touchedSet, *string, error) {
		msg := fmt.Sprintf(format, a...)
		return nil, &msg, nil
	}
	if strings.HasPrefix(base, "-") {
		return refuse("the base %q is not a git ref. Fix: pass a branch, tag or commit to --base", base)
	}

	if _, err := git(ctx, env, "rev-parse", "--is-inside-work-tree"); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return refuse("the touched lines cannot be found because %s is not inside a git work tree. Fix: run june inside the repository, or pass --changed-lines FILE", filepath.Base(env.Dir))
	}
	if _, err := git(ctx, env, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return refuse("the base %q is not a commit in this repository. Fix: fetch it, or pass an existing branch, tag or commit to --base", base)
	}
	mb, err := git(ctx, env, "merge-base", base, "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return refuse("HEAD and %q have no merge base, so the touched lines are unknowable. Fix: fetch full history (git fetch --unshallow) or pass a base that shares history with HEAD", base)
	}

	// -M forces rename detection whatever diff.renames says, so a pure rename shows no hunk.
	// --no-textconv and --no-ext-diff keep the diff a pure function of the trees.
	patch, err := git(ctx, env, "-c", "core.quotepath=off", "diff", "--relative", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--unified=0", strings.TrimSpace(mb), "--")
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return refuse("git could not diff against the merge base with %q (%v). Fix: run git diff %s to see why", base, err, base)
	}
	set := parsePatch(patch)

	untracked, err := git(ctx, env, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return refuse("git could not list untracked files (%v). Fix: run git status to see why", err)
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if path.Ext(name) != ".py" {
			continue
		}
		if n := countLines(filepath.Join(env.Dir, filepath.FromSlash(name))); n > 0 {
			set[name] = append(set[name], span{1, n})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return set, nil, nil
}

// git runs git in env.Dir and returns its standard output.
func git(ctx context.Context, env Env, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = env.Dir
	cmd.Env = make([]string, 0, len(env.Vars)+3)
	for k, v := range env.Vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Later entries win, so these fix the language and keep git read-only and non-interactive.
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, firstLine(msg))
		}
		return "", err
	}
	return out.String(), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// parsePatch reads a zero-context unified diff into the post-image lines it adds or edits.
// Deletions, mode changes, pure renames and binary files carry no added lines and so touch nothing.
func parsePatch(patch string) touchedSet {
	set := touchedSet{}
	var file string // "" while in a header or a file that does not count
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = ""
		case strings.HasPrefix(line, "+++ "):
			file = postImagePath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ ") && file != "":
			if sp, ok := parseHunk(line); ok {
				set[file] = append(set[file], sp)
			}
		}
	}
	return set
}

// postImagePath decodes the "+++" path of a diff, or "" for /dev/null.
func postImagePath(s string) string {
	if strings.HasPrefix(s, `"`) {
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			return ""
		}
		s = unquoted
	} else {
		s = strings.TrimSuffix(s, "\t") // git marks a name with spaces by a trailing tab
	}
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}

// parseHunk reads the post-image range of "@@ -a,b +c,d @@". A hunk that adds no lines reports false.
func parseHunk(header string) (span, bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return span{}, false
	}
	start, count, hasCount := strings.Cut(strings.TrimPrefix(fields[2], "+"), ",")
	from, err := strconv.Atoi(start)
	if err != nil {
		return span{}, false
	}
	n := 1
	if hasCount {
		if n, err = strconv.Atoi(count); err != nil {
			return span{}, false
		}
	}
	if n == 0 {
		return span{}, false
	}
	return span{from, from + n - 1}, true
}

// countLines counts the lines of a text file; a binary, unreadable or non-regular file has none.
func countLines(name string) int {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	b, err := os.ReadFile(name)
	if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}
