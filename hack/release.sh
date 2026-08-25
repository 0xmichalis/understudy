#!/usr/bin/env bash
set -euo pipefail

version="${1:?usage: hack/release.sh <version, without leading v>}"
image="ghcr.io/kylan11/understudy"
chart_dir="charts/understudy"
chart_repo="oci://ghcr.io/kylan11/charts"

if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is not clean" >&2
  exit 1
fi

head_tag="$(git describe --tags --exact-match 2>/dev/null || true)"
if [ "$head_tag" != "v$version" ]; then
  echo "error: HEAD is not tagged v$version (got '${head_tag:-none}')" >&2
  exit 1
fi

chart_version="$(sed -n 's/^version: *//p' "$chart_dir/Chart.yaml")"
app_version="$(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$chart_dir/Chart.yaml")"
if [ "$chart_version" != "$version" ] || [ "$app_version" != "$version" ]; then
  echo "error: Chart.yaml has version=$chart_version appVersion=$app_version, expected $version" >&2
  exit 1
fi
./hack/check-chart-metadata.sh

docker buildx build --platform linux/amd64,linux/arm64 -t "$image:$version" --push .

mkdir -p dist
helm package "$chart_dir" --destination dist
helm push "dist/understudy-$version.tgz" "$chart_repo"
