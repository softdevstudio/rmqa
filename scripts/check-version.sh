#!/usr/bin/env bash
# VERSION is the one place the version is set; the code and the changelog
# must agree with it. Prints ::error:: lines so CI annotations show why.
set -euo pipefail
cd "$(dirname "$0")/.."
v=$(tr -d '[:space:]' < VERSION)
go_v=$(sed -n 's/^const Version = "\(.*\)"/\1/p' cmd/rmqa/main.go)
fail=0
[ "$go_v" = "$v" ] || { echo "::error::cmd/rmqa/main.go has $go_v, VERSION has $v"; fail=1; }
grep -q "^## $v" CHANGELOG.md || { echo "::error::CHANGELOG.md has no section for $v"; fail=1; }
[ $fail = 0 ] && echo "version $v everywhere"
exit $fail
