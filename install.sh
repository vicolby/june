#!/bin/sh
# Install the june release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/vicolby/june/main/install.sh | sh
#
# Environment:
#   JUNE_VERSION           Release to install, with or without the leading "v". Default: latest.
#   JUNE_INSTALL_DIR       Where the june binary goes. Default: /usr/local/bin as root,
#                          $HOME/.local/bin otherwise.
#   JUNE_OS, JUNE_ARCH     Override the detected platform (linux or darwin, amd64 or arm64).
#   JUNE_INSTALL_AST_GREP  Set to 1 to also install the tested ast-grep with
#                          `npm install --global @ast-grep/cli@$ast_grep_version`.
#   JUNE_VERIFY            Set to 1 to also verify the release is signed by this repository's release
#                          workflow, with `gh attestation verify` or else cosign. Fails if neither is
#                          installed or verification fails. Default: sha256 check only.
#   JUNE_RELEASES_URL      Release base URL. Default: https://github.com/vicolby/june/releases.
#
# In CI set JUNE_VERSION and fetch this script from a release tag, not main.
# The script downloads the archive and checksums.txt, verifies the sha256 and only then installs.
# The sha256 check catches corruption only: checksums.txt comes from the same place as the archive.
# JUNE_VERIFY=1 adds a signature check that a compromised release cannot forge.

set -eu

# The ast-grep release june is tested against (same as .ast-grep-version). june refuses anything older than its floor.
ast_grep_version=0.51.3

say() {
	printf '%s\n' "$*"
}

fail() {
	printf 'install.sh: error: %s\n' "$*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

# The whole installer is one function called on the last line, so a download cut short
# defines functions at most and never runs a partial install.
main() {
	releases_url=${JUNE_RELEASES_URL:-https://github.com/vicolby/june/releases}
	releases_url=${releases_url%/}

	need curl
	need tar
	need uname

	# Platform.
	os=${JUNE_OS:-}
	if [ -z "$os" ]; then
		case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) fail "unsupported operating system: $(uname -s) (set JUNE_OS to linux or darwin)" ;;
		esac
	fi
	arch=${JUNE_ARCH:-}
	if [ -z "$arch" ]; then
		case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		arm64 | aarch64) arch=arm64 ;;
		*) fail "unsupported architecture: $(uname -m) (set JUNE_ARCH to amd64 or arm64)" ;;
		esac
	fi
	case "$os" in
	linux | darwin) ;;
	*) fail "unsupported operating system: $os (releases cover linux and darwin)" ;;
	esac
	case "$arch" in
	amd64 | arm64) ;;
	*) fail "unsupported architecture: $arch (releases cover amd64 and arm64)" ;;
	esac

	# Version. "latest" follows the releases/latest redirect, so it needs no API token or JSON parser.
	version=${JUNE_VERSION:-latest}
	if [ "$version" = latest ]; then
		latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$releases_url/latest") ||
			fail "could not resolve the latest release from $releases_url/latest (set JUNE_VERSION)"
		version=${latest_url##*/}
		case "$version" in
		v[0-9]*) ;;
		*) fail "could not read a release version from $latest_url (set JUNE_VERSION)" ;;
		esac
	fi
	version=${version#v}
	[ -n "$version" ] || fail "empty JUNE_VERSION"

	# Install locations.
	# HOME only matters when a default path derived from it is needed.
	if [ -n "${JUNE_INSTALL_DIR:-}" ]; then
		install_dir=$JUNE_INSTALL_DIR
	elif [ "$(id -u)" = 0 ]; then
		install_dir=/usr/local/bin
	else
		[ -n "${HOME:-}" ] || fail "HOME is not set (set JUNE_INSTALL_DIR)"
		install_dir=$HOME/.local/bin
	fi

	if command -v sha256sum >/dev/null 2>&1; then
		sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
	elif command -v shasum >/dev/null 2>&1; then
		sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
	else
		fail "required command not found: sha256sum or shasum"
	fi

	tmp=$(mktemp -d "${TMPDIR:-/tmp}/june-install.XXXXXX") || fail "could not create a temporary directory"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	archive="june_${version}_${os}_${arch}.tar.gz"
	base="$releases_url/download/v${version}"

	say "Downloading june $version for $os/$arch"
	curl -fsSL -o "$tmp/$archive" "$base/$archive" ||
		fail "could not download $base/$archive (check that release v$version exists for $os/$arch)"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" ||
		fail "could not download $base/checksums.txt"
	got=$(sha256 "$tmp/$archive")

	if [ "${JUNE_VERIFY:-}" = 1 ]; then
		release_identity="https://github.com/vicolby/june/.github/workflows/release.yml@refs/tags/v${version}"
		if command -v gh >/dev/null 2>&1; then
			say "Verifying build provenance with gh"
			subjects=$(gh attestation verify "$tmp/$archive" --repo vicolby/june \
				--cert-identity "$release_identity" --source-ref "refs/tags/v${version}" \
				--format json --jq '.[].verificationResult.statement.subject[] | [.name, .digest.sha256] | @tsv') ||
				fail "gh attestation verify failed for $archive (expected $release_identity)"
			printf '%s\n' "$subjects" | awk -v f="$archive" -v d="$got" \
				'$1 == f && $2 == d { found = 1 } END { exit !found }' ||
				fail "verified provenance has no matching name and digest for $archive"
		elif command -v cosign >/dev/null 2>&1; then
			say "Verifying checksums.txt signature with cosign"
			curl -fsSL -o "$tmp/checksums.txt.sigstore.json" "$base/checksums.txt.sigstore.json" ||
				fail "could not download $base/checksums.txt.sigstore.json"
			cosign verify-blob \
				--bundle "$tmp/checksums.txt.sigstore.json" \
				--certificate-identity "$release_identity" \
				--certificate-oidc-issuer https://token.actions.githubusercontent.com \
				"$tmp/checksums.txt" >/dev/null 2>&1 ||
				fail "cosign could not verify the signature of checksums.txt (expected $release_identity)"
		else
			fail "JUNE_VERIFY=1 needs gh or cosign on PATH"
		fi
	fi

	want=$(awk -v f="$archive" '$2 == f { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail "checksums.txt has no entry for $archive"
	[ "$got" = "$want" ] || fail "checksum mismatch for $archive: expected $want, got $got"

	mkdir "$tmp/extract"
	tar -xzf "$tmp/$archive" -C "$tmp/extract" || fail "could not extract $archive"
	[ -f "$tmp/extract/june" ] || fail "$archive does not contain a june binary"

	mkdir -p "$install_dir" || fail "could not create $install_dir (set JUNE_INSTALL_DIR)"
	cp "$tmp/extract/june" "$install_dir/june" || fail "could not write $install_dir/june (set JUNE_INSTALL_DIR)"
	chmod 755 "$install_dir/june"

	if [ "${JUNE_INSTALL_AST_GREP:-}" = 1 ]; then
		need npm
		say "Installing ast-grep"
		npm install --global "@ast-grep/cli@$ast_grep_version" || fail "npm install of @ast-grep/cli@$ast_grep_version failed"
	fi

	installed=$("$install_dir/june" --version) || fail "$install_dir/june --version failed"
	say "$installed"
	say "june installed to $install_dir/june"

	case ":${PATH:-}:" in
	*":$install_dir:"*) ;;
	*) say "note: $install_dir is not on PATH" ;;
	esac
	if ! command -v ast-grep >/dev/null 2>&1; then
		say "note: ast-grep is not on PATH; june needs it (rerun with JUNE_INSTALL_AST_GREP=1 or npm install --global @ast-grep/cli@$ast_grep_version)"
	fi
}

main "$@"
