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
grep -q 'run: make ci-test-integration' .github/workflows/ci.yml
