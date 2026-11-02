#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 migration|application image@sha256:digest" >&2
	exit 2
fi

case "$1" in
	migration) file=migrate-job.yaml ;;
	application) file=kubernetes.yaml ;;
	*) echo "unknown manifest: $1" >&2; exit 2 ;;
esac

image=$2
if ! printf '%s\n' "$image" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$'; then
	echo "a pinned sha256 image reference is required" >&2
	exit 2
fi

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
manifest="$root/deployments/$file"
count=$(grep -Ec '^[[:space:]]*image: [^[:space:]]+:sha-0000000[[:space:]]*$' "$manifest" || true)
if [ "$count" -ne 1 ]; then
	echo "expected exactly one image placeholder in $file" >&2
	exit 1
fi
repository=$(sed -nE 's#^[[:space:]]*image: ([[:graph:]]+):sha-0000000[[:space:]]*$#\1#p' "$manifest")
if [ "${image%@sha256:*}" != "$repository" ]; then
	echo "image repository does not match $file" >&2
	exit 2
fi

sed -E "s#^([[:space:]]*image: )[[:graph:]]+:sha-0000000[[:space:]]*#\\1$image#" "$manifest"
