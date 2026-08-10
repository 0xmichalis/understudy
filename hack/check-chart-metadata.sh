#!/usr/bin/env bash
set -euo pipefail

chart="charts/understudy/Chart.yaml"
image_repo="ghcr.io/kylan11/understudy"

app_version="$(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$chart")"
pinned="$(sed -n "s|^ *image: ${image_repo}:\(.*\) *$|\1|p" "$chart")"

status=0

if [ -z "$app_version" ]; then
  echo "error: could not read appVersion from $chart" >&2
  status=1
fi

if [ -z "$pinned" ]; then
  echo "error: could not read the artifacthub.io/images tag from $chart" >&2
  status=1
fi

if [ "$status" -eq 0 ] && [ "$app_version" != "$pinned" ]; then
  echo "error: artifacthub.io/images pins ${image_repo}:${pinned} but appVersion is ${app_version}" >&2
  echo "hint: update the artifacthub.io/images annotation, and refresh artifacthub.io/changes for this release" >&2
  status=1
fi

exit "$status"
