#!/bin/sh
set -eu

local_command=$(make -s -n test-integration)
ci_command=$(make -s -n ci-test-integration)

case "$local_command" in
	*"-tags=integration"*"-run '^$'"*) ;;
	*) echo "test-integration must compile without running containers" >&2; exit 1 ;;
esac
case "$ci_command" in
	*"-tags=integration"*"-count=1"*) ;;
	*) echo "ci-test-integration must execute the integration suite" >&2; exit 1 ;;
esac
find . -type f -name '*_test.go' -exec grep -l '^//go:build integration$' {} + | while IFS= read -r file; do
	package=$(dirname "$file")
	case "$package" in
		./integration|./integration/*) package=./integration/... ;;
	esac
	case "$local_command" in
		*"$package"*) ;;
		*) echo "test-integration must compile $package (from $file)" >&2; exit 1 ;;
	esac
	case "$ci_command" in
		*"$package"*) ;;
		*) echo "ci-test-integration must execute $package (from $file)" >&2; exit 1 ;;
	esac
done
grep -q 'run: make ci-test-integration' .github/workflows/ci.yml
