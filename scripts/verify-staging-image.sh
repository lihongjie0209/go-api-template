#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 image@sha256:digest" >&2
	exit 2
fi

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
image=$1
sh "$root/scripts/render-deployment-manifest.sh" application "$image" staging >/dev/null

kubectl --namespace microservices-staging rollout status deployment/go-api-template --timeout=5m >/dev/null
actual=$(kubectl --namespace microservices-staging get deployment go-api-template -o jsonpath='{.spec.template.spec.containers[0].image}')
if [ "$actual" != "$image" ]; then
	echo "staging deployment does not use the requested image digest" >&2
	exit 1
fi
profile=$(kubectl --namespace microservices-staging get configmap go-api-template -o jsonpath='{.data.APP_ENV}')
if [ "$profile" != staging ]; then
	echo "staging ConfigMap does not select the staging profile" >&2
	exit 1
fi
label=$(kubectl get namespace microservices-staging -o jsonpath='{.metadata.labels.platform-environment}')
if [ "$label" != staging ]; then
	echo "staging Namespace does not have the staging environment label" >&2
	exit 1
fi
