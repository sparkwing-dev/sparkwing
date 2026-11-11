#!/usr/bin/env bash
set -euo pipefail

repo="${PWD}"
brief=""
mode=(--sw-detached)
dry=()
while (($#)); do
  case "$1" in
    -C) repo="${2:?missing checkout}"; shift 2 ;;
    --brief) brief="${2:?missing goal}"; shift 2 ;;
    --foreground) mode=(); shift ;;
    --dry-run) dry=(--sw-dry-run); shift ;;
    *) echo "usage: bash queue-land.sh -C <candidate> --brief <goal> [--foreground] [--dry-run]" >&2; exit 2 ;;
  esac
done
if [[ -z "${brief//[[:space:]]/}" ]]; then
  echo "land queue: --brief requires a non-empty goal" >&2
  exit 2
fi
repo="$(git -C "$repo" rev-parse --show-toplevel)"
git -C "$repo" fetch --quiet origin main
main="$(git -C "$repo" rev-parse --verify 'refs/remotes/origin/main^{commit}')"
trusted="$(git -C "$repo" rev-parse "$main:bin/queue-land.sh")"
actual="$(git hash-object -- "${BASH_SOURCE[0]}")"
if [[ "$actual" != "$trusted" ]]; then
  echo "land queue: submission script differs from origin/main; extract bin/queue-land.sh from freshly fetched origin/main (see DELIVERY.md)" >&2
  exit 1
fi
if ! git -C "$repo" diff --quiet --no-ext-diff "$main" -- .sparkwing/sparkwing.yaml; then
  echo "land queue: .sparkwing/sparkwing.yaml differs from origin/main and can redirect the land entrypoint; restore main's registry or have an operator review its land mapping and run:" >&2
  printf '  ' >&2
  printf '%q ' sparkwing run land -C "$repo" --sw-route local --sw-pipeline-ref "$main" \
    --sw-allow-risk destructive,prod -- --brief "$brief" >&2
  printf '\n' >&2
  exit 1
fi
exec sparkwing run land -C "$repo" "${mode[@]}" "${dry[@]}" --sw-route local \
  --sw-pipeline-ref "$main" --sw-allow-risk destructive,prod -- \
  --queued --queued-main "$main" --brief "$brief"
