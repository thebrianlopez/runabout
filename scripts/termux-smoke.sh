#!/usr/bin/env bash
set -euo pipefail

repo_root="${1:-}"
package_path="${2:-}"
root_dir="${3:-}"
key_path="${4:-}"

if [[ -z "$repo_root" || -z "$package_path" || -z "$root_dir" ]]; then
  echo "usage: termux-smoke.sh <repo-root> <package.deb> <termux-root> [key.gpg]" >&2
  exit 2
fi

if [[ ! -f "$package_path" ]]; then
  echo "termux smoke: package not found: $package_path" >&2
  exit 2
fi

pkg_name="$(dpkg-deb -f "$package_path" Package)"
pkg_arch="$(dpkg-deb -f "$package_path" Architecture)"
pkg_filename="$(basename "$package_path")"
pkg_copy="$(mktemp /tmp/termux-smoke-package.XXXXXX.deb)"
cp "$package_path" "$pkg_copy"
key_copy=""
if [[ -n "$key_path" ]]; then
  key_copy="$(mktemp /tmp/termux-smoke-key.XXXXXX.gpg)"
  cp "$key_path" "$key_copy"
fi
pool_dir="$repo_root/pool/main/r/$pkg_name"
dist_dir="$repo_root/dists/stable/main/binary-$pkg_arch"
termux_prefix="$root_dir/data/data/com.termux/files/usr"
apt_lists_dir="$root_dir/var/lib/apt/lists"
apt_cache_dir="$root_dir/var/cache/apt/archives"
dpkg_status_dir="$root_dir/var/lib/dpkg"
source_list="$root_dir/etc/apt/sources.list"
packages_path="$dist_dir/Packages"
release_path="$repo_root/dists/stable/Release"

rm -rf "$repo_root" "$root_dir"
mkdir -p "$pool_dir" "$dist_dir" "$termux_prefix/bin" "$root_dir/etc/apt" "$apt_lists_dir/partial" "$apt_cache_dir/partial" "$dpkg_status_dir"
: > "$dpkg_status_dir/status"
cp "$pkg_copy" "$pool_dir/$pkg_filename"

python3 - "$pkg_copy" "$pool_dir/$pkg_filename" "$packages_path" "$release_path" "$pkg_name" "$pkg_arch" <<'PY'
import gzip
import hashlib
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

src = Path(sys.argv[1])
pkg = Path(sys.argv[2])
packages_path = Path(sys.argv[3])
release_path = Path(sys.argv[4])
pkg_name = sys.argv[5]
pkg_arch = sys.argv[6]

control = subprocess.check_output(["dpkg-deb", "-f", str(src)], text=True)
if control and not control.endswith("\n"):
    control += "\n"

size = pkg.stat().st_size
md5 = hashlib.md5(pkg.read_bytes()).hexdigest()
sha256 = hashlib.sha256(pkg.read_bytes()).hexdigest()
packages_text = (
    f"{control}Filename: pool/main/r/{pkg_name}/{pkg.name}\n"
    f"Size: {size}\n"
    f"MD5sum: {md5}\n"
    f"SHA256: {sha256}\n\n"
)
packages_path.write_text(packages_text, encoding="utf-8")
packages_gz = packages_path.with_suffix(".gz")
with gzip.open(packages_gz, "wb") as fh:
    fh.write(packages_text.encode("utf-8"))

checksums = []
for digest_name, digest_fn in (("MD5Sum", hashlib.md5), ("SHA256", hashlib.sha256)):
    for target in (packages_path, packages_gz):
        checksums.append(
            f" {digest_fn(target.read_bytes()).hexdigest()} {target.stat().st_size} "
            f"main/binary-{pkg_arch}/{target.name}"
        )

release_path.write_text(
    "\n".join(
        [
            "Origin: runabout",
            "Label: runabout",
            "Suite: stable",
            "Codename: stable",
            f"Date: {datetime.now(timezone.utc).strftime('%a, %d %b %Y %H:%M:%S UTC')}",
            f"Architectures: {pkg_arch}",
            "Components: main",
            "Description: runabout Termux repository",
            "MD5Sum:",
            checksums[0],
            checksums[1],
            "SHA256:",
            checksums[2],
            checksums[3],
            "",
        ]
    ),
    encoding="utf-8",
)
PY

cat > "$source_list" <<EOF
deb [trusted=yes] file:$repo_root stable main
EOF

if [[ -n "$key_copy" ]]; then
  tmp_gnupg="$(mktemp -d)"
  chmod 700 "$tmp_gnupg"
  GNUPGHOME="$tmp_gnupg" gpg --batch --import "$key_copy" >/dev/null 2>&1
  rm -rf "$tmp_gnupg"
fi

apt_opts=(
  -o Dir::Etc::sourcelist="$source_list"
  -o Dir::Etc::sourceparts="-"
  -o Dir::State::status="$dpkg_status_dir/status"
  -o Dir::State::lists="$apt_lists_dir"
  -o Dir::Cache::archives="$apt_cache_dir"
  -o APT::Get::List-Cleanup="0"
  -o APT::Architectures="$pkg_arch"
  -o DPkg::Options::="--root=$root_dir"
  -o DPkg::Options::="--force-architecture"
)

apt-get "${apt_opts[@]}" update
apt-get "${apt_opts[@]}" -y install "$pkg_name"

installed_bin="$termux_prefix/bin/mdq"
if [[ ! -x "$installed_bin" ]]; then
  echo "termux smoke: expected binary missing: $installed_bin" >&2
  exit 1
fi

"$installed_bin" --version
