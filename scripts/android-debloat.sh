#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  android-debloat.sh list [--serial SERIAL]
  android-debloat.sh apply --packages FILE [--dry-run] [--serial SERIAL]
  android-debloat.sh apply --packages FILE --confirm [--serial SERIAL] [--allow-system]
  android-debloat.sh restore [--serial SERIAL]

Set ADB_SERIAL or --serial to the target device serial.
Apply defaults to dry-run. Only --confirm disables packages for Android user 0 and records changes.
System packages require --allow-system. No package is uninstalled or removed from the ROM.
EOF
}

mode=""
serial="${ADB_SERIAL:-}"
packages_file=""
confirmed=false
dry_run_explicit=false
allow_system=false

while (($#)); do
  case "$1" in
    list|apply|restore)
      if [[ -n "$mode" ]]; then usage >&2; exit 2; fi
      mode="$1"; shift ;;
    --serial)
      (($# >= 2)) || { usage >&2; exit 2; }
      serial="$2"; shift 2 ;;
    --packages)
      (($# >= 2)) || { usage >&2; exit 2; }
      packages_file="$2"; shift 2 ;;
    --confirm) confirmed=true; shift ;;
    --dry-run) dry_run_explicit=true; shift ;;
    --allow-system) allow_system=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "$mode" ]] || { usage >&2; exit 2; }
[[ -n "$serial" ]] || { echo "Set ADB_SERIAL or pass --serial SERIAL" >&2; exit 2; }
command -v adb >/dev/null || { echo "adb not found" >&2; exit 1; }
[[ "$(adb -s "$serial" get-state 2>/dev/null || true)" == "device" ]] || {
  echo "No authorized ADB device at $serial" >&2
  exit 1
}

state_root="${XDG_STATE_HOME:-$HOME/.local/state}/kidstracker"
safe_serial="${serial//[^A-Za-z0-9_.-]/_}"
state_file="$state_root/$safe_serial/disabled-by-script.txt"

package_list() { adb -s "$serial" shell pm list packages "$@" | sed 's/^package://' | tr -d '\r'; }
is_installed() { package_list | grep -Fxq -- "$1"; }
is_third_party() { package_list -3 | grep -Fxq -- "$1"; }
is_disabled() { package_list -d | grep -Fxq -- "$1"; }

protected_package() {
  case "$1" in
    android|*emergency*|*Emergency*|*dialer*|*Dialer*|*telecom*|*Telecom*|*incall*|*InCall*|com.android.systemui|com.android.settings|com.android.phone|com.android.server.telecom|com.android.providers.telephony|com.android.providers.settings|com.android.packageinstaller|com.google.android.packageinstaller|com.android.location.fused|com.android.providers.downloads|com.android.keychain|com.android.deskclock|com.android.alarmclock|com.android.inputmethod.*|com.android.vending|com.google.android.gms|com.google.android.gsf|com.google.android.googlequicksearchbox)
      return 0 ;;
    *) return 1 ;;
  esac
}

case "$mode" in
  list)
    echo "Third-party packages on $serial:"
    package_list -3 | sort
    ;;
  apply)
    [[ -n "$packages_file" && -f "$packages_file" ]] || { echo "Supply an existing --packages FILE" >&2; exit 2; }
    if [[ "$confirmed" == true && "$dry_run_explicit" == true ]]; then
      echo "Choose either --confirm or --dry-run" >&2
      exit 2
    fi
    if [[ "$allow_system" == true ]]; then
      echo "WARNING: system-package disabling can break boot, calls, GPS, or recovery."
    fi
    if [[ "$confirmed" == true ]]; then
      mkdir -p "$(dirname "$state_file")"
      touch "$state_file"
    else
      echo "DRY RUN: no packages will be changed (pass --confirm to apply)."
    fi
    while IFS= read -r package || [[ -n "$package" ]]; do
      package="${package%%#*}"
      package="$(printf '%s' "$package" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
      [[ -z "$package" ]] && continue
      [[ "$package" =~ ^[A-Za-z0-9_]+([.][A-Za-z0-9_]+)+$ ]] || { echo "Invalid package name: $package" >&2; exit 1; }
      if protected_package "$package"; then
        echo "Refusing protected package: $package" >&2
        exit 1
      fi
      if ! is_installed "$package"; then
        echo "Not installed; skipping: $package"
        continue
      fi
      if ! is_third_party "$package" && [[ "$allow_system" != true ]]; then
        echo "System package requires --allow-system; skipping: $package"
        continue
      fi
      if is_disabled "$package"; then
        echo "Already disabled; leaving unchanged: $package"
        continue
      fi
      if [[ "$confirmed" != true ]]; then
        echo "Would disable $package"
        continue
      fi
      echo "Disabling $package"
      adb -s "$serial" shell pm disable-user --user 0 "$package"
      grep -Fxq -- "$package" "$state_file" || printf '%s\n' "$package" >> "$state_file"
    done < "$packages_file"
    ;;
  restore)
    [[ -f "$state_file" ]] || { echo "No recorded package changes for $serial"; exit 0; }
    while IFS= read -r package || [[ -n "$package" ]]; do
      [[ -n "$package" ]] || continue
      if is_installed "$package" && is_disabled "$package"; then
        echo "Re-enabling $package"
        adb -s "$serial" shell pm enable --user 0 "$package"
      else
        echo "Already enabled or no longer installed: $package"
      fi
    done < "$state_file"
    mv "$state_file" "$state_file.restored.$(date +%Y%m%d%H%M%S)"
    ;;
esac
