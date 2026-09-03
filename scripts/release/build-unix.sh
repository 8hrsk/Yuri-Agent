#!/bin/sh
set -eu

if [ "$#" -ne 5 ]; then
  echo "usage: $0 PLATFORM ARCH VERSION OUTPUT_DIR TOOLS_DIR" >&2
  exit 2
fi

platform=$1
arch=$2
version=$3
output_dir=$4
tools_dir=$5

case "$version" in
  *[!0-9.]*|.*|*.|*..*) echo "invalid stable SemVer: $version" >&2; exit 2 ;;
esac
[ "$(printf '%s' "$version" | awk -F. '{ print NF }')" -eq 3 ] || {
  echo "invalid stable SemVer: $version" >&2
  exit 2
}

case "$platform/$arch" in
  darwin/universal|linux/amd64|linux/arm64) ;;
  *) echo "unsupported release target: $platform/$arch" >&2; exit 2 ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository=$(CDPATH= cd -- "$script_dir/../.." && pwd)
output_dir=$(mkdir -p "$output_dir" && CDPATH= cd -- "$output_dir" && pwd)
tools_dir=$(mkdir -p "$tools_dir" && CDPATH= cd -- "$tools_dir" && pwd)
wails_version=v2.15.0
commit=$(git -C "$repository" rev-parse HEAD)
build_date=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

if [ "$platform" = linux ]; then
  command -v docker >/dev/null 2>&1 || {
    echo "Docker Desktop with buildx is required to build Linux on macOS" >&2
    exit 1
  }
  if ! docker info >/dev/null 2>&1; then
    [ "$(uname -s)" = Darwin ] && [ -d /Applications/Docker.app ] || {
      echo "Docker daemon is not available" >&2
      exit 1
    }
    open -gja Docker
    attempts=0
    until docker info >/dev/null 2>&1; do
      attempts=$((attempts + 1))
      [ "$attempts" -lt 60 ] || {
        echo "Docker Desktop did not become ready within 120 seconds" >&2
        exit 1
      }
      sleep 2
    done
  fi
  docker buildx version >/dev/null
  staging=$(mktemp -d "${TMPDIR:-/tmp}/yuri-linux-release.XXXXXX")
  trap 'rm -rf "$staging"' EXIT HUP INT TERM
  docker buildx build \
    --platform "linux/$arch" \
    --file "$script_dir/Dockerfile.linux" \
    --target artifact \
    --build-arg "VERSION=$version" \
    --build-arg "COMMIT=$commit" \
    --build-arg "BUILD_DATE=$build_date" \
    --output "type=local,dest=$staging" \
    "$repository"
  executable="$staging/yuri-linux-$arch"
  [ -x "$executable" ] || { echo "Linux executable not found: $executable" >&2; exit 1; }
  file "$executable" | grep -F ELF >/dev/null || {
    echo "Linux release is not an ELF executable: $executable" >&2
    exit 1
  }
  artifact="$output_dir/yuri-$version-linux-$arch.tar.gz"
  tar -czf "$artifact" -C "$staging" "yuri-linux-$arch"
  "$repository/scripts/checksum-artifact.sh" "$artifact" "$artifact.sha256"
  "$repository/scripts/checksum-artifact.sh" --verify "$artifact" "$artifact.sha256"
  exit 0
fi

[ "$(uname -s)" = Darwin ] || { echo "darwin releases require macOS" >&2; exit 1; }
wails="$tools_dir/wails"
if [ ! -x "$wails" ] || ! "$wails" version 2>&1 | grep -F "$wails_version" >/dev/null; then
  GOBIN="$tools_dir" go install "github.com/wailsapp/wails/v2/cmd/wails@$wails_version"
fi

npm --prefix "$repository/frontend" ci --no-audit --no-fund
npm --prefix "$repository/frontend" run build

wails_config="$repository/cmd/yuri/wails.json"
wails_backup=$(mktemp "${TMPDIR:-/tmp}/yuri-wails.XXXXXX")
cp "$wails_config" "$wails_backup"
restore_wails_config() {
  cp "$wails_backup" "$wails_config"
  rm -f "$wails_backup"
}
trap restore_wails_config EXIT HUP INT TERM
node "$script_dir/set-wails-version.mjs" "$wails_config" "$version"

(
  cd "$repository/cmd/yuri"
  CGO_ENABLED=1 \
  MACOSX_DEPLOYMENT_TARGET=11.0.0 \
  CGO_CFLAGS='-mmacosx-version-min=11.0.0' \
  CGO_LDFLAGS='-mmacosx-version-min=11.0.0' \
  "$wails" build -clean -platform darwin/universal -trimpath \
    -ldflags "-s -w -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Version=$version -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Commit=$commit -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Date=$build_date" \
    -m -nosyncgomod -s
)

app="$repository/cmd/yuri/build/bin/yuri.app"
"$repository/scripts/validate-macos-oss.sh" --app "$app" --version "$version"
artifact="$output_dir/yuri-$version-macos-universal.zip"
rm -f "$artifact"
ditto -c -k --sequesterRsrc --keepParent "$app" "$artifact"
"$repository/scripts/checksum-artifact.sh" "$artifact" "$artifact.sha256"
"$repository/scripts/checksum-artifact.sh" --verify "$artifact" "$artifact.sha256"
