#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --app PATH.app --version X.Y.Z" >&2
  exit 2
}

app=
version=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --app) [ "$#" -ge 2 ] || usage; app=$2; shift 2 ;;
    --version) [ "$#" -ge 2 ] || usage; version=$2; shift 2 ;;
    *) usage ;;
  esac
done

[ -n "$app" ] || usage
[ -n "$version" ] || usage
[ -d "$app" ] || { echo "app bundle not found: $app" >&2; exit 1; }

plist="$app/Contents/Info.plist"
[ -f "$plist" ] || { echo "Info.plist not found: $plist" >&2; exit 1; }
plutil -lint "$plist" >/dev/null

read_plist() {
  /usr/libexec/PlistBuddy -c "Print :$1" "$plist"
}

package_type=$(read_plist CFBundlePackageType)
[ "$package_type" = "APPL" ] || { echo "unexpected bundle type: $package_type" >&2; exit 1; }
identifier=$(read_plist CFBundleIdentifier)
[ "$identifier" = "ai.ordo.yuri" ] || { echo "unexpected bundle id: $identifier" >&2; exit 1; }
bundle_version=$(read_plist CFBundleShortVersionString)
[ "$bundle_version" = "$version" ] || { echo "unexpected bundle version: $bundle_version (expected $version)" >&2; exit 1; }
build_version=$(read_plist CFBundleVersion)
[ "$build_version" = "$version" ] || { echo "unexpected bundle build version: $build_version (expected $version)" >&2; exit 1; }
minimum=$(read_plist LSMinimumSystemVersion)
[ "$minimum" = "11.0.0" ] || { echo "unexpected minimum macOS version: $minimum" >&2; exit 1; }
microphone=$(read_plist NSMicrophoneUsageDescription)
[ -n "$microphone" ] || { echo "missing microphone usage description" >&2; exit 1; }

executable_name=$(read_plist CFBundleExecutable)
executable="$app/Contents/MacOS/$executable_name"
[ -x "$executable" ] || { echo "bundle executable is missing: $executable" >&2; exit 1; }
architectures=$(lipo -archs "$executable")
case " $architectures " in *" arm64 "*) ;; *) echo "arm64 slice is missing: $architectures" >&2; exit 1 ;; esac
case " $architectures " in *" x86_64 "*) ;; *) echo "x86_64 slice is missing: $architectures" >&2; exit 1 ;; esac

echo "Validated universal macOS OSS bundle metadata: $app"
