#!/bin/sh
set -eu

bin_dir="${CLIAMP_RPC_BIN_DIR:-${HOME:?HOME is not set}/.local/bin}"
service_dir="${CLIAMP_RPC_SERVICE_DIR:-${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user}"

usage() {
  cat <<'EOF'
Usage: uninstall.sh [options]

Remove the Cliamp Discord RPC daemon and systemd user service.

Options:
  --bin-dir DIRECTORY     Daemon installation directory (default: ~/.local/bin)
  --service-dir DIRECTORY systemd user unit directory
  -h, --help              Show this help

The CLIAMP_RPC_BIN_DIR and CLIAMP_RPC_SERVICE_DIR environment variables
provide the same settings as the corresponding options.

The Cliamp plugin, configuration, and playback state are not removed.
EOF
}

fail() {
  printf 'uninstall.sh: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ "$#" -ge 2 ] || fail "--bin-dir requires a value"
      bin_dir=$2
      [ -n "$bin_dir" ] || fail "--bin-dir cannot be empty"
      shift 2
      ;;
    --service-dir)
      [ "$#" -ge 2 ] || fail "--service-dir requires a value"
      service_dir=$2
      [ -n "$service_dir" ] || fail "--service-dir cannot be empty"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user disable --now cliamp-rpcd.service >/dev/null 2>&1 || true
else
  printf 'systemctl not found; stop any running cliamp-rpcd process manually.\n' >&2
fi

rm -f "$bin_dir/cliamp-rpcd" "$service_dir/cliamp-rpcd.service"

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload
fi

printf '\nRemoved %s/cliamp-rpcd\n' "$bin_dir"
printf 'Removed %s/cliamp-rpcd.service\n' "$service_dir"
printf 'Cliamp plugin, configuration, and playback state were preserved.\n'
