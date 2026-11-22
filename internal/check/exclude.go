package check

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultConfig = ".june/config.yml"

// config is the content of .june/config.yml. An unknown key is an error, so nothing in it is silently dropped.
type config struct {
	Exclude []string `yaml:"exclude"`
}

// excludes loads the globs that keep paths out of the check: the config file in env.Dir, if any, then the request's own.
// The returned message is a complete "<cause>. Fix: <instruction>" refusal.
func excludes(env Env, req Request) (*excluder, *string) {
	var globs []string
	cfgPath := resolve(env.Dir, defaultConfig)
	raw, err := os.ReadFile(cfgPath)
	switch {
	case err == nil:
		var cfg config
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			msg := defaultConfig + " is not a valid june config (" + oneLine(err.Error()) + "). Fix: keep one YAML document whose only key is exclude, a list of globs"
			return nil, &msg
		}
		globs = append(globs, cfg.Exclude...)
	case errors.Is(err, fs.ErrNotExist):
	default:
		msg := defaultConfig + " cannot be read. Fix: make it a readable file"
		return nil, &msg
	}
	globs = append(globs, req.Exclude...)
	ex := &excluder{}
	for _, g := range globs {
		p, ok := parseGlob(g)
		if !ok {
			msg := fmt.Sprintf("the exclude pattern %q is not a valid glob. Fix: write a path glob relative to the working directory, such as vendor/** or **/*_pb2.py", g)
			return nil, &msg
		}
		ex.globs = append(ex.globs, p)
	}
	return ex, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// excluder is the set of exclude globs.
type excluder struct{ globs [][]string }

// parseGlob splits a glob into slash-separated segments. A leading "./" and a trailing "/" mean nothing.
func parseGlob(g string) ([]string, bool) {
	g = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./"), "/")
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

// excluded reports whether file, a slash-separated path relative to the working directory, is excluded.
// A glob that matches a directory excludes everything under it.
func (e *excluder) excluded(file string) bool {
	segs := strings.Split(file, "/")
	for _, g := range e.globs {
		for n := 1; n <= len(segs); n++ {
			if matchSegments(g, segs[:n]) {
				return true
			}
		}
	}
	return false
}

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

// filter splits files into those to check and the count of those excluded.
func (e *excluder) filter(files []string) ([]string, int) {
	if len(e.globs) == 0 {
		return files, 0
	}
	kept := make([]string, 0, len(files))
	for _, f := range files {
		if !e.excluded(f) {
			kept = append(kept, f)
		}
	}
	return kept, len(files) - len(kept)
}
