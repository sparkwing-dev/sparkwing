#!/usr/bin/env bash
# Refuses a release tag that does not outrank every version already published.
# The candidate is $1; the tags a repository already carries arrive on stdin,
# one per line, as bare tags or as `git ls-remote --tags` output.
set -euo pipefail

candidate="${1:-}"
if [ -z "$candidate" ]; then
  echo "usage: check-release-tag-order.sh <vX.Y.Z> < existing-tags" >&2
  exit 2
fi

semver_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

core_of() {
  local v="${1#v}"
  printf '%s' "${v%%-*}"
}

pre_of() {
  local v="${1#v}"
  case "$v" in
  *-*) printf '%s' "${v#*-}" ;;
  *) printf '' ;;
  esac
}

# Prints 1, 0 or -1 for $1 against $2 under semver precedence: numeric release
# fields first, then a pre-release ranking below the release it qualifies.
semver_cmp() {
  local a="$1" b="$2" i x y n ap bp
  local -a ac bc ai bi
  IFS=. read -r -a ac <<<"$(core_of "$a")"
  IFS=. read -r -a bc <<<"$(core_of "$b")"
  for i in 0 1 2; do
    if ((10#${ac[i]} > 10#${bc[i]})); then
      printf '1'
      return
    fi
    if ((10#${ac[i]} < 10#${bc[i]})); then
      printf -- '-1'
      return
    fi
  done
  ap="$(pre_of "$a")"
  bp="$(pre_of "$b")"
  if [ -z "$ap" ] && [ -z "$bp" ]; then
    printf '0'
    return
  fi
  if [ -z "$ap" ]; then
    printf '1'
    return
  fi
  if [ -z "$bp" ]; then
    printf -- '-1'
    return
  fi
  IFS=. read -r -a ai <<<"$ap"
  IFS=. read -r -a bi <<<"$bp"
  n=${#ai[@]}
  if ((${#bi[@]} > n)); then n=${#bi[@]}; fi
  for ((i = 0; i < n; i++)); do
    x="${ai[i]-}"
    y="${bi[i]-}"
    if [ -z "$x" ]; then
      printf -- '-1'
      return
    fi
    if [ -z "$y" ]; then
      printf '1'
      return
    fi
    if [[ "$x" =~ ^[0-9]+$ ]] && [[ "$y" =~ ^[0-9]+$ ]]; then
      if ((10#$x > 10#$y)); then
        printf '1'
        return
      fi
      if ((10#$x < 10#$y)); then
        printf -- '-1'
        return
      fi
    elif [[ "$x" =~ ^[0-9]+$ ]]; then
      printf -- '-1'
      return
    elif [[ "$y" =~ ^[0-9]+$ ]]; then
      printf '1'
      return
    elif [[ "$x" > "$y" ]]; then
      printf '1'
      return
    elif [[ "$x" < "$y" ]]; then
      printf -- '-1'
      return
    fi
  done
  printf '0'
}

if ! [[ "$candidate" =~ $semver_re ]]; then
  echo "check-release-tag-order: $candidate is not a vMAJOR.MINOR.PATCH release tag (an optional -prerelease suffix is allowed)" >&2
  exit 1
fi

newest=""
while IFS= read -r line || [ -n "$line" ]; do
  tag="${line##*refs/tags/}"
  tag="${tag%^\{\}}"
  tag="${tag%%[[:space:]]*}"
  [ -n "$tag" ] || continue
  [[ "$tag" =~ $semver_re ]] || continue
  if [ -z "$newest" ] || [ "$(semver_cmp "$tag" "$newest")" = "1" ]; then
    newest="$tag"
  fi
done

if [ -z "$newest" ]; then
  echo "check-release-tag-order: $candidate is the first release tag"
  exit 0
fi

if [ "$(semver_cmp "$candidate" "$newest")" != "1" ]; then
  echo "check-release-tag-order: $candidate does not outrank $newest, the newest tag this repository carries. A release tag must be ahead of every published version; cut a higher one." >&2
  exit 1
fi

echo "check-release-tag-order: $candidate is ahead of $newest"
