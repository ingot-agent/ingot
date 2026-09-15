#!/usr/bin/env sh
# Install an official ingot core binary from GitHub Releases.
set -eu

release_base=https://github.com/ingot-agent/ingot/releases
prefix=
bindir=
destdir=
version=
force=false
staged_target=

usage() {
	cat <<'EOF'
usage: install.sh [options]

options:
  --prefix DIR       install prefix (default: ~/.local)
  --bindir DIR       binary directory (default: <prefix>/bin)
  --destdir DIR      staging root prepended to the binary directory
  --version VERSION  exact release version, with or without a v prefix
  --force            reinstall the same version or allow a downgrade
  -h, --help         show this help

The installer only installs the ingot core binary. It does not initialize or
modify INGOT_HOME, plugins, Images, or Runtimes.
EOF
}

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
		--destdir)
			[ "$#" -ge 2 ] || { echo "install.sh: --destdir requires a value" >&2; exit 2; }
			destdir=$2
			shift 2
			;;
		--version)
			[ "$#" -ge 2 ] || { echo "install.sh: --version requires a value" >&2; exit 2; }
			version=$2
			shift 2
			;;
		--force)
			force=true
			shift
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

if [ -z "$bindir" ]; then
	if [ -z "$prefix" ]; then
		[ -n "${HOME:-}" ] || { echo "install.sh: HOME is required unless --prefix or --bindir is set" >&2; exit 1; }
		prefix=$HOME/.local
	fi
	bindir=$prefix/bin
fi

command -v curl >/dev/null 2>&1 || { echo "install.sh: curl is required" >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "install.sh: tar is required" >&2; exit 1; }

temporary=$(mktemp -d "${TMPDIR:-/tmp}/ingot-install.XXXXXX")
trap 'rm -rf "$temporary"; [ -z "$staged_target" ] || rm -f "$staged_target"' EXIT HUP INT TERM

download() {
	url=$1
	destination=$2
	curl --proto '=https' --tlsv1.2 -fsSL --retry 3 --retry-delay 1 "$url" -o "$destination"
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		echo "install.sh: sha256sum, shasum, or openssl is required" >&2
		return 1
	fi
}

verify_file() {
	file=$1
	name=$2
	expected=$(awk -v name="$name" '$2 == name || $2 == "*" name { print $1; exit }' "$temporary/checksums.txt")
	[ -n "$expected" ] || { echo "install.sh: no checksum published for $name" >&2; return 1; }
	actual=$(sha256_file "$file")
	[ "$actual" = "$expected" ] || { echo "install.sh: SHA-256 mismatch for $name" >&2; return 1; }
}

canonical_tag() {
	LC_ALL=C awk -v value="$1" '
function core_number(part) { return part ~ /^(0|[1-9][0-9]*)$/ }
BEGIN {
	if (substr(value, 1, 1) == "v") value = substr(value, 2)
	if (value == "" || index(value, "+")) exit 1
	dash = index(value, "-")
	core = dash ? substr(value, 1, dash - 1) : value
	pre = dash ? substr(value, dash + 1) : ""
	if (split(core, parts, ".") != 3) exit 1
	for (i = 1; i <= 3; i++) if (!core_number(parts[i])) exit 1
	if (dash) {
		if (pre == "") exit 1
		count = split(pre, identifiers, ".")
		for (i = 1; i <= count; i++) {
			identifier = identifiers[i]
			if (identifier !~ /^[0-9A-Za-z-]+$/) exit 1
			if (identifier ~ /^[0-9]+$/ && length(identifier) > 1 && substr(identifier, 1, 1) == "0") exit 1
		}
	}
	print "v" value
}' </dev/null
}

semver_compare() {
	LC_ALL=C awk -v left="$1" -v right="$2" '
function numeric(value) { return value ~ /^[0-9]+$/ }
function compare_numeric(left_number, right_number) {
	if (length(left_number) < length(right_number)) return -1
	if (length(left_number) > length(right_number)) return 1
	if (left_number == right_number) return 0
	return left_number < right_number ? -1 : 1
}
BEGIN {
	sub(/^v/, "", left); sub(/^v/, "", right)
	left_core = left; sub(/-.*/, "", left_core)
	right_core = right; sub(/-.*/, "", right_core)
	left_pre = index(left, "-") ? substr(left, index(left, "-") + 1) : ""
	right_pre = index(right, "-") ? substr(right, index(right, "-") + 1) : ""
	split(left_core, lc, "."); split(right_core, rc, ".")
	for (i = 1; i <= 3; i++) {
		comparison = compare_numeric(lc[i], rc[i])
		if (comparison != 0) { print comparison; exit }
	}
	if (left_pre == "" && right_pre == "") { print 0; exit }
	if (left_pre == "") { print 1; exit }
	if (right_pre == "") { print -1; exit }
	ln = split(left_pre, lp, "."); rn = split(right_pre, rp, ".")
	n = ln > rn ? ln : rn
	for (i = 1; i <= n; i++) {
		if (i > ln) { print -1; exit }
		if (i > rn) { print 1; exit }
		if (lp[i] == rp[i]) continue
		lnumeric = numeric(lp[i]); rnumeric = numeric(rp[i])
		if (lnumeric && rnumeric) { print compare_numeric(lp[i], rp[i]); exit }
		if (lnumeric) { print -1; exit }
		if (rnumeric) { print 1; exit }
		print (lp[i] < rp[i]) ? -1 : 1; exit
	}
	print 0
}' </dev/null
}

case $(uname -s) in
	Linux) goos=linux ;;
	Darwin) goos=darwin ;;
	*) echo "install.sh: unsupported operating system $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
	x86_64|amd64) goarch=amd64 ;;
	arm64|aarch64) goarch=arm64 ;;
	*) echo "install.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "$version" ]; then
	download "$release_base/latest/download/VERSION" "$temporary/version-hint"
	version_hint=$(tr -d '\r\n' <"$temporary/version-hint")
	tag=$(canonical_tag "$version_hint") || { echo "install.sh: invalid release version $version_hint" >&2; exit 1; }
else
	tag=$(canonical_tag "$version") || { echo "install.sh: invalid release version $version" >&2; exit 2; }
fi

exact_base=$release_base/download/$tag
download "$exact_base/VERSION" "$temporary/VERSION"
published_tag=$(tr -d '\r\n' <"$temporary/VERSION")
[ "$(canonical_tag "$published_tag" 2>/dev/null || true)" = "$published_tag" ] || { echo "install.sh: invalid published release version $published_tag" >&2; exit 1; }
[ "$published_tag" = "$tag" ] || { echo "install.sh: release VERSION is $published_tag, expected $tag" >&2; exit 1; }
download "$exact_base/checksums.txt" "$temporary/checksums.txt"
verify_file "$temporary/VERSION" VERSION

asset=ingot-$tag-$goos-$goarch.tar.gz
download "$exact_base/$asset" "$temporary/$asset"
verify_file "$temporary/$asset" "$asset"
mkdir "$temporary/extract"
tar -tzf "$temporary/$asset" >"$temporary/archive-entries"
LC_ALL=C awk '
BEGIN { binary = 0; license = 0; total = 0; bad = 0 }
{
	total++
	if ($0 == "ingot") binary++
	else if ($0 == "LICENSE") license++
	else bad = 1
}
END { if (bad || total != 2 || binary != 1 || license != 1) exit 1 }
' "$temporary/archive-entries" || { echo "install.sh: release archive has unexpected entries" >&2; exit 1; }
tar -xzf "$temporary/$asset" -C "$temporary/extract" ingot LICENSE
candidate=$temporary/extract/ingot
[ -f "$candidate" ] && [ ! -L "$candidate" ] || { echo "install.sh: release archive does not contain a regular ingot binary" >&2; exit 1; }
chmod 0755 "$candidate"
release_version=${tag#v}
candidate_version=$($candidate --version)
[ "$candidate_version" = "ingot $release_version" ] || { echo "install.sh: candidate reports $candidate_version, expected ingot $release_version" >&2; exit 1; }

target_dir=$destdir$bindir
target=$target_dir/ingot
if [ -x "$target" ]; then
	current_output=$(INGOT_HOME=$temporary/legacy-home "$target" --version 2>/dev/null || true)
	case "$current_output" in
		ingot\ *)
			current_version=${current_output#ingot }
			if current_tag=$(canonical_tag "$current_version" 2>/dev/null); then
				comparison=$(semver_compare "$release_version" "${current_tag#v}")
				if [ "$comparison" -eq 0 ] && ! $force; then
					echo "ingot $release_version is already installed at $target"
					exit 0
				fi
				if [ "$comparison" -lt 0 ] && ! $force; then
					echo "install.sh: refusing to downgrade ingot $current_version to $release_version without --force" >&2
					exit 1
				fi
			fi
			;;
	esac
fi

mkdir -p "$target_dir"
staged_target=$target.tmp.$$
install -m 0755 "$candidate" "$staged_target"
mv -f "$staged_target" "$target"
staged_target=

echo "ingot $release_version installed to $target"
if [ -z "$destdir" ]; then
	case :${PATH:-}: in
		*:$bindir:*) ;;
		*)
			echo "$bindir is not in PATH. Add it to your shell configuration:"
			echo "  export PATH=\"$bindir:\$PATH\""
			;;
	esac
fi
