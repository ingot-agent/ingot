#!/usr/bin/env sh
# install.sh — build and install the ingot CLI and its official plugin set.
#
# The ingot binary embeds no plugin sources: the official plugins are
# distributed as directory trees next to the binary (this repository keeps
# them under plugins/) and `ingot init` locates them during installation.
# This script installs both the binary and the plugin tree in a standard
# layout:
#
#   <prefix>/bin/ingot
#   <prefix>/share/ingot/plugins/<plugin>/...
#
# Usage:
#   ./scripts/install.sh                       # -> /usr/local
#   ./scripts/install.sh --prefix ~/.local     # -> ~/.local/bin, ~/.local/share/ingot
#   DESTDIR=./pkg ./scripts/install.sh         # staged packaging
set -eu

usage() {
	cat <<'EOF'
usage: ./scripts/install.sh [options]

options:
  --prefix DIR    install prefix (default: /usr/local)
  --bindir DIR    binary directory (default: <prefix>/bin)
  --sharedir DIR  plugin share directory (default: <prefix>/share/ingot)
  --destdir DIR   staging root prepended to all paths (default: empty)
  -h, --help      show this help
EOF
}

prefix=/usr/local
bindir=
sharedir=
destdir=

while [ "$#" -gt 0 ]; do
	case "$1" in
		--prefix)
			[ "$#" -ge 2 ] || { echo "install.sh: --prefix requires a value" >&2; exit 2; }
			prefix=$2
			shift 2
			;;
		--bindir)
			[ "$#" -ge 2 ] || { echo "install.sh: --bindir requires a value" >&2; exit 2; }
			bindir=$2
			shift 2
			;;
		--sharedir)
			[ "$#" -ge 2 ] || { echo "install.sh: --sharedir requires a value" >&2; exit 2; }
			sharedir=$2
			shift 2
			;;
		--destdir)
			[ "$#" -ge 2 ] || { echo "install.sh: --destdir requires a value" >&2; exit 2; }
			destdir=$2
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "install.sh: unknown option $1" >&2
			usage >&2
			exit 2
			;;
	esac
done

[ -n "$bindir" ] || bindir="$prefix/bin"
[ -n "$sharedir" ] || sharedir="$prefix/share/ingot"

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
command -v go >/dev/null 2>&1 || { echo "install.sh: go 1.24+ is required to build ingot" >&2; exit 1; }
[ -f "$root/go.mod" ] || { echo "install.sh: cannot locate the ingot source tree at $root" >&2; exit 1; }
[ -d "$root/plugins" ] || { echo "install.sh: the official plugin set (plugins/) is missing from $root" >&2; exit 1; }

temporary=$(mktemp -d "${TMPDIR:-/tmp}/ingot-install.XXXXXX")
trap 'rm -rf "$temporary"' EXIT

echo "==> building ingot"
(cd "$root" && go build -trimpath -o "$temporary/ingot" ./cmd/ingot)

echo "==> installing to $destdir$bindir" 
mkdir -p "$destdir$bindir" "$destdir$sharedir/plugins"
install -m 0755 "$temporary/ingot" "$destdir$bindir/ingot"

echo "==> installing official plugins to $destdir$sharedir/plugins"
# Copy the whole plugin tree; VCS/editor metadata never enters the bundle
# identity, but exclude it anyway to keep the install clean.
if command -v rsync >/dev/null 2>&1; then
	rsync -a --exclude '.git' --exclude '.hg' --exclude '.svn' --exclude '.idea' --exclude '.vscode' "$root/plugins/" "$destdir$sharedir/plugins/"
else
	(cd "$root" && tar -cf - plugins) | (cd "$destdir$sharedir" && tar -xf -)
fi

echo
echo "ingot installed:"
echo "  binary:  $destdir$bindir/ingot"
echo "  plugins: $destdir$sharedir/plugins"
echo
echo "Next steps:"
echo "  1. Run: ingot init"
echo "  2. Edit your ingot home config.toml (model provider settings)."
echo "  3. Run: ingot apply"
echo "  4. Run: ingot chat"
