package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeJune = "#!/bin/sh\necho 'june 0.2.0'\n"

// fixtureArchive builds a release archive like goreleaser's: june and rules/python/LST01.yml.
func fixtureArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := []struct {
		name, body string
		mode       int64
	}{
		{"june", fakeJune, 0o755},
		{"rules/python/LST01.yml", "id: LST01\n", 0o644},
	}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// releaseServer serves one release (v0.2.0, linux/amd64) the way GitHub does.
// checksum is the hash listed in checksums.txt; "" lists no entry for the archive.
func releaseServer(t *testing.T, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	const name = "june_0.2.0_linux_amd64.tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/download/v0.2.0/"+name, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/download/v0.2.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		body := "0000  june_0.2.0_darwin_arm64.tar.gz\n"
		if checksum != "" {
			body += checksum + "  " + name + "\n"
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/v0.2.0", http.StatusFound)
	})
	mux.HandleFunc("/tag/v0.2.0", func(w http.ResponseWriter, _ *http.Request) {})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type result struct {
	stdout, stderr string
	err            error
	home           string
}

// runInstall runs install.sh with a clean environment. env entries override the defaults.
func runInstall(t *testing.T, env ...string) result {
	t.Helper()
	script, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "sh", script)
	cmd.Env = append([]string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"TMPDIR=" + t.TempDir(),
		"JUNE_OS=linux",
		"JUNE_ARCH=amd64",
	}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return result{out.String(), errOut.String(), err, home}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("want %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("want %s absent", path)
	}
}

func TestInstall(t *testing.T) {
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))

	for _, version := range []string{"0.2.0", "v0.2.0", "latest", ""} {
		t.Run("version="+version, func(t *testing.T) {
			dir := t.TempDir()
			env := []string{"JUNE_RELEASES_URL=" + srv.URL, "JUNE_INSTALL_DIR=" + dir + "/bin", "JUNE_RULES_DIR=" + dir + "/rules"}
			if version != "" {
				env = append(env, "JUNE_VERSION="+version)
			}
			r := runInstall(t, env...)
			if r.err != nil {
				t.Fatalf("install failed: %v\nstdout: %s\nstderr: %s", r.err, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, "june 0.2.0\n") {
				t.Errorf("stdout = %q, want the installed version", r.stdout)
			}
			got, err := os.ReadFile(dir + "/bin/june")
			if err != nil || string(got) != fakeJune {
				t.Errorf("binary = %q, %v", got, err)
			}
			if fi, err := os.Stat(dir + "/bin/june"); err == nil && fi.Mode().Perm()&0o111 == 0 {
				t.Errorf("binary mode = %v, want executable", fi.Mode())
			}
			mustExist(t, dir+"/rules/python/LST01.yml")
		})
	}
}

func TestInstallReplacesStaleRules(t *testing.T) {
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	dir := t.TempDir()
	stale := dir + "/rules/python/OLD.yml"
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := runInstall(t, "JUNE_RELEASES_URL="+srv.URL, "JUNE_VERSION=0.2.0", "JUNE_INSTALL_DIR="+dir+"/bin", "JUNE_RULES_DIR="+dir+"/rules")
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.stderr)
	}
	mustNotExist(t, stale)
	mustExist(t, dir+"/rules/python/LST01.yml")
}

func TestInstallDefaultDirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root installs under /usr/local")
	}
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	r := runInstall(t, "JUNE_RELEASES_URL="+srv.URL, "JUNE_VERSION=0.2.0")
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.stderr)
	}
	mustExist(t, r.home+"/.local/bin/june")
	mustExist(t, r.home+"/.local/share/june/rules/python/LST01.yml")
	if !strings.Contains(r.stdout, "is not on PATH") {
		t.Errorf("stdout = %q, want a PATH note", r.stdout)
	}
}

func TestInstallWithoutHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root does not derive its install paths from HOME")
	}
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))

	t.Run("both dirs set", func(t *testing.T) {
		dir := t.TempDir()
		r := runInstall(t, "HOME=", "JUNE_RELEASES_URL="+srv.URL, "JUNE_VERSION=0.2.0", "JUNE_INSTALL_DIR="+dir+"/bin", "JUNE_RULES_DIR="+dir+"/rules")
		if r.err != nil {
			t.Fatalf("install failed: %v\n%s", r.err, r.stderr)
		}
		mustExist(t, dir+"/bin/june")
		mustExist(t, dir+"/rules/python/LST01.yml")
	})

	for _, tt := range []struct {
		name string
		set  func(dir string) []string
	}{
		{"neither dir set", func(string) []string { return nil }},
		{"only install dir set", func(dir string) []string { return []string{"JUNE_INSTALL_DIR=" + dir + "/bin"} }},
		{"only rules dir set", func(dir string) []string { return []string{"JUNE_RULES_DIR=" + dir + "/rules"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			env := append([]string{"HOME=", "JUNE_RELEASES_URL=" + srv.URL, "JUNE_VERSION=0.2.0"}, tt.set(dir)...)
			r := runInstall(t, env...)
			if r.err == nil {
				t.Fatalf("install succeeded, want failure\nstdout: %s", r.stdout)
			}
			if want := "HOME is not set (set JUNE_INSTALL_DIR and JUNE_RULES_DIR)"; !strings.Contains(r.stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
			}
			mustNotExist(t, dir+"/bin/june")
			mustNotExist(t, dir+"/rules")
		})
	}
}

func TestInstallFailures(t *testing.T) {
	archive := fixtureArchive(t)
	good := releaseServer(t, archive, sum(archive))
	badSum := releaseServer(t, archive, strings.Repeat("a", 64))
	noEntry := releaseServer(t, archive, "")

	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"bad checksum", []string{"JUNE_RELEASES_URL=" + badSum.URL, "JUNE_VERSION=0.2.0"}, "checksum mismatch"},
		{"no checksum entry", []string{"JUNE_RELEASES_URL=" + noEntry.URL, "JUNE_VERSION=0.2.0"}, "no entry for june_0.2.0_linux_amd64.tar.gz"},
		{"unsupported os", []string{"JUNE_RELEASES_URL=" + good.URL, "JUNE_VERSION=0.2.0", "JUNE_OS=windows"}, "unsupported operating system: windows"},
		{"unsupported arch", []string{"JUNE_RELEASES_URL=" + good.URL, "JUNE_VERSION=0.2.0", "JUNE_ARCH=riscv64"}, "unsupported architecture: riscv64"},
		{"missing version", []string{"JUNE_RELEASES_URL=" + good.URL, "JUNE_VERSION=9.9.9"}, "could not download"},
		{"latest unresolvable", []string{"JUNE_RELEASES_URL=" + good.URL + "/nope"}, "could not resolve the latest release"},
		{"no network tools", []string{"JUNE_RELEASES_URL=" + good.URL, "JUNE_VERSION=0.2.0", "PATH=/nonexistent"}, "required command not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			env := append([]string{"JUNE_INSTALL_DIR=" + dir + "/bin", "JUNE_RULES_DIR=" + dir + "/rules"}, tt.env...)
			r := runInstall(t, env...)
			if r.err == nil {
				t.Fatalf("install succeeded, want failure\nstdout: %s", r.stdout)
			}
			if !strings.Contains(r.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, tt.want)
			}
			mustNotExist(t, dir+"/bin/june")
			mustNotExist(t, dir+"/rules")
		})
	}
}

func TestInstallAstGrepOptIn(t *testing.T) {
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	npm := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\n", log)
	if err := os.WriteFile(bin+"/npm", []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	base := []string{"PATH=" + bin + ":/usr/bin:/bin", "JUNE_RELEASES_URL=" + srv.URL, "JUNE_VERSION=0.2.0", "JUNE_INSTALL_DIR=" + dir + "/bin", "JUNE_RULES_DIR=" + dir + "/rules"}

	r := runInstall(t, base...)
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.stderr)
	}
	mustNotExist(t, log)

	r = runInstall(t, append(base, "JUNE_INSTALL_AST_GREP=1")...)
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.stderr)
	}
	got, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(got)) != "install --global @ast-grep/cli" {
		t.Errorf("npm calls = %q, %v", got, err)
	}
}

// A download cut short must not install anything: the body is one function called on the last line.
func TestTruncatedScriptInstallsNothing(t *testing.T) {
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	full, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, cut := range []int{len(full) / 4, len(full) / 2, len(full) - 12} {
		cmd := exec.CommandContext(context.Background(), "sh")
		cmd.Stdin = bytes.NewReader(full[:cut])
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "JUNE_RELEASES_URL=" + srv.URL, "JUNE_VERSION=0.2.0", "JUNE_OS=linux", "JUNE_ARCH=amd64", "JUNE_INSTALL_DIR=" + dir + "/bin", "JUNE_RULES_DIR=" + dir + "/rules"}
		// A cut inside main is a syntax error; a cut just before the call defines functions and exits 0.
		// Either way nothing is installed.
		_ = cmd.Run()
		mustNotExist(t, dir+"/bin/june")
		mustNotExist(t, dir+"/rules")
	}
}
