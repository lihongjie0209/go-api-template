#!/bin/sh
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
render="$root/scripts/render-deployment-manifest.sh"
image='ghcr.io/lihongjie0209/go-api-template@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

for environment in production staging; do
	for kind in migration application; do
		output=$(sh "$render" "$kind" "$image" "$environment")
		printf '%s\n' "$output" | grep -Fq "image: $image"
		if printf '%s\n' "$output" | grep -Eq 'image: .*:sha-0000000|image: .*:main'; then
			echo "unresolved image in $environment $kind manifest" >&2
			exit 1
		fi
		if [ "$environment" = staging ]; then
			printf '%s\n' "$output" | grep -Eq '^[[:space:]]*namespace: microservices-staging[[:space:]]*$'
			if printf '%s\n' "$output" | grep -Eq '^[[:space:]]*(namespace: microservices|platform-environment: production|APP_ENV: production)[[:space:]]*$'; then
				echo "production identity remained in staging $kind manifest" >&2
				exit 1
			fi
		else
			printf '%s\n' "$output" | grep -Eq '^[[:space:]]*namespace: microservices[[:space:]]*$'
		fi
		if [ "$kind" = application ]; then
			printf '%s\n' "$output" | grep -Eq "^[[:space:]]*platform-environment: $environment[[:space:]]*$"
			printf '%s\n' "$output" | grep -Eq "^[[:space:]]*APP_ENV: $environment[[:space:]]*$"
			printf '%s\n' "$output" | grep -Eq '^[[:space:]]*fsGroup: 2000[[:space:]]*$'
			printf '%s\n' "$output" | grep -Eq '^[[:space:]]*defaultMode: 0440[[:space:]]*$'
		else
			printf '%s\n' "$output" | grep -Fq -- "-env, $environment,"
			printf '%s\n' "$output" | grep -Eq '^[[:space:]]*key: APP_MIGRATION_DATABASE_URL[[:space:]]*$'
		fi
	done
done

grep -Eq '^[[:space:]]*APP_MIGRATION_DATABASE_URL:' "$root/deployments/secret.example.yaml"

default_application=$(sh "$render" application "$image")
production_application=$(sh "$render" application "$image" production)
if [ "$default_application" != "$production_application" ]; then
	echo "default render is not production" >&2
	exit 1
fi

for bad in ghcr.io/lihongjie0209/go-api-template:main ghcr.io/lihongjie0209/go-api-template:sha-abcdef0 ghcr.io/example/other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; do
	if sh "$render" migration "$bad" >/dev/null 2>&1; then
		echo "accepted mutable image: $bad" >&2
		exit 1
	fi
done

if sh "$render" application "$image" unknown >/dev/null 2>&1; then
	echo "accepted an unknown deployment environment" >&2
	exit 1
fi
