#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/bin" "$test_root/state"
export HOME="$test_root/home"
export XDG_STATE_HOME="$test_root/state"
export ADB_SERIAL=mock-device
mkdir -p "$HOME"

cat > "$test_root/bin/adb" <<'MOCK_ADB'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == "-s" ]] || exit 2
shift 2
if [[ "$1" == "get-state" ]]; then echo device; exit 0; fi
[[ "$1" == "shell" && "$2" == "pm" ]] || exit 2
shift 2
if [[ "$1" == "list" && "$2" == "packages" ]]; then
  case "${3:-}" in
    -3) printf 'package:com.example.tracker\npackage:com.example.unused\n' ;;
    -d) [[ -f "$MOCK_DISABLED" ]] && sed 's/^/package:/' "$MOCK_DISABLED" || true ;;
    *) printf 'package:com.example.tracker\npackage:com.example.unused\npackage:com.android.example\n';;
  esac
elif [[ "$1" == "disable-user" ]]; then
  [[ "$2" == "--user" && "$3" == "0" ]] || exit 2
  printf '%s\n' "$4" >> "$MOCK_DISABLED"
elif [[ "$1" == "enable" ]]; then
  [[ "$2" == "--user" && "$3" == "0" ]] || exit 2
  grep -Fxv -- "$4" "$MOCK_DISABLED" > "$MOCK_DISABLED.tmp" || true
  mv "$MOCK_DISABLED.tmp" "$MOCK_DISABLED"
else
  exit 2
fi
MOCK_ADB
chmod +x "$test_root/bin/adb"
export PATH="$test_root/bin:$PATH"
export MOCK_DISABLED="$test_root/disabled.txt"
touch "$MOCK_DISABLED"

packages="$test_root/packages.txt"
printf ' # comment\n  com.example.tracker  \n' > "$packages"
script="$project_root/scripts/android-debloat.sh"

"$script" list >/dev/null
"$script" apply --packages "$packages" --dry-run >/dev/null
[[ ! -s "$MOCK_DISABLED" ]] || { echo "dry run changed package state" >&2; exit 1; }
"$script" apply --packages "$packages" --confirm >/dev/null
grep -Fxq com.example.tracker "$MOCK_DISABLED"
"$script" restore >/dev/null
[[ ! -s "$MOCK_DISABLED" ]] || { echo "restore did not re-enable the package" >&2; exit 1; }

printf 'com.android.systemui\n' > "$packages"
if "$script" apply --packages "$packages" --confirm --allow-system >/dev/null 2>&1; then
  echo "protected packages must never be disabled" >&2
  exit 1
fi
[[ ! -s "$MOCK_DISABLED" ]] || { echo "protected package test changed package state" >&2; exit 1; }
printf 'com.vendor.emergencydialer\n' > "$packages"
if "$script" apply --packages "$packages" --confirm --allow-system >/dev/null 2>&1; then
  echo "emergency dialer packages must never be disabled" >&2
  exit 1
fi
[[ ! -s "$MOCK_DISABLED" ]] || { echo "emergency package test changed package state" >&2; exit 1; }
printf 'com.example .tracker\n' > "$packages"
if "$script" apply --packages "$packages" --confirm >/dev/null 2>&1; then
  echo "malformed package names must not be silently normalized" >&2
  exit 1
fi
[[ ! -s "$MOCK_DISABLED" ]] || { echo "invalid package test changed package state" >&2; exit 1; }
echo "ADB debloat safety tests passed"
