package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const defaultConfig = ".june/config.yml"

// config is the content of .june/config.yml. An unknown key is an error, so nothing in it is silently dropped.
type config struct {
	// Rules lists rules directories, relative to the directory that holds .june. A --rules flag replaces it.
	Rules []string `yaml:"rules"`
	// Base is the git ref to judge against when nothing more specific (a flag, a CI event, pre-commit refs) names one.
	Base string `yaml:"base"`
	// Exclude lists path globs, relative to the directory that holds .june.
	Exclude []string `yaml:"exclude"`
}

// project is where june's own files live: the repository root, or the working directory outside git.
type project struct {
	dir    string
	prefix string // the working directory under dir, slash-separated with a trailing slash, "" when they are the same
	cfg    config
}

// repoLocation is the repository root above env.Dir and env.Dir's path under it, slash-separated with a trailing slash.
// Both are "" outside git.
func repoLocation(ctx context.Context, env Env) (root, prefix string, err error) {
	out, err := git(ctx, env, "", "rev-parse", "--show-toplevel", "--show-prefix")
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	said := gitSaid(err)
	if strings.HasPrefix(said, "fatal: not a git repository") {
		return "", "", nil
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", "", errors.New("git was not found, so the repository root cannot be found. Fix: install git and put it on PATH")
		}
		if root, ok := dubiousRoot(said); ok {
			return "", "", fmt.Errorf("git refuses to use this repository (%s). Fix: git config --global --add safe.directory %s", said, shellQuote(root))
		}
		return "", "", fmt.Errorf("git could not inspect the repository (%s). Fix: run git rev-parse --is-inside-work-tree to see why", gitCause(err))
	}
	top, pre, _ := strings.Cut(strings.TrimSuffix(out, "\n"), "\n")
	return filepath.Clean(top), pre, nil
}

// findProject locates the project and reads its config.
// The returned message is a complete "<cause>. Fix: <instruction>" refusal.
func findProject(ctx context.Context, env Env) (project, *string) {
	p := project{dir: env.Dir}
	root, prefix, err := repoLocation(ctx, env)
	if err != nil {
		msg := err.Error()
		return p, &msg
	}
	if root != "" {
		p.dir, p.prefix = root, prefix
	}
	raw, err := os.ReadFile(resolve(p.dir, defaultConfig))
	switch {
	case err == nil:
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		err := dec.Decode(&p.cfg)
		if err == nil {
			var extra any
			if next := dec.Decode(&extra); !errors.Is(next, io.EOF) {
				err = errors.New("expected exactly one YAML document")
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			msg := defaultConfig + " is not a valid june config (" + oneLine(err.Error()) + "). Fix: keep one YAML document whose keys are rules (a list of directories), base (a git ref) and exclude (a list of globs)"
			return p, &msg
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		msg := defaultConfig + " cannot be read. Fix: make it a readable file"
		return p, &msg
	}
	return p, nil
}

// rulesDirs is the rules directories for a run that named none: the config's, else .june/rules.
// A directory is spelled relative to env.Dir when the project is there, else absolute.
func (p project) rulesDirs(env Env) []string {
	dirs := p.cfg.Rules
	if len(dirs) == 0 {
		dirs = []string{defaultRules}
	}
	if p.dir == env.Dir {
		return dirs
	}
	out := make([]string, len(dirs))
	for i, d := range dirs {
		if d == "" {
			d = defaultRules
		}
		out[i] = resolve(p.dir, d)
	}
	return out
}

// excludes builds the filter that keeps paths out of the check: the config's globs, the request's own, and the narrowing to its paths.
// The returned message is a complete "<cause>. Fix: <instruction>" refusal.
func excludes(env Env, p project, req Request) (*excluder, *string) {
	ex := &excluder{}
	for _, g := range p.cfg.Exclude {
		seg, ok := parseGlob(g)
		if !ok {
			return nil, badGlob(g)
		}
		ex.rootGlobs = append(ex.rootGlobs, seg)
	}
	if p.prefix != "" {
		ex.prefix = strings.Split(strings.TrimSuffix(p.prefix, "/"), "/")
	}
	for _, g := range req.Exclude {
		seg, ok := parseGlob(g)
		if !ok {
			return nil, badGlob(g)
		}
		ex.globs = append(ex.globs, seg)
	}
	all := false
	for _, arg := range req.Paths {
		rel := filepath.ToSlash(arg)
		if filepath.IsAbs(arg) {
			r, err := filepath.Rel(env.Dir, arg)
			if err != nil {
				r = ".."
			}
			rel = filepath.ToSlash(r)
		}
		rel = path.Clean(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			msg := fmt.Sprintf("the path %q is outside the working directory. Fix: pass files or directories under the working directory, or run june from a directory that holds them", arg)
			return nil, &msg
		}
		if _, err := os.Stat(resolve(env.Dir, rel)); err != nil {
			msg := fmt.Sprintf("the path %q does not exist. Fix: pass an existing file or directory relative to the working directory", arg)
			return nil, &msg
		}
		if rel == "." {
			all = true
			continue
		}
		ex.only = append(ex.only, strings.Split(rel, "/"))
	}
	if all {
		ex.only = nil
	}
	return ex, nil
}

func badGlob(g string) *string {
	msg := fmt.Sprintf("the exclude pattern %q is not a valid glob. Fix: write a path glob relative to the working directory, such as vendor/** or **/*_pb2.py", g)
	return &msg
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// excluder is the set of exclude globs and the paths a run is narrowed to.
type excluder struct {
	globs     [][]string // from --exclude, relative to the working directory
	rootGlobs [][]string // from the config, relative to the directory that holds .june, which is prefix above the working directory
	prefix    []string
	only      [][]string // the paths a run is narrowed to, relative to the working directory; none means all
}

// active reports whether anything can keep a file out.
func (e *excluder) active() bool { return len(e.globs)+len(e.rootGlobs)+len(e.only) > 0 }

// outside reports whether narrowing to paths leaves file out.
func (e *excluder) outside(file string) bool {
	if len(e.only) == 0 {
		return false
	}
	segs := strings.Split(file, "/")
	for _, o := range e.only {
		if len(segs) >= len(o) && slices.Equal(segs[:len(o)], o) {
			return false
		}
	}
	return true
}

// matched reports whether an exclude glob keeps file out.
// file is slash-separated and relative to the working directory, so a sibling is spelled with "..".
func (e *excluder) matched(file string) bool {
	segs := strings.Split(file, "/")
	// A glob from --exclude is relative to the working directory, so it cannot name a path that leaves it.
	if !outsideWorkDir(file) {
		for n := 1; n <= len(segs); n++ {
			for _, g := range e.globs {
				if matchSegments(g, segs[:n]) {
					return true
				}
			}
		}
	}
	full := rootSegments(e.prefix, segs)
	for n := 1; n <= len(full); n++ {
		for _, g := range e.rootGlobs {
			if matchSegments(g, full[:n]) {
				return true
			}
		}
	}
	return false
}

// outsideWorkDir reports whether file, relative to the working directory, leaves that directory.
func outsideWorkDir(file string) bool {
	cleaned := path.Clean(file)
	return cleaned == ".." || strings.HasPrefix(cleaned, "../")
}

// rootSegments is file, relative to the working directory, resolved against the repository root.
func rootSegments(prefix, segs []string) []string {
	parts := append(slices.Clone(prefix), segs...)
	if len(parts) == 0 {
		return nil
	}
	cleaned := path.Clean(strings.Join(parts, "/"))
	if cleaned == "." {
		return nil
	}
	return strings.Split(cleaned, "/")
}

// fromRoot is a repository-root path relative to the working directory.
func (e *excluder) fromRoot(root string) string {
	if len(e.prefix) == 0 {
		return root
	}
	prefix := strings.Join(e.prefix, "/") + "/"
	if strings.HasPrefix(root, prefix) {
		return strings.TrimPrefix(root, prefix)
	}
	return strings.Repeat("../", len(e.prefix)) + root
}

// parseGlob splits a glob into slash-separated segments. A leading "./" and a trailing "/" mean nothing.
func parseGlob(g string) ([]string, bool) {
	g = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(g), "./"), "/")
	if g == "" || strings.HasPrefix(g, "/") {
		return nil, false
	}
	segs := strings.Split(g, "/")
	for _, s := range segs {
		if s == "" {
			return nil, false
		}
		if _, err := path.Match(s, ""); err != nil {
			return nil, false
		}
	}
	return segs, true
}

// excluded reports whether file, a slash-separated path relative to the working directory, is left out of the check.
// A glob that matches a directory excludes everything under it.
func (e *excluder) excluded(file string) bool { return e.outside(file) || e.matched(file) }

// matchSegments matches a glob against path segments: "**" is any number of whole segments, other segments follow path.Match.
func matchSegments(glob, segs []string) bool {
	for len(glob) > 0 {
		if glob[0] == "**" {
			for len(glob) > 1 && glob[1] == "**" {
				glob = glob[1:]
			}
			if len(glob) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(glob[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(glob[0], segs[0]); !ok {
			return false
		}
		glob, segs = glob[1:], segs[1:]
	}
	return len(segs) == 0
}

// filter splits files into those to check and the count excluded by a glob. A file outside the narrowed paths is dropped uncounted.
func (e *excluder) filter(files []string) ([]string, int) {
	if !e.active() {
		return files, 0
	}
	kept := make([]string, 0, len(files))
	n := 0
	for _, f := range files {
		switch {
		case e.outside(f):
		case e.matched(f):
			n++
		default:
			kept = append(kept, f)
		}
	}
	return kept, n
}
