#!/bin/sh
set -eu

repository="fazaimron27/cliamp-plugin-discord-rpc"
version="${CLIAMP_RPC_VERSION:-v1.5.0}"
bin_dir="${CLIAMP_RPC_BIN_DIR:-${HOME:?HOME is not set}/.local/bin}"
service_dir="${CLIAMP_RPC_SERVICE_DIR:-${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user}"
script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
tmp_dir=""

usage() {
  cat <<'EOF'
Usage: install.sh [options]

Install the Cliamp Discord RPC daemon and systemd user service.

Options:
  --version VERSION       Release version to install (default: v1.5.0)
  --bin-dir DIRECTORY     Daemon installation directory (default: ~/.local/bin)
  --service-dir DIRECTORY systemd user unit directory
  -h, --help              Show this help

The CLIAMP_RPC_VERSION, CLIAMP_RPC_BIN_DIR, and CLIAMP_RPC_SERVICE_DIR
environment variables provide the same settings.
EOF
}

fail() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$tmp_dir" ]; then
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT HUP INT TERM

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      version=$2
      shift 2
      ;;
    --bin-dir)
      [ "$#" -ge 2 ] || fail "--bin-dir requires a value"
      bin_dir=$2
      shift 2
      ;;
    --service-dir)
      [ "$#" -ge 2 ] || fail "--service-dir requires a value"
      service_dir=$2
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

source_dir=$script_dir
installed_version=$version
if [ -f "$source_dir/cliamp-rpcd" ] && [ -f "$source_dir/cliamp-rpcd.service" ]; then
  package_name=$(basename "$source_dir")
  case "$package_name" in
    cliamp-plugin-discord-rpc_v*_linux_*)
      installed_version=${package_name#cliamp-plugin-discord-rpc_}
      installed_version=${installed_version%_linux_*}
      ;;
  esac
fi

version_numbers=${version#v}
[ "$version_numbers" != "$version" ] || fail "invalid release version: $version"
# Split the dotted version into exactly three numeric components.
old_ifs=$IFS
IFS=.
# shellcheck disable=SC2086
set -- $version_numbers
IFS=$old_ifs
[ "$#" -eq 3 ] || fail "invalid release version: $version"
for component do
  case "$component" in
    ''|*[!0-9]*) fail "invalid release version: $version" ;;
  esac
done

if [ ! -f "$source_dir/cliamp-rpcd" ] || [ ! -f "$source_dir/cliamp-rpcd.service" ]; then
  for command in awk curl gh sha256sum tar uname mktemp; do
    command -v "$command" >/dev/null 2>&1 || fail "required command not found: $command"
  done

  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac

  package="cliamp-plugin-discord-rpc_${version}_linux_${arch}"
  archive="${package}.tar.gz"
  base_url="https://github.com/${repository}/releases/download/${version}"
  tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/cliamp-rpc-install.XXXXXX")

  printf 'Downloading %s...\n' "$archive"
  curl -fL --retry 3 -o "$tmp_dir/$archive" "$base_url/$archive"
  curl -fL --retry 3 -o "$tmp_dir/checksums.txt" "$base_url/checksums.txt"
  gh attestation verify "$tmp_dir/$archive" \
    --repo "$repository" \
    --signer-workflow "$repository/.github/workflows/release.yml" \
    --source-ref "refs/tags/$version" \
    --deny-self-hosted-runners \
    || fail "release provenance verification failed for $archive"

  expected=$(awk -v name="$archive" '$2 == name || $2 == "./" name { print $1; exit }' "$tmp_dir/checksums.txt")
  [ -n "$expected" ] || fail "checksum not found for $archive"
  printf '%s  %s\n' "$expected" "$tmp_dir/$archive" | sha256sum -c -

  tar -xzf "$tmp_dir/$archive" -C "$tmp_dir"
  source_dir="$tmp_dir/$package"
fi

[ -x "$source_dir/cliamp-rpcd" ] || fail "cliamp-rpcd is missing or not executable"
[ -f "$source_dir/cliamp-rpcd.service" ] || fail "cliamp-rpcd.service is missing"
command -v install >/dev/null 2>&1 || fail "required command not found: install"

install -Dm755 "$source_dir/cliamp-rpcd" "$bin_dir/cliamp-rpcd"
install -Dm644 "$source_dir/cliamp-rpcd.service" "$service_dir/cliamp-rpcd.service"

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user daemon-reload
else
  printf 'systemctl not found; reload your systemd user manager manually.\n' >&2
fi

printf '\nInstalled cliamp-rpcd %s\n' "$installed_version"
printf '  Daemon:  %s/cliamp-rpcd\n' "$bin_dir"
printf '  Service: %s/cliamp-rpcd.service\n' "$service_dir"
printf 'The user service was installed but not enabled or started.\n'
printf 'Configure ~/.config/cliamp/config.toml, then run manually:\n'
printf '  %s/cliamp-rpcd\n' "$bin_dir"
printf 'Or enable the user service:\n'
printf '  systemctl --user enable --now cliamp-rpcd.service\n'
