#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
cd "${repo_root}"

next_patch_version() {
  local version="${1#v}"
  local major minor patch
  IFS='.' read -r major minor patch <<<"${version}"
  if [[ -z "${major:-}" || -z "${minor:-}" || -z "${patch:-}" ]]; then
    printf 'v0.0.1\n'
    return 0
  fi
  printf 'v%s.%s.%s\n' "${major}" "${minor}" "$((patch + 1))"
}

env_or_default() {
  local var_name="$1"
  local default_value="$2"
  if [[ "${!var_name+x}" == "x" ]]; then
    printf '%s' "${!var_name}"
  else
    printf '%s' "${default_value}"
  fi
}

exact_tag="$(env_or_default IMPRINT_TEST_EXACT_TAG "$(git describe --tags --exact-match 2>/dev/null || true)")"
base_tag="$(env_or_default IMPRINT_TEST_BASE_TAG "$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")")"
short_sha="$(env_or_default IMPRINT_TEST_SHORT_SHA "$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")")"
dirty_state="${IMPRINT_TEST_DIRTY:-auto}"
dirty_suffix=""

case "${dirty_state}" in
  true|1|yes)
    dirty_suffix=".dirty"
    ;;
  false|0|no)
    dirty_suffix=""
    ;;
  *)
    if [[ -n "$(git status --porcelain 2>/dev/null || true)" ]]; then
      dirty_suffix=".dirty"
    fi
    ;;
esac

if [[ -n "${exact_tag}" && -z "${dirty_suffix}" ]]; then
  printf '%s\n' "${exact_tag}"
  exit 0
fi

base_version="${IMPRINT_BASE_VERSION:-}"
if [[ -z "${base_version}" ]]; then
  if [[ -n "${exact_tag}" ]]; then
    base_version="$(next_patch_version "${exact_tag}")"
  else
    base_version="$(next_patch_version "${base_tag}")"
  fi
fi

printf '%s\n' "${base_version}-dev+${short_sha}${dirty_suffix}"
