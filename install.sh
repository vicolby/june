#!/bin/sh
# Install the june release binary and its bundled rules.
#
#   curl -fsSL https://raw.githubusercontent.com/vicolby/june/main/install.sh | sh
#
# Environment:
#   JUNE_VERSION           Release to install, with or without the leading "v". Default: latest.
#   JUNE_INSTALL_DIR       Where the june binary goes. Default: /usr/local/bin as root,
#                          $HOME/.local/bin otherwise.
#   JUNE_RULES_DIR         Where the bundled rules go. Default: <prefix>/share/june/rules, with the
#                          prefix /usr/local as root and $HOME/.local otherwise.
#   JUNE_OS, JUNE_ARCH     Override the detected platform (linux or darwin, amd64 or arm64).
#   JUNE_INSTALL_AST_GREP  Set to 1 to also run `npm install --global @ast-grep/cli`.
#   JUNE_RELEASES_URL      Release base URL. Default: https://github.com/vicolby/june/releases.
#
# In CI set JUNE_VERSION and fetch this script from a release tag, not main.
# The script downloads the archive and checksums.txt, verifies the sha256 and only then installs.

set -eu

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
	if [ "$(id -u)" = 0 ]; then
		prefix=/usr/local
	else
		[ -n "${HOME:-}" ] || fail "HOME is not set (set JUNE_INSTALL_DIR and JUNE_RULES_DIR)"
		prefix=$HOME/.local
	fi
	install_dir=${JUNE_INSTALL_DIR:-$prefix/bin}
	rules_dir=${JUNE_RULES_DIR:-$prefix/share/june/rules}

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

	want=$(awk -v f="$archive" '$2 == f { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail "checksums.txt has no entry for $archive"
	got=$(sha256 "$tmp/$archive")
	[ "$got" = "$want" ] || fail "checksum mismatch for $archive: expected $want, got $got"

	mkdir "$tmp/extract"
	tar -xzf "$tmp/$archive" -C "$tmp/extract" || fail "could not extract $archive"
	[ -f "$tmp/extract/june" ] || fail "$archive does not contain a june binary"
	[ -d "$tmp/extract/rules" ] || fail "$archive does not contain a rules directory"

	mkdir -p "$install_dir" || fail "could not create $install_dir (set JUNE_INSTALL_DIR)"
	mkdir -p "$(dirname "$rules_dir")" || fail "could not create $(dirname "$rules_dir") (set JUNE_RULES_DIR)"
	rm -rf "$rules_dir"
	cp -R "$tmp/extract/rules" "$rules_dir" || fail "could not write $rules_dir (set JUNE_RULES_DIR)"
	cp "$tmp/extract/june" "$install_dir/june" || fail "could not write $install_dir/june (set JUNE_INSTALL_DIR)"
	chmod 755 "$install_dir/june"

	if [ "${JUNE_INSTALL_AST_GREP:-}" = 1 ]; then
		need npm
		say "Installing ast-grep"
		npm install --global @ast-grep/cli || fail "npm install of @ast-grep/cli failed"
	fi

	installed=$("$install_dir/june" --version) || fail "$install_dir/june --version failed"
	say "$installed"
	say "june installed to $install_dir/june"
	say "rules installed to $rules_dir (use --rules $rules_dir/python)"

	case ":${PATH:-}:" in
	*":$install_dir:"*) ;;
	*) say "note: $install_dir is not on PATH" ;;
	esac
	if ! command -v ast-grep >/dev/null 2>&1; then
		say "note: ast-grep is not on PATH; june needs it (rerun with JUNE_INSTALL_AST_GREP=1 or npm install --global @ast-grep/cli)"
	fi
}

main "$@"
