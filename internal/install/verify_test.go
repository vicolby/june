package install

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallVerifiedRelease(t *testing.T) {
	const identity = "https://github.com/vicolby/june/.github/workflows/release.yml@refs/tags/v0.2.0"
	archive := fixtureArchive(t)
	digest := sum(archive)
	for _, tool := range []string{"gh", "cosign"} {
		for _, tc := range []struct {
			name, os, arch, version, signedIdentity, signedRepo, subjects, checksumName string
			verifyError, reject                                                         bool
		}{
			{name: "linux amd64", os: "linux", arch: "amd64", version: "0.2.0"},
			{name: "linux arm64", os: "linux", arch: "arm64", version: "v0.2.0"},
			{name: "darwin amd64", os: "darwin", arch: "amd64", version: "latest"},
			{name: "darwin arm64", os: "darwin", arch: "arm64"},
			{name: "older release", signedIdentity: strings.Replace(identity, "v0.2.0", "v0.1.9", 1), reject: true},
			{name: "tag suffix", signedIdentity: identity + "-other", reject: true},
			{name: "other workflow", signedIdentity: strings.Replace(identity, "release.yml", "ci.yml", 1), reject: true},
			{name: "other repository", signedIdentity: strings.Replace(identity, "vicolby/june", "attacker/june", 1), signedRepo: "attacker/june", reject: true},
			{name: "verification failure", verifyError: true, reject: true},
			{name: "checksum names another version", checksumName: "june_0.1.9_linux_amd64.tar.gz", reject: true},
			{name: "checksum names another os", checksumName: "june_0.2.0_darwin_amd64.tar.gz", reject: true},
			{name: "checksum names another arch", checksumName: "june_0.2.0_linux_arm64.tar.gz", reject: true},
			{name: "subject names another version", subjects: "june_0.1.9_linux_amd64.tar.gz\t" + digest, reject: true},
			{name: "subject names another os", subjects: "june_0.2.0_darwin_amd64.tar.gz\t" + digest, reject: true},
			{name: "subject names another arch", subjects: "june_0.2.0_linux_arm64.tar.gz\t" + digest, reject: true},
			{name: "subject has wrong digest", subjects: "june_0.2.0_linux_amd64.tar.gz\t" + strings.Repeat("0", 64), reject: true},
			{name: "name and digest belong to different subjects", subjects: "june_0.2.0_linux_amd64.tar.gz\t" + strings.Repeat("0", 64) + "\njune_0.1.9_linux_amd64.tar.gz\t" + digest, reject: true},
			{name: "no subjects", subjects: "\n", reject: true},
			{name: "multiple subjects", subjects: "june_0.1.9_linux_amd64.tar.gz\t" + digest + "\njune_0.2.0_linux_amd64.tar.gz\t" + digest},
		} {
			if tool == "cosign" && tc.subjects != "" {
				continue
			}
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				if tc.os == "" {
					tc.os = "linux"
				}
				if tc.arch == "" {
					tc.arch = "amd64"
				}
				if tc.signedIdentity == "" {
					tc.signedIdentity = identity
				}
				if tc.signedRepo == "" {
					tc.signedRepo = "vicolby/june"
				}
				name := fmt.Sprintf("june_0.2.0_%s_%s.tar.gz", tc.os, tc.arch)
				if tc.subjects == "" {
					tc.subjects = name + "\t" + digest
				}
				if tc.checksumName == "" {
					tc.checksumName = name
				}
				mux := http.NewServeMux()
				mux.HandleFunc("/download/v0.2.0/"+name, func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write(archive)
				})
				mux.HandleFunc("/download/v0.2.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, "%s  %s\n", digest, tc.checksumName)
				})
				mux.HandleFunc("/download/v0.2.0/checksums.txt.sigstore.json", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte("{}"))
				})
				mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "/tag/v0.2.0", http.StatusFound)
				})
				mux.HandleFunc("/tag/v0.2.0", func(http.ResponseWriter, *http.Request) {})
				srv := httptest.NewServer(mux)
				defer srv.Close()
				bin, dir := t.TempDir(), t.TempDir()
				verifier := `#!/bin/sh
set -eu
[ "$VERIFY_ERROR" = false ] || exit 1
case "$1" in
attestation) test "$2" = verify; test -s "$3"; shift 3 ;;
verify-blob) shift ;;
*) exit 1 ;;
esac
while [ "$#" -gt 0 ]; do
  case "$1" in
  --repo) test "$2" = "$SIGNED_REPO"; shift 2 ;;
  --cert-identity|--certificate-identity) test "$2" = "$SIGNED_IDENTITY"; shift 2 ;;
  --source-ref) test "$2" = refs/tags/v0.2.0; shift 2 ;;
  --certificate-identity-regexp) printf '%s\n' "$SIGNED_IDENTITY" | grep -E "$2" >/dev/null; shift 2 ;;
  --certificate-oidc-issuer) test "$2" = https://token.actions.githubusercontent.com; shift 2 ;;
  --bundle) test -s "$2"; shift 2 ;;
  --format) test "$2" = json; shift 2 ;;
  --jq) shift 2 ;;
  *) test -s "$1"; shift ;;
  esac
done
printf '%s\n' "$VERIFIED_SUBJECTS"
`
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(verifier), 0o755); err != nil {
					t.Fatal(err)
				}
				r := runInstall(t,
					"PATH="+bin+":/usr/bin:/bin", "JUNE_RELEASES_URL="+srv.URL,
					"JUNE_INSTALL_DIR="+dir, "JUNE_VERSION="+tc.version, "JUNE_OS="+tc.os, "JUNE_ARCH="+tc.arch,
					"JUNE_VERIFY=1", "JUNE_VERIFY_REPO=attacker/june", "SIGNED_REPO="+tc.signedRepo,
					"SIGNED_IDENTITY="+tc.signedIdentity, "VERIFIED_SUBJECTS="+tc.subjects,
					fmt.Sprintf("VERIFY_ERROR=%t", tc.verifyError),
				)
				if tc.reject {
					if r.err == nil {
						t.Fatalf("untrusted release installed: %s", r.stdout)
					}
					mustNotExist(t, filepath.Join(dir, "june"))
					return
				}
				if r.err != nil {
					t.Fatalf("trusted release rejected: %v\n%s\n%s", r.err, r.stdout, r.stderr)
				}
				got, err := os.ReadFile(filepath.Join(dir, "june"))
				if err != nil || string(got) != fakeJune {
					t.Fatalf("installed binary = %q, %v", got, err)
				}
			})
		}
	}
}

func TestInstallVerificationNeedsVerifier(t *testing.T) {
	archive := fixtureArchive(t)
	srv := releaseServer(t, archive, sum(archive))
	dir := t.TempDir()
	r := runInstall(t, "JUNE_RELEASES_URL="+srv.URL, "JUNE_INSTALL_DIR="+dir, "JUNE_VERSION=0.2.0", "JUNE_VERIFY=1")
	if r.err == nil || !strings.Contains(r.stderr, "JUNE_VERIFY=1 needs gh or cosign on PATH") {
		t.Fatalf("missing verifier: %v\n%s\n%s", r.err, r.stdout, r.stderr)
	}
	mustNotExist(t, filepath.Join(dir, "june"))
}
