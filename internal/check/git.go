package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// scope names the base a git run judged against and where june got it.
// scanDir is a temporary tree of the diff post-image for a staged or ref run; empty means the worktree.
// The caller removes scanDir when it is set.
type scope struct{ base, source, scanDir string }

// fullDepth is the fix for a GitLab clone too shallow to hold the merge request diff base.
const fullDepth = "clone the full history, with GIT_DEPTH: 0 in the GitLab CI job variables"

const (
	sourceFlag     = "--base"
	sourceMR       = "merge request diff base"
	sourceUpstream = "upstream branch"
	sourceDefault  = "remote default branch"
	sourcePull     = "pull request base"
	sourceRefs     = "pre-commit refs"
	sourceStaged   = "staged diff"
)

// onGitHub reports whether env is a GitHub Actions run.
func onGitHub(env Env) bool { return env.Vars["GITHUB_ACTIONS"] == "true" }

// pullRequestBase is the remote-tracking ref of the branch a GitHub pull request targets.
// actions/checkout fetches it with fetch-depth: 0. It is "" outside a pull request event.
func pullRequestBase(env Env) string {
	if !onGitHub(env) {
		return ""
	}
	switch env.Vars["GITHUB_EVENT_NAME"] {
	case "pull_request", "pull_request_target":
		if ref := env.Vars["GITHUB_BASE_REF"]; ref != "" {
			return "origin/" + ref
		}
	}
	return ""
}

// gitTouched finds the lines a change added or edited.
// An empty base is inferred, first hit wins: pre-commit's from and to refs, a GitHub pull request base,
// a GitLab merge request base passed in by the caller, pre-commit's staged diff (the index against HEAD,
// or the repository's empty tree before the first commit), the upstream branch of HEAD, else the remote default branch.
// Refs and the staged diff are read from the Env, never from the process.
// A ref run reads the to-ref from the object store. A staged or ref run scans that diff's
// post-image, not the worktree. A local run includes the worktree and untracked files.
// source says where a non-empty base came from, so a refusal can name the right fix.
// Paths are relative to env.Dir. The returned message is a complete "<cause>. Fix: <instruction>" refusal.
// A cancelled context returns its error.
func gitTouched(ctx context.Context, env Env, base, source string) (touchedSet, scope, *string, error) {
	sc := scope{base: base, source: source}
	staged := false
	toRef := ""
	scanDir := ""
	keepScan := false
	defer func() {
		if !keepScan && scanDir != "" {
			_ = os.RemoveAll(scanDir)
		}
	}()
	// An explicit --base wins. Pre-commit refs, then a GitHub pull request base, outrank a merge
	// request base the caller already resolved. The staged diff comes after that base.
	if source != sourceFlag {
		from, to := env.Vars["PRE_COMMIT_FROM_REF"], env.Vars["PRE_COMMIT_TO_REF"]
		if from != "" && to != "" {
			base, toRef = from, to
			sc = scope{base: base, source: sourceRefs}
		} else if pr := pullRequestBase(env); pr != "" {
			base = pr
			sc = scope{base: base, source: sourcePull}
		}
	}
	refuse := func(format string, a ...any) (touchedSet, scope, *string, error) {
		msg := fmt.Sprintf(format, a...)
		return nil, scope{}, &msg, nil
	}
	if strings.HasPrefix(base, "-") {
		return refuse("the base %q is not a git ref. Fix: pass a branch, tag or commit to --base", base)
	}

	if _, err := git(ctx, env, "", "rev-parse", "--is-inside-work-tree"); err != nil {
		if ctx.Err() != nil {
			return nil, scope{}, nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return refuse("git was not found, so the touched lines cannot be found. Fix: install git and put it on PATH, or pass --changed-lines FILE")
		}
		return refuse("the touched lines cannot be found because %s is not inside a git work tree. Fix: run june inside the repository, or pass --changed-lines FILE", filepath.Base(env.Dir))
	}
	if base == "" {
		if env.Vars["PRE_COMMIT"] == "1" {
			staged = true
			sc = scope{base: "HEAD", source: sourceStaged}
		} else {
			// actions/checkout makes the pushed branch track itself, so an upstream there would be HEAD and judge nothing.
			pushed := onGitHub(env) && env.Vars["GITHUB_EVENT_NAME"] == "push"
			inferred, got := "", ""
			if !pushed {
				inferred, got = inferBase(ctx, env)
			}
			if err := ctx.Err(); err != nil {
				return nil, scope{}, nil, err
			}
			if inferred == "" && onGitHub(env) {
				event := env.Vars["GITHUB_EVENT_NAME"]
				return refuse("a %s event has no pull request base, so the touched lines cannot be found. Fix: pass --base REF (for example --base origin/main) with fetch-depth: 0 on actions/checkout, or run june on pull_request", event)
			}
			if inferred == "" {
				return refuse("no base branch can be inferred because the current branch has no upstream and the repository has no remote default branch. Fix: pass --base REF, or --changed-lines FILE")
			}
			base = inferred
			sc = scope{base: base, source: got}
		}
	}

	var mb string
	if staged {
		if head, err := git(ctx, env, "", "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
			mb = head
		} else if ctx.Err() != nil {
			return nil, scope{}, nil, ctx.Err()
		} else {
			id, err := git(ctx, env, "", "hash-object", "-t", "tree", "--stdin")
			if err != nil {
				if ctx.Err() != nil {
					return nil, scope{}, nil, ctx.Err()
				}
				return refuse("git could not name the empty tree (%v). Fix: run git hash-object -t tree --stdin to see why", err)
			}
			mb = strings.TrimSpace(id)
			sc.base = mb
		}
	} else {
		if _, err := git(ctx, env, "", "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			if sc.source == sourcePull {
				return refuse("the base %q is not a commit in this repository. Fix: set fetch-depth: 0 on actions/checkout so the pull request base is fetched, or pass an existing branch, tag or commit to --base", base)
			}
			if sc.source == sourceMR {
				return refuse("the merge request diff base %s is not a commit in this repository. Fix: %s", base, fullDepth)
			}
			return refuse("the base %q is not a commit in this repository. Fix: fetch it, or pass an existing branch, tag or commit to --base", base)
		}
		end := "HEAD"
		if toRef != "" {
			if _, err := git(ctx, env, "", "rev-parse", "--verify", "--quiet", toRef+"^{commit}"); err != nil {
				if ctx.Err() != nil {
					return nil, scope{}, nil, ctx.Err()
				}
				return refuse("pre-commit's to-ref %q is not a commit in this repository. Fix: fetch it, or pass --base REF or --changed-lines FILE", toRef)
			}
			end = toRef
		}
		var err error
		if mb, err = git(ctx, env, "", "merge-base", base, end); err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			if sc.source == sourceMR {
				return refuse("HEAD and the merge request diff base %s have no merge base, so the touched lines are unknowable. Fix: %s", base, fullDepth)
			}
			return refuse("%s and %q have no merge base, so the touched lines are unknowable. Fix: fetch full history (git fetch --unshallow) or pass a base that shares history with %s", end, base, end)
		}
	}

	prefix, err := git(ctx, env, "", "rev-parse", "--show-prefix")
	if err != nil {
		if ctx.Err() != nil {
			return nil, scope{}, nil, ctx.Err()
		}
		return refuse("git could not locate the working directory. Fix: run git status to see why")
	}
	prefix = strings.TrimSuffix(prefix, "\n")
	absent := map[string]bool{}
	if !staged && toRef == "" {
		untracked, err := git(ctx, env, "", "ls-files", "--others", "--exclude-standard", "-z", "--")
		if err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			return refuse("git could not list untracked files (%v). Fix: run git status to see why", err)
		}
		var entries strings.Builder
		if untracked != "" {
			empty, err := git(ctx, env, "", "hash-object", "--stdin")
			if err != nil {
				if ctx.Err() != nil {
					return nil, scope{}, nil, ctx.Err()
				}
				return refuse("git could not prepare untracked files. Fix: run git status to see why")
			}
			for _, name := range strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00") {
				if path.Ext(name) != ".py" {
					continue
				}
				info, err := os.Lstat(filepath.Join(env.Dir, filepath.FromSlash(name)))
				if err != nil {
					return refuse("untracked file %q cannot be inspected. Fix: check its permissions and run june again", name)
				}
				if !info.Mode().IsRegular() {
					continue
				}
				mode := "100644"
				if info.Mode().Perm()&0o111 != 0 {
					mode = "100755"
				}
				fmt.Fprintf(&entries, "%s %s 0\t%s%s\x00", mode, strings.TrimSpace(empty), prefix, name)
			}
		}
		tracked, err := git(ctx, env, "", "ls-files", "--stage", "-t", "-z", "--", ":/")
		if err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			return refuse("git could not list tracked files. Fix: run git status to see why")
		}
		var skipped strings.Builder
		for _, record := range strings.Split(tracked, "\x00") {
			if record == "" {
				continue
			}
			metadata, name, _ := strings.Cut(record[2:], "\t")
			repoPath := path.Join(prefix, name)
			fmt.Fprintf(&entries, "%s\t%s\x00", metadata, repoPath)
			if record[0] != 'S' {
				continue
			}
			if _, err := os.Lstat(filepath.Join(env.Dir, filepath.FromSlash(name))); os.IsNotExist(err) {
				skipped.WriteString(name)
				skipped.WriteByte(0)
				absent[repoPath] = true
			} else if err != nil {
				return refuse("sparse file %q cannot be inspected. Fix: check its permissions and run june again", name)
			}
		}
		dir, err := os.MkdirTemp("", "june-git-")
		if err != nil {
			return refuse("a temporary git index cannot be created. Fix: check temporary directory permissions and run june again")
		}
		defer func() { _ = os.RemoveAll(dir) }()
		vars := make(map[string]string, len(env.Vars)+1)
		for k, v := range env.Vars {
			vars[k] = v
		}
		vars["GIT_INDEX_FILE"] = filepath.Join(dir, "index")
		env.Vars = vars
		if _, err := git(ctx, env, entries.String(), "-c", "core.ignorestat=false", "update-index", "--no-split-index", "-z", "--index-info"); err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			return refuse("a temporary git index cannot be prepared. Fix: run git status and check temporary directory permissions")
		}
		if skipped.Len() > 0 {
			if _, err := git(ctx, env, skipped.String(), "-c", "core.ignorestat=false", "update-index", "--no-split-index", "--skip-worktree", "-z", "--stdin"); err != nil {
				if ctx.Err() != nil {
					return nil, scope{}, nil, ctx.Err()
				}
				return refuse("a temporary git index cannot be prepared. Fix: run git status and check temporary directory permissions")
			}
		}

	}
	// -M forces rename detection whatever diff.renames says, so a pure rename shows no hunk.
	// --no-textconv and --no-ext-diff keep the diff a pure function of the trees.
	// A staged run diffs the index itself, which pre-commit points at the tree the commit will hold,
	// and the scan reads those index blobs.
	// A ref run diffs the to-ref and the scan reads that commit's blobs, so the worktree and the index do not join.
	var cached, tip []string
	switch {
	case staged:
		cached = []string{"--cached"}
	case toRef != "":
		tip = []string{toRef}
	}
	nameArgs := append(append([]string{"diff"}, cached...), "--name-status", "--diff-filter=d", "-z", "--no-relative", "--no-color", "--no-ext-diff", "--no-textconv", "-M", strings.TrimSpace(mb))
	changes, err := git(ctx, env, "", append(append(nameArgs, tip...), "--")...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, scope{}, nil, ctx.Err()
		}
		return refuse("git could not diff against the merge base with %q (%v). Fix: run git diff %s to see why", sc.base, err, sc.base)
	}
	var paths []string
	for records := strings.Split(changes, "\x00"); len(records) >= 2; {
		status, source := records[0], records[1]
		records = records[2:]
		destination := source
		if strings.HasPrefix(status, "R") {
			destination = records[0]
			records = records[1:]
		}
		if path.Ext(destination) != ".py" || !strings.HasPrefix(destination, prefix) {
			continue
		}
		paths = append(paths, exactPathspec(source))
		if destination != source {
			paths = append(paths, exactPathspec(destination))
		}
	}
	if len(paths) == 0 {
		return touchedSet{}, sc, nil, ctx.Err()
	}
	args := append(append([]string{"-c", "core.quotepath=off", "diff"}, cached...), "--text", "--no-relative", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "-M", "--unified=0", "--inter-hunk-context=0", strings.TrimSpace(mb))
	args = append(append(args, tip...), "--")
	patch, err := git(ctx, env, "", append(args, paths...)...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, scope{}, nil, ctx.Err()
		}
		return refuse("git could not diff against the merge base with %q (%v). Fix: run git diff %s to see why", sc.base, err, sc.base)
	}
	parsed := parsePatch(patch)
	var post map[string]postBlob
	if staged || toRef != "" {
		rawArgs := append(append([]string{"diff"}, cached...), "--raw", "--abbrev=64", "--diff-filter=d", "-z", "--no-relative", "--no-color", "--no-ext-diff", "--no-textconv", "-M", strings.TrimSpace(mb))
		raw, err := git(ctx, env, "", append(append(rawArgs, tip...), "--")...)
		if err != nil {
			if ctx.Err() != nil {
				return nil, scope{}, nil, ctx.Err()
			}
			return refuse("git could not diff against the merge base with %q (%v). Fix: run git diff %s to see why", sc.base, err, sc.base)
		}
		post = parseRaw(raw)
	}
	set := touchedSet{}
	for _, name := range parsed.pythonFiles() {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		var content []byte
		if post != nil {
			blob, ok := post[name]
			if !ok {
				return refuse("changed file %q cannot be read. Fix: check its permissions and run june again", rel)
			}
			if !strings.HasPrefix(blob.mode, "100") {
				continue
			}
			out, err := git(ctx, env, "", "cat-file", "blob", blob.sha)
			if err != nil {
				if ctx.Err() != nil {
					return nil, scope{}, nil, ctx.Err()
				}
				return refuse("changed file %q cannot be read. Fix: check its permissions and run june again", rel)
			}
			content = []byte(out)
		} else {
			abs := filepath.Join(env.Dir, filepath.FromSlash(rel))
			info, err := os.Lstat(abs)
			if os.IsNotExist(err) && absent[name] {
				continue
			}
			if err != nil {
				return refuse("changed file %q cannot be inspected. Fix: check its permissions and run june again", rel)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			content, err = os.ReadFile(abs)
			if err != nil {
				return refuse("changed file %q cannot be read. Fix: check its permissions and run june again", rel)
			}
		}
		if bytes.IndexByte(content, 0) >= 0 {
			continue
		}
		if post != nil {
			if err := writeScanFile(&scanDir, rel, content); err != nil {
				return refuse("a temporary copy of %q cannot be created. Fix: check temporary directory permissions and run june again", rel)
			}
		}
		set[rel] = parsed[name]
	}
	if err := ctx.Err(); err != nil {
		return nil, scope{}, nil, err
	}
	sc.scanDir = scanDir
	keepScan = true
	return set, sc, nil, nil
}

// postBlob is one destination in a raw diff: mode is the git file mode and sha is the post-image blob.
type postBlob struct{ mode, sha string }

// parseRaw reads a -z --raw diff into the post-image of each destination path.
func parseRaw(raw string) map[string]postBlob {
	out := map[string]postBlob{}
	records := strings.Split(raw, "\x00")
	for len(records) >= 2 && records[0] != "" {
		meta, path := records[0], records[1]
		records = records[2:]
		fields := strings.Fields(meta)
		if len(fields) < 5 {
			break
		}
		status := fields[4]
		dest := path
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if len(records) == 0 || records[0] == "" {
				break
			}
			dest = records[0]
			records = records[1:]
		}
		out[dest] = postBlob{mode: fields[1], sha: fields[3]}
	}
	return out
}

// writeScanFile stores content where a later scan reads it, under the repo-relative path.
func writeScanFile(dir *string, rel string, content []byte) error {
	if *dir == "" {
		d, err := os.MkdirTemp("", "june-post-")
		if err != nil {
			return err
		}
		*dir = d
	}
	path := filepath.Join(*dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// inferBase picks the base of a bare run: a GitHub pull request base, else the upstream branch of HEAD, else the default branch of origin.
// It returns "" when neither exists. Both are local reads; nothing is fetched.
func inferBase(ctx context.Context, env Env) (base, source string) {
	if pr := pullRequestBase(env); pr != "" {
		return pr, sourcePull
	}
	if out, err := git(ctx, env, "", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, sourceUpstream
		}
	}
	if out, err := git(ctx, env, "", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, sourceDefault
		}
	}
	return "", ""
}

func exactPathspec(name string) string {
	var pattern strings.Builder
	pattern.WriteString(":(top,glob)")
	for i := 0; i < len(name); i++ {
		if name[i] != '/' {
			pattern.WriteByte('\\')
		}
		pattern.WriteByte(name[i])
	}
	return pattern.String()
}

// scrubbedVars are the caller's git variables that change what a diff or a pathspec means.
// git drops them so the touched lines depend on the trees, not on the caller's shell.
var scrubbedVars = map[string]bool{
	"GIT_DIFF_OPTS":         true,
	"GIT_EXTERNAL_DIFF":     true,
	"GIT_GLOB_PATHSPECS":    true,
	"GIT_ICASE_PATHSPECS":   true,
	"GIT_LITERAL_PATHSPECS": true,
	"GIT_NOGLOB_PATHSPECS":  true,
}

// git runs git in env.Dir and returns its standard output.
func git(ctx context.Context, env Env, input string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = env.Dir
	cmd.Env = make([]string, 0, len(env.Vars)+4)
	for k, v := range env.Vars {
		if !scrubbedVars[k] {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	// Later entries win, so these fix the language and keep git read-only and non-interactive.
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(input)
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
// Deletions, mode changes and pure renames carry no added lines and so touch nothing.
func parsePatch(patch string) touchedSet {
	set := touchedSet{}
	var file string // "" while in a header or a file that does not count
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = ""
			inHunk = false
		case strings.HasPrefix(line, "+++ ") && !inHunk:
			file = postImagePath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ ") && file != "":
			inHunk = true
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
