#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --app PATH.app [--timeout SECONDS]" >&2
  exit 2
}

app=
timeout=20
while [ "$#" -gt 0 ]; do
  case "$1" in
    --app)
      [ "$#" -ge 2 ] || usage
      app=$2
      shift 2
      ;;
    --timeout)
      [ "$#" -ge 2 ] || usage
      timeout=$2
      shift 2
      ;;
    *) usage ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || {
  echo "macOS Wails launch smoke requires Darwin" >&2
  exit 1
}
[ -n "$app" ] || usage
case "$timeout" in
  ''|*[!0-9]*) usage ;;
esac
[ "$timeout" -gt 0 ] || usage

app_directory=$(cd "$(dirname "$app")" && pwd -P)
app="$app_directory/$(basename "$app")"
[ -d "$app" ] || { echo "app bundle not found: $app" >&2; exit 1; }

plist="$app/Contents/Info.plist"
[ -f "$plist" ] || { echo "Info.plist not found: $plist" >&2; exit 1; }
bundle_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist")
executable_name=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist")
executable="$app/Contents/MacOS/$executable_name"
[ -x "$executable" ] || { echo "bundle executable is missing: $executable" >&2; exit 1; }

for command_path in /usr/bin/open /usr/bin/osascript /usr/bin/pgrep /usr/bin/mktemp /bin/rm /bin/sleep /bin/date; do
  [ -x "$command_path" ] || { echo "required macOS command is missing: $command_path" >&2; exit 1; }
done

existing_pids=$(/usr/bin/pgrep -x "$executable_name" || true)
[ -z "$existing_pids" ] || {
  echo "refusing to race an existing $executable_name process: $existing_pids" >&2
  exit 1
}

smoke_root=$(/usr/bin/mktemp -d "${TMPDIR:-/tmp}/yuri-launch-smoke.XXXXXX")
ready_file="$smoke_root/dom-ready.json"
launch_log="$smoke_root/launch.log"
open_pid=

process_alive() {
  /bin/kill -0 "$1" 2>/dev/null
}

print_diagnostics() {
  echo "--- isolated profile ---" >&2
  /bin/ls -la "$smoke_root" "$smoke_root/data" >&2 || true
  if [ -f "$launch_log" ]; then
    echo "--- launch output ---" >&2
    sed -n '1,160p' "$launch_log" >&2 || true
  fi
}

cleanup() {
  status=$?
  trap - EXIT INT TERM HUP
  set +e
  if [ -n "${open_pid:-}" ] && process_alive "$open_pid"; then
    /usr/bin/osascript -e "tell application id \"$bundle_id\" to quit" >/dev/null 2>&1
    /bin/kill -TERM "$open_pid" >/dev/null 2>&1
  fi
  [ "$status" -eq 0 ] || print_diagnostics
  case "$smoke_root" in
    "${TMPDIR:-/tmp}"/yuri-launch-smoke.*) /bin/rm -rf "$smoke_root" ;;
    *) echo "refusing to remove unexpected smoke root: $smoke_root" >&2 ;;
  esac
  exit "$status"
}
trap cleanup EXIT INT TERM HUP

# Launch Services receives every smoke variable explicitly. The production
# binary ignores profile redirection and auto-exit unless YURI_TEST_MODE=1.
/usr/bin/open -n -W \
  --stdout "$launch_log" \
  --stderr "$launch_log" \
  --env "YURI_TEST_MODE=1" \
  --env "YURI_TEST_PROFILE_ROOT=$smoke_root" \
  --env "YURI_TEST_READY_FILE=$ready_file" \
  --env "YURI_TEST_AUTO_EXIT=1" \
  "$app" &
open_pid=$!

deadline=$(( $(/bin/date +%s) + timeout ))
while [ ! -f "$ready_file" ] && [ "$(/bin/date +%s)" -lt "$deadline" ]; do
  if ! process_alive "$open_pid"; then
    wait_status=0
    if ! wait "$open_pid"; then wait_status=$?; fi
    open_pid=
    echo "app exited before the Wails DOM-ready marker (status $wait_status)" >&2
    exit 1
  fi
  /bin/sleep 1
done

[ -f "$ready_file" ] || {
  echo "Wails DOM did not become ready within ${timeout}s" >&2
  exit 1
}
grep -q '"state":"ready"' "$ready_file" || {
  echo "unexpected Wails readiness marker" >&2
  exit 1
}

database_file="$smoke_root/data/yuri.sqlite3"
[ -f "$database_file" ] || {
  echo "isolated Yuri database was not created: $database_file" >&2
  exit 1
}

exit_deadline=$(( $(/bin/date +%s) + timeout ))
while process_alive "$open_pid" && [ "$(/bin/date +%s)" -lt "$exit_deadline" ]; do
  /bin/sleep 1
done
if process_alive "$open_pid"; then
  echo "app did not auto-exit cleanly within ${timeout}s after DOM ready" >&2
  exit 1
fi

wait_status=0
if ! wait "$open_pid"; then wait_status=$?; fi
open_pid=
[ "$wait_status" -eq 0 ] || {
  echo "open(1) reported a non-zero app exit status: $wait_status" >&2
  exit 1
}

if [ -x /usr/bin/sqlite3 ]; then
  # Apple's sqlite3 -readonly currently fails on this WAL-created database
  # even after a clean close. query_only keeps the validation connection from
  # mutating the temporary store while remaining compatible with the system CLI.
  integrity=$(/usr/bin/sqlite3 "$database_file" 'PRAGMA query_only=ON; PRAGMA integrity_check;' 2>&1) || {
    echo "SQLite integrity check failed after app shutdown: $integrity" >&2
    exit 1
  }
  [ "$integrity" = "ok" ] || { echo "unexpected SQLite integrity result: $integrity" >&2; exit 1; }
fi

echo "macOS Wails launch smoke passed: $app"
echo "DOM ready, isolated profile created, and application shut down cleanly"
