#!/bin/sh
# End to end check of a vendor's behaviour with the real june binary on a fixture repository.
#
#   run.sh github   run under GitHub Actions; the runner's own environment is used. Prints the
#                   workflow commands the runner turns into annotations, and exits with june's code.
#   run.sh gitlab   run as a GitLab merge request pipeline would, with the variables GitLab sets,
#                   then check the Code Quality report. Used where no GitLab runner is available.
#
# JUNE is the binary to run (default: june on PATH). The fixture is a main branch with one clean
# file and a topic branch that adds a mutable default argument, so june must report one error on
# app.py line 2 and exit 1.
set -eu

vendor=${1:?usage: run.sh github|gitlab}
june=${JUNE:-june}
here=$(cd "$(dirname "$0")" && pwd)
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

git -C "$dir" init -q -b main
git -C "$dir" config user.name june
git -C "$dir" config user.email june@example.com
git -C "$dir" config commit.gpgsign false
mkdir -p "$dir/.june"
cp -R "$here/../rules/python" "$dir/.june/rules"
printf 'x = 1\n' > "$dir/app.py"
git -C "$dir" add -A
git -C "$dir" commit -q -m base
git -C "$dir" checkout -q -b topic
printf 'x = 1\ndef f(a=[]): pass\n' > "$dir/app.py"
git -C "$dir" commit -q -a -m change

out="$dir/out.txt"
case "$vendor" in
github)
	# On a real runner GITHUB_ACTIONS is already true and june picks the github format itself.
	if [ "${GITHUB_ACTIONS:-}" != true ]; then
		echo "run.sh github: not on a GitHub Actions runner, so there is nothing to check" >&2
		exit 2
	fi
	status=0
	(cd "$dir" && "$june" --base main) > "$out" || status=$?
	cat "$out"
	printf 'exit_code=%s\n' "$status" >> "$GITHUB_OUTPUT"
	exit "$status"
	;;
gitlab)
	status=0
	(cd "$dir" &&
		env -u GITHUB_ACTIONS GITLAB_CI=true CI_MERGE_REQUEST_EVENT_TYPE= \
			CI_MERGE_REQUEST_DIFF_BASE_SHA="$(git rev-parse main)" "$june") > "$out" || status=$?
	cat "$out"
	[ "$status" = 1 ] || { echo "run.sh: exit $status, want 1" >&2; exit 1; }
	report="$dir/gl-code-quality-report.json"
	test -f "$report" || { echo "run.sh: no gl-code-quality-report.json" >&2; exit 1; }
	jq -e '
		length == 1 and
		.[0].check_name == "PY012" and
		.[0].severity == "major" and
		.[0].location.path == "app.py" and
		.[0].location.lines.begin == 2 and
		(.[0].description | startswith("Mutable default argument")) and
		(.[0].fingerprint | test("^[0-9a-f]{64}$"))
	' "$report" > /dev/null || { echo "run.sh: Code Quality report is wrong:" >&2; cat "$report" >&2; exit 1; }
	echo "run.sh gitlab: ok"
	;;
*)
	echo "usage: run.sh github|gitlab" >&2
	exit 2
	;;
esac
