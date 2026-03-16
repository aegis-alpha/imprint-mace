#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# --- Check clean state ---
if [ -n "$(git status --porcelain)" ]; then
  echo "ERROR: uncommitted changes. Commit or stash first."
  exit 1
fi

# --- Get last tag ---
LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
if [ -z "$LAST_TAG" ]; then
  echo "No previous tag found. This will be the first release."
  COMMITS=$(git log --oneline --no-decorate)
else
  echo "Last tag: $LAST_TAG"
  COMMITS=$(git log "${LAST_TAG}..HEAD" --oneline --no-decorate)
fi

if [ -z "$COMMITS" ]; then
  echo "No commits since $LAST_TAG. Nothing to release."
  exit 0
fi

# --- Determine bump type from conventional commits ---
HAS_BREAKING=false
HAS_FEAT=false
HAS_FIX=false

while IFS= read -r line; do
  msg="${line#* }"
  case "$msg" in
    feat!:*|feat\(*\)!:*|BREAKING\ CHANGE:*) HAS_BREAKING=true ;;
    feat:*|feat\(*\):*) HAS_FEAT=true ;;
    fix:*|fix\(*\):*|docs:*|refactor:*|perf:*) HAS_FIX=true ;;
  esac
done <<< "$COMMITS"

# --- Parse current version ---
if [ -z "$LAST_TAG" ]; then
  MAJOR=0; MINOR=0; PATCH=0
else
  VERSION="${LAST_TAG#v}"
  MAJOR=$(echo "$VERSION" | cut -d. -f1)
  MINOR=$(echo "$VERSION" | cut -d. -f2)
  PATCH=$(echo "$VERSION" | cut -d. -f3)
fi

# --- Calculate new version ---
if [ "$HAS_BREAKING" = true ]; then
  if [ "$MAJOR" -eq 0 ]; then
    MINOR=$((MINOR + 1)); PATCH=0
  else
    MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0
  fi
  BUMP="BREAKING (minor bump, pre-stable)"
elif [ "$HAS_FEAT" = true ]; then
  MINOR=$((MINOR + 1)); PATCH=0
  BUMP="MINOR (new feature)"
elif [ "$HAS_FIX" = true ]; then
  PATCH=$((PATCH + 1))
  BUMP="PATCH (fix)"
else
  PATCH=$((PATCH + 1))
  BUMP="PATCH (no conventional prefix detected)"
fi

NEW_VERSION="v${MAJOR}.${MINOR}.${PATCH}"

# --- Show summary ---
echo ""
echo "=== Release Summary ==="
echo "Bump type: $BUMP"
echo "Version:   $LAST_TAG -> $NEW_VERSION"
echo ""
echo "Commits:"
echo "$COMMITS"
echo ""

# --- Generate changelog entries ---
FEAT_ENTRIES=""
FIX_ENTRIES=""
OTHER_ENTRIES=""

while IFS= read -r line; do
  msg="${line#* }"
  case "$msg" in
    feat!:*|feat\(*\)!:*)
      entry="${msg#*: }"
      FEAT_ENTRIES="${FEAT_ENTRIES}\n- **BREAKING:** ${entry}" ;;
    feat:*|feat\(*\):*)
      entry="${msg#*: }"
      FEAT_ENTRIES="${FEAT_ENTRIES}\n- ${entry}" ;;
    fix:*|fix\(*\):*)
      entry="${msg#*: }"
      FIX_ENTRIES="${FIX_ENTRIES}\n- ${entry}" ;;
    docs:*|refactor:*|perf:*|chore:*|test:*)
      entry="${msg#*: }"
      OTHER_ENTRIES="${OTHER_ENTRIES}\n- ${entry}" ;;
    *)
      OTHER_ENTRIES="${OTHER_ENTRIES}\n- ${msg}" ;;
  esac
done <<< "$COMMITS"

DATE=$(date +%Y-%m-%d)
CHANGELOG_BLOCK="## [${NEW_VERSION#v}] - ${DATE}"
if [ -n "$FEAT_ENTRIES" ]; then
  CHANGELOG_BLOCK="${CHANGELOG_BLOCK}\n\n### Added\n${FEAT_ENTRIES}"
fi
if [ -n "$FIX_ENTRIES" ]; then
  CHANGELOG_BLOCK="${CHANGELOG_BLOCK}\n\n### Fixed\n${FIX_ENTRIES}"
fi
if [ -n "$OTHER_ENTRIES" ]; then
  CHANGELOG_BLOCK="${CHANGELOG_BLOCK}\n\n### Changed\n${OTHER_ENTRIES}"
fi

echo "Proposed CHANGELOG entry:"
echo ""
echo -e "$CHANGELOG_BLOCK"
echo ""

# --- Confirm ---
read -p "Proceed with release $NEW_VERSION? [y/N] " CONFIRM
if [ "$CONFIRM" != "y" ] && [ "$CONFIRM" != "Y" ]; then
  echo "Aborted."
  exit 0
fi

# --- Update CHANGELOG.md ---
LINK_LINE="[${NEW_VERSION#v}]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/${NEW_VERSION}"

if [ -f CHANGELOG.md ]; then
  TEMP=$(mktemp)
  awk -v block="$(echo -e "$CHANGELOG_BLOCK")" -v link="$LINK_LINE" '
    /^## \[/ && !inserted {
      print block
      print ""
      inserted=1
    }
    { print }
    END { print link }
  ' CHANGELOG.md > "$TEMP"
  mv "$TEMP" CHANGELOG.md
else
  echo -e "# Changelog\n\n${CHANGELOG_BLOCK}\n\n${LINK_LINE}" > CHANGELOG.md
fi

# --- Update PROJECT.md version ---
if [ -f PROJECT.md ]; then
  sed -i.bak "s/\*\*Current version: v[0-9]*\.[0-9]*\.[0-9]*\*\*/**Current version: ${NEW_VERSION}**/" PROJECT.md
  rm -f PROJECT.md.bak
fi

# --- Commit, tag, push ---
git add CHANGELOG.md PROJECT.md
git commit -m "chore: release ${NEW_VERSION}"
git tag "$NEW_VERSION"
git push origin main
git push origin "$NEW_VERSION"

echo ""
echo "Released ${NEW_VERSION}"
echo "GitHub Actions will create the GitHub Release and Docker image."
echo "Check: https://github.com/aegis-alpha/imprint-MACE/actions"
