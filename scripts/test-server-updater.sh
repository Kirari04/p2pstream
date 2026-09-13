#!/usr/bin/env bash
set -Eeuo pipefail

# All registry aliases, published fixture ports, /etc/hosts changes, and server
# replacements live in this disposable nested daemon. The outer daemon only
# builds images and starts/removes this script's uniquely named container.
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"
command -v docker >/dev/null
[[ $(docker info --format '{{.OSType}}') == linux ]] || {
  echo "The server updater rehearsal requires a Linux Docker engine." >&2
  exit 1
}
artifact_parent=${SERVER_UPDATER_TEST_ARTIFACTS:-"$repo_dir/tmp/server-updater-tests"}
mkdir -p -- "$artifact_parent"
artifact_parent=$(cd -- "$artifact_parent" && pwd)
artifact_dir=$(mktemp -d "$artifact_parent/run.XXXXXX")
run_id="$(date +%s)-$$-${RANDOM}"
daemon="p2pstream-updater-review-$run_id"
image_prefix="p2pstream-updater-review-$run_id"
baseline_image="$image_prefix:baseline"
candidate_image="$image_prefix:candidate"
helper_image="$image_prefix:helper"
upstream_image="$image_prefix:upstream"
baseline_version=v0.1.53-staging.9000
candidate_version=v0.1.53-staging.9001
baseline_commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
candidate_commit=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
dind_image=docker:28.3.3-dind
registry_image=registry:2

cleanup() {
  local result=$?
  trap - EXIT INT TERM
  set +e
  if docker inspect "$daemon" >/dev/null 2>&1; then
    docker logs "$daemon" >"$artifact_dir/daemon.log" 2>&1
    docker exec "$daemon" sh -c '
      mkdir -p /review/diagnostics
      docker ps -a --no-trunc > /review/diagnostics/containers.txt
      docker info > /review/diagnostics/docker-info.txt 2>&1
      for id in $(docker ps -aq); do
        docker inspect "$id" > "/review/diagnostics/$id.json"
        docker logs "$id" > "/review/diagnostics/$id.log" 2>&1
      done
    ' >"$artifact_dir/collect.log" 2>&1
    mkdir -p "$artifact_dir/review"
    docker cp "$daemon:/review/." "$artifact_dir/review" >>"$artifact_dir/collect.log" 2>&1 || {
      echo "Could not collect the guest review artifacts." >&2
      [[ $result -ne 0 ]] || result=1
    }
    docker rm --force --volumes "$daemon" >"$artifact_dir/cleanup.log" 2>&1 || {
      echo "Could not remove disposable daemon $daemon; remove it manually." >&2
      [[ $result -ne 0 ]] || result=1
    }
  fi
  # Remove only this run's tags; shared base images and their caches remain.
  docker image rm "$baseline_image" "$candidate_image" "$helper_image" "$upstream_image" >>"$artifact_dir/cleanup.log" 2>&1
  echo "Server updater rehearsal artifacts: $artifact_dir"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

guest() { docker exec -i "$daemon" "$@"; }

build_runtime() {
  local image=$1 version=$2 commit=$3
  docker build --target runtime --tag "$image" \
    --build-arg "VERSION=$version" --build-arg "COMMIT=$commit" \
    --build-arg SOURCE_REPOSITORY=review/p2pstream --build-arg RELEASE_CHANNEL=staging \
    --build-arg VITE_RELEASE_REPOSITORY=review/p2pstream --build-arg "VITE_RELEASE_REF=$version" .
}

echo "Building two release identities and the production-driver test helper."
build_runtime "$baseline_image" "$baseline_version" "$baseline_commit" 2>&1 | tee "$artifact_dir/build-baseline.log"
build_runtime "$candidate_image" "$candidate_version" "$candidate_commit" 2>&1 | tee "$artifact_dir/build-candidate.log"
docker build --file Dockerfile.updater-test --build-arg "REVIEW_RUNTIME=$baseline_image" \
  --tag "$helper_image" . 2>&1 | tee "$artifact_dir/build-helper.log"
docker build --target smoke-upstream --tag "$upstream_image" . 2>&1 | tee "$artifact_dir/build-upstream.log"
docker pull "$registry_image" 2>&1 | tee "$artifact_dir/pull-registry.log"
docker pull "$dind_image" 2>&1 | tee "$artifact_dir/pull-daemon.log"

echo "Starting disposable Docker 28.3.3; no host ports or host Docker socket are mounted."
docker run --detach --privileged --name "$daemon" \
  --add-host ghcr.io:127.0.0.1 --env DOCKER_TLS_CERTDIR= \
  --env DOCKER_HOST=unix:///var/run/docker.sock \
  "$dind_image" dockerd --host=unix:///var/run/docker.sock --insecure-registry=ghcr.io \
  >"$artifact_dir/daemon-id"
ready=false
for ((attempt=0; attempt<90; attempt++)); do
  if guest docker info >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
[[ $ready == true ]] || { echo "Disposable Docker daemon did not become ready." >&2; exit 1; }
[[ $(guest docker version --format '{{.Server.Version}}') == 28.3.3 ]] || {
  echo "Unexpected guest Docker engine version." >&2; exit 1;
}
guest apk add --no-cache python3 2>&1 | tee "$artifact_dir/install-python.log"
guest mkdir -p /review
guest touch /review/ISOLATED_TEST_VM
docker cp scripts/test-server-updater-docker.py "$daemon:/review/test-server-updater-docker.py"
docker save "$baseline_image" "$candidate_image" "$helper_image" "$upstream_image" "$registry_image" \
  | guest docker load >"$artifact_dir/load-images.log"

# Use precisely the Compose executable shipped in the tested updater. Enrolled
# config hashes differ between Compose versions even for equivalent models.
copy_container=$(guest docker create --entrypoint /bin/true "$helper_image")
guest docker cp "$copy_container:/usr/local/bin/docker" /tmp/review-docker
guest docker cp "$copy_container:/usr/local/lib/docker/cli-plugins/docker-compose" /tmp/review-compose
guest docker rm "$copy_container" >/dev/null
guest mkdir -p /usr/local/lib/docker/cli-plugins
guest mv /tmp/review-docker /usr/local/bin/docker
guest mv /tmp/review-compose /usr/local/lib/docker/cli-plugins/docker-compose
guest chmod 0755 /usr/local/bin/docker /usr/local/lib/docker/cli-plugins/docker-compose
compose_version=$(guest docker compose version --short)
[[ ${compose_version#v} == 2.39.2 ]] || { echo "Expected updater Compose 2.39.2, got $compose_version." >&2; exit 1; }

guest docker run --detach --name review-registry --publish 127.0.0.1:80:5000 "$registry_image" >/dev/null
guest python3 - <<'PY'
import time
import urllib.request
for attempt in range(60):
    try:
        with urllib.request.urlopen('http://127.0.0.1/v2/', timeout=2) as response:
            assert response.status == 200
        break
    except (OSError, AssertionError):
        time.sleep(1)
else:
    raise SystemExit('Guest registry did not become ready')
PY

guest docker tag "$baseline_image" "ghcr.io/review/p2pstream:$baseline_version"
guest docker tag "$candidate_image" "ghcr.io/review/p2pstream:$candidate_version"
guest docker tag "$upstream_image" p2pstream-review:upstream
guest docker push "ghcr.io/review/p2pstream:$baseline_version" >"$artifact_dir/push-baseline.log" 2>&1
guest docker push "ghcr.io/review/p2pstream:$candidate_version" >"$artifact_dir/push-candidate.log" 2>&1
baseline_ref=$(guest docker image inspect --format '{{index .RepoDigests 0}}' "ghcr.io/review/p2pstream:$baseline_version")
candidate_ref=$(guest docker image inspect --format '{{index .RepoDigests 0}}' "ghcr.io/review/p2pstream:$candidate_version")
helper_ref=$(guest docker image inspect --format '{{.Id}}' "$helper_image")
guest python3 - "$baseline_ref" "$candidate_ref" "$helper_ref" <<'PY'
import json
from pathlib import Path
import re
import sys
baseline, candidate, helper = sys.argv[1:]
for image in (baseline, candidate):
    assert re.fullmatch(r'ghcr\.io/review/p2pstream@sha256:[0-9a-f]{64}', image), image
assert re.fullmatch(r'sha256:[0-9a-f]{64}', helper), helper
assert baseline != candidate
Path('/review/images.json').write_text(json.dumps(dict(baseline=baseline, candidate=candidate, helper=helper)))
PY

for scenario in success rollback retry-recovery kill-validating; do
  echo "Running server update scenario: $scenario"
  guest python3 /review/test-server-updater-docker.py "$scenario" 2>&1 | tee "$artifact_dir/$scenario.log"
done
echo "SKIP reboot-committing: requires a real disposable VM reboot; Docker-in-Docker does not model host boot."
echo "PASS all four Docker server update lifecycle scenarios."
