#!/bin/sh
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
render="$root/scripts/render-production-manifest.sh"
image='ghcr.io/lihongjie0209/go-api-template@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

for kind in migration application; do
	output=$(sh "$render" "$kind" "$image")
	printf '%s\n' "$output" | grep -Fq "image: $image"
	if printf '%s\n' "$output" | grep -Eq 'image: .*:sha-0000000|image: .*:main'; then
		echo "unresolved image in $kind manifest" >&2
		exit 1
	fi
done

for bad in ghcr.io/lihongjie0209/go-api-template:main ghcr.io/lihongjie0209/go-api-template:sha-abcdef0 ghcr.io/example/other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; do
	if sh "$render" migration "$bad" >/dev/null 2>&1; then
		echo "accepted mutable image: $bad" >&2
		exit 1
	fi
done
