#!/usr/bin/env sh
# install.sh — install ingot and its official plugin set, then prepare a
# ready-to-use agent in one command.
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
# After installation the script initializes a new home or refreshes the
# official bundle in an existing home, collects model provider settings (from
# the INGOT_* environment variables or interactively), builds a named image,
# creates the `default` Runtime, and offers to start the web UI.
#
# Usage:
#   ./scripts/install.sh                          # -> /usr/local, one-command setup
#   ./scripts/install.sh --prefix ~/.local        # -> ~/.local/bin, ~/.local/share/ingot
#   DESTDIR=./pkg ./scripts/install.sh            # staged packaging (no init/build)
#   INGOT_API_KEY=sk-... INGOT_BASE_URL=https://api.example.com/v1 \
#     INGOT_MODEL=gpt-4o-mini ./scripts/install.sh   # non-interactive
set -eu

usage() {
	cat <<'EOF'
usage: ./scripts/install.sh [options]

options:
  --prefix DIR       install prefix (default: /usr/local)
  --bindir DIR       binary directory (default: <prefix>/bin)
  --sharedir DIR     plugin share directory (default: <prefix>/share/ingot)
  --destdir DIR      staging root prepended to all paths (default: empty)
  --home PATH        ingot home directory (default: ~/.ingot)
  --profile NAME     bundle profile: default (web UI) or minimal (default: default)
  --no-configure     skip model provider configuration
  --no-apply         legacy alias: skip image build and Runtime creation
  --no-open          do not open the web UI after start
  -h, --help         show this help

Model provider settings, when not provided interactively:
  INGOT_PROVIDER_NAME  provider display name (default: openai)
  INGOT_BASE_URL       OpenAI-compatible base URL (default: https://api.openai.com/v1)
  INGOT_API_KEY        API key
  INGOT_MODEL          model name (default: gpt-4o-mini)
EOF
}

prefix=/usr/local
bindir=
sharedir=
destdir=
home=
profile=default
no_configure=false
no_apply=false
no_open=false

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
		--home)
			[ "$#" -ge 2 ] || { echo "install.sh: --home requires a value" >&2; exit 2; }
			home=$2
			shift 2
			;;
		--profile)
			[ "$#" -ge 2 ] || { echo "install.sh: --profile requires a value" >&2; exit 2; }
			profile=$2
			shift 2
			;;
		--no-configure)
			no_configure=true
			shift
			;;
		--no-apply)
			no_apply=true
			shift
			;;
		--no-open)
			no_open=true
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

[ -n "$bindir" ] || bindir="$prefix/bin"
[ -n "$sharedir" ] || sharedir="$prefix/share/ingot"
[ -n "$home" ] || home="${INGOT_HOME:-$(printf '%s' "${HOME:-$USERPROFILE}/.ingot")}"
[ "$profile" = "default" ] || [ "$profile" = "minimal" ] || {
	echo "install.sh: unknown profile $profile (available: default, minimal)" >&2
	exit 2
}

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

# A staged packaging run (DESTDIR) cannot touch the real home; stop here.
if [ -n "$destdir" ]; then
	echo "Staged packaging complete (DESTDIR set). To prepare a usable home:"
	echo "  $bindir/ingot --home \"$home\" init --profile $profile --bundle \"$sharedir/plugins\""
	exit 0
fi

ingot_bin="$bindir/ingot"
[ -x "$ingot_bin" ] || { echo "install.sh: installed binary not found at $ingot_bin" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 1. init
# ---------------------------------------------------------------------------
echo "==> initializing or refreshing ingot home $home (profile: $profile)"
mkdir -p "$home"
"$ingot_bin" --home "$home" init --profile "$profile" --bundle "$sharedir/plugins"

image_ref=local/ingot:default
runtime_name=default
profile_recipe="$home/profiles/${profile}.toml"
profile_lock="$home/profiles/${profile}.lock"
if $no_apply; then
	echo "==> skipping image build and Runtime creation (--no-apply legacy alias)"
else
	echo "==> building runtime image (first build downloads modules and may take a few minutes)"
	build_attempts=0
	until "$ingot_bin" --home "$home" build --use "$profile_recipe" --lock "$profile_lock" --tag "$image_ref"; do
		build_attempts=$((build_attempts + 1))
		if [ "$build_attempts" -ge 2 ]; then
			echo "install.sh: build failed twice; re-run this script after checking network access" >&2
			exit 1
		fi
		echo "==> retrying build"
		sleep 2
	done
	if "$ingot_bin" --home "$home" runtime inspect "$runtime_name" >/dev/null 2>&1; then
		"$ingot_bin" --home "$home" runtime switch "$runtime_name" "$image_ref"
	else
		"$ingot_bin" --home "$home" runtime create "$runtime_name" --image "$image_ref" -- web
	fi
fi

# ---------------------------------------------------------------------------
# 2. model provider configuration
# ---------------------------------------------------------------------------
# Plugins own their persistent configuration inside the Runtime Home; there is
# no shared runtime config.toml. The model provider is configured by writing
# the provider plugin's own state file before the first run.
provider_dir="$home/runtimes/$runtime_name/state/model.openai-compatible"
runtime_dir="$home/runtimes/$runtime_name/state/model.runtime"
config="$provider_dir/config.toml"
defaults="$runtime_dir/config.toml"
configured=false
if [ -f "$config" ] && ! grep -q 'api_key = ""' "$config"; then
	configured=true
fi

if $no_apply || $no_configure || $configured; then
	:
else
	echo "==> model provider configuration"
	provider_name=${INGOT_PROVIDER_NAME:-openai}
	base_url=${INGOT_BASE_URL:-}
	api_key=${INGOT_API_KEY:-}
	model=${INGOT_MODEL:-}

	if [ -t 0 ]; then
		printf 'provider name [%s]: ' "$provider_name"
		read -r input; [ -n "${input:-}" ] && provider_name=$input
		printf 'base URL (OpenAI-compatible) [%s]: ' "${base_url:-https://api.openai.com/v1}"
		read -r input; [ -n "${input:-}" ] && base_url=$input
		[ -n "$base_url" ] || base_url="https://api.openai.com/v1"
		if [ -z "$api_key" ]; then
			printf 'API key: '
			read -r input
			api_key=$input
		fi
		printf 'model [%s]: ' "$model"
		read -r input; [ -n "${input:-}" ] && model=$input
		[ -n "$model" ] || model="gpt-4o-mini"
	else
		[ -n "$base_url" ] || base_url="https://api.openai.com/v1"
		[ -n "$model" ] || model="gpt-4o-mini"
	fi

	if [ -z "$api_key" ]; then
		echo "install.sh: no API key provided; skipping configuration" >&2
		echo "  (set INGOT_API_KEY and re-run, or write $config manually)" >&2
	elif command -v python3 >/dev/null 2>&1; then
		# Preferred path: python3 renders TOML values correctly (\ and " escaping).
		python3 - "$config" "$defaults" "$provider_name" "$base_url" "$api_key" "$model" <<'PY'
import json, os, sys
config, defaults, provider, base_url, api_key, model = sys.argv[1:7]
t = lambda v: json.dumps(v, ensure_ascii=False)  # JSON string escaping is TOML-compatible
for path in (config, defaults):
    directory = os.path.dirname(path)
    if directory:
        os.makedirs(directory, exist_ok=True)
with open(config, 'w', encoding='utf-8') as handle:
    handle.write('providers = [\n  { name = ' + t(provider) + ', base_url = ' + t(base_url) + ', api_key = ' + t(api_key) + ', models = [' + t(model) + '] },\n]\n')
with open(defaults, 'w', encoding='utf-8') as handle:
    handle.write('default_provider = ' + t(provider) + '\ndefault_model = ' + t(model) + '\n')
PY
		echo "==> wrote provider ${provider_name} (${model}) to $config"
	else
		# Fallback: plain printf works for values without \ " & | characters.
		case "$provider_name$base_url$api_key$model" in
			*'\\'*|*'"'*|*'&'*|*'|'*)
				echo "install.sh: value contains characters the fallback writer cannot handle; write $config manually" >&2
				;;
			*)
				mkdir -p "$provider_dir" "$runtime_dir"
				printf 'providers = [\n  { name = "%s", base_url = "%s", api_key = "%s", models = ["%s"] },\n]\n' \
					"$provider_name" "$base_url" "$api_key" "$model" >"$config"
				printf 'default_provider = "%s"\ndefault_model = "%s"\n' \
					"$provider_name" "$model" >"$defaults"
				echo "==> wrote provider ${provider_name} (${model}) to $config"
				;;
		esac
	fi
fi

# ---------------------------------------------------------------------------
# 3. start
# ---------------------------------------------------------------------------
launch_web() {
	if [ -t 0 ]; then
		printf '\nStart the web UI now? [Y/n]: '
		read -r input
		case "$input" in
			n|N|no|NO) return ;;
		esac
	fi
	echo '==> starting web UI in the background'
	if "$ingot_bin" --home "$1" runtime start "$runtime_name"; then
		echo "    listening on http://127.0.0.1:7316/"
		if ! $no_open && [ -n "${DISPLAY:-}" ] && command -v xdg-open >/dev/null 2>&1; then
			(xdg-open http://127.0.0.1:7316/ >/dev/null 2>&1 || true) &
		fi
	else
		echo "    web UI did not start; run: $ingot_bin --home \"$1\" runtime logs $runtime_name" >&2
	fi
}

if ! $no_apply; then
	launch_web "$home"
else
	echo
	echo "Agent home is ready. Next steps:"
	echo "  $ingot_bin --home \"$home\" build --use \"$profile_recipe\" --lock \"$profile_lock\" --tag $image_ref"
	echo "  $ingot_bin --home \"$home\" runtime create $runtime_name --image $image_ref -- web"
	echo "  $ingot_bin --home \"$home\" runtime start $runtime_name"
fi
