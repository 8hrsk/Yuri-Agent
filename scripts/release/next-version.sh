#!/bin/sh
set -eu

repository=${1:-.}
cd "$repository"

semver_pattern='^[0-9]+\.[0-9]+\.[0-9]+$'
baseline=$(tr -d '[:space:]' < VERSION)
printf '%s\n' "$baseline" | grep -Eq "$semver_pattern" || {
  echo "VERSION must contain a stable SemVer such as 0.7.0" >&2
  exit 1
}

semver_max() {
  awk -F. '
    NF == 3 && (!found || $1 > major || ($1 == major && $2 > minor) || ($1 == major && $2 == minor && $3 > patch)) {
      major=$1; minor=$2; patch=$3; found=1
    }
    END { if (found) printf "%d.%d.%d\n", major, minor, patch }
  '
}

head_version=$(git tag --points-at HEAD | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+)$/\1/p' | semver_max)
if [ -n "$head_version" ]; then
  printf '%s\n' "$head_version"
  exit 0
fi

latest=$(git tag --list | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+)$/\1/p' | semver_max)
if [ -z "$latest" ]; then
  printf '%s\n' "$baseline"
  exit 0
fi

latest_tag=v$latest
highest=$(printf '%s\n%s\n' "$latest" "$baseline" | semver_max)
if [ "$highest" = "$baseline" ] && [ "$baseline" != "$latest" ]; then
  printf '%s\n' "$baseline"
  exit 0
fi

major=${latest%%.*}
remainder=${latest#*.}
minor=${remainder%%.*}
patch=${latest##*.}
messages=$(git log --format='%s%n%b' "$latest_tag..HEAD")

if printf '%s\n' "$messages" | grep -Eq '(^|[[:space:]])BREAKING CHANGE:|^[a-zA-Z]+(\([^)]*\))?!:'; then
  major=$((major + 1))
  minor=0
  patch=0
elif printf '%s\n' "$messages" | grep -Eq '^feat(\([^)]*\))?:'; then
  minor=$((minor + 1))
  patch=0
else
  patch=$((patch + 1))
fi

printf '%s.%s.%s\n' "$major" "$minor" "$patch"
