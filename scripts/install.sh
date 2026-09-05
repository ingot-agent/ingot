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
# After installation the script runs `ingot init`, collects model provider
# settings (from the INGOT_* environment variables or interactively), runs
# `ingot apply` to build the runtime image, and offers to start the web UI.
#
# Usage:
#   ./scripts/install.sh                          # -> /usr/local, one-command setup
#   ./scripts/install.sh --prefix ~/.local        # -> ~/.local/bin, ~/.local/share/ingot
#   DESTDIR=./pkg ./scripts/install.sh            # staged packaging (no init/apply)
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
  --no-apply         init only; do not build the runtime image
  --no-open          do not open the web UI after apply
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
if [ -f "$home/plugins.toml" ]; then
	echo "==> home $home is already initialized; skipping init"
else
	echo "==> initializing ingot home $home (profile: $profile)"
	mkdir -p "$home"
	"$ingot_bin" --home "$home" init --profile "$profile" --bundle "$sharedir/plugins"
fi

# ---------------------------------------------------------------------------
# 2. model provider configuration
# ---------------------------------------------------------------------------
config="$home/config.toml"
configured=false
if [ -f "$config" ] && grep -q 'api_key = ""' "$config"; then
	configured=false
else
	configured=true
fi

if $no_configure || $configured; then
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
		echo "  (set INGOT_API_KEY and re-run, or edit $home/config.toml manually)" >&2
	elif command -v python3 >/dev/null 2>&1; then
		# Preferred path: python3 renders TOML values correctly (\ and " escaping).
		python3 - "$config" "$provider_name" "$base_url" "$api_key" "$model" <<'PY'
import json, sys
config, provider, base_url, api_key, model = sys.argv[1:6]
s = open(config, encoding='utf-8').read()
t = lambda v: json.dumps(v, ensure_ascii=False)  # JSON string escaping is TOML-compatible
s = s.replace('name = "openai"',                 'name = ' + t(provider), 1)
s = s.replace('base_url = "https://api.example.com/v1"', 'base_url = ' + t(base_url), 1)
s = s.replace('api_key = ""',                    'api_key = ' + t(api_key), 1)
s = s.replace('models = ["gpt-4o-mini"]',       'models = [' + t(model) + ']', 1)
s = s.replace('default_provider = "openai"',    'default_provider = ' + t(provider), 1)
s = s.replace('default_model = "gpt-4o-mini"',   'default_model = ' + t(model), 1)
open(config, 'w', encoding='utf-8').write(s)
PY
		echo "==> wrote provider ${provider_name} (${model}) to $config"
	else
		# Fallback: plain sed works for values without \ " & | characters.
		case "$provider_name$base_url$api_key$model" in
			*'\\'*|*'"'*|*'&'*|*'|'*)
				echo "install.sh: value contains characters the fallback writer cannot handle; edit $config manually" >&2
				;;
			*)
				sed -i \
					-e "s|name = \"openai\"|name = \"$provider_name\"|" \
					-e "s|base_url = \"https://api.example.com/v1\"|base_url = \"$base_url\"|" \
					-e "s|api_key = \"\"|api_key = \"$api_key\"|" \
					-e "s|models = \[\"gpt-4o-mini\"\]|models = [\"$model\"]|" \
					-e "s|default_provider = \"openai\"|default_provider = \"$provider_name\"|" \
					-e "s|default_model = \"gpt-4o-mini\"|default_model = \"$model\"|" \
					"$config"
				echo "==> wrote provider ${provider_name} (${model}) to $config"
				;;
		esac
	fi
fi

# ---------------------------------------------------------------------------
# 3. apply
# ---------------------------------------------------------------------------
if $no_apply; then
	echo "==> skipping apply (--no-apply); run later: $ingot_bin --home \"$home\" apply"
else
	echo "==> building runtime image (first build downloads modules and may take a few minutes)"
	apply_attempts=0
	until "$ingot_bin" --home "$home" apply; do
		apply_attempts=$((apply_attempts + 1))
		if [ "$apply_attempts" -ge 2 ]; then
			echo "install.sh: apply failed twice; re-run this script after checking network access" >&2
			exit 1
		fi
		echo "==> retrying apply (attempt $((apply_attempts + 1)))"
		sleep 2
	done
	echo "==> active image ready"
fi

# ---------------------------------------------------------------------------
# 4. start
# ---------------------------------------------------------------------------
launch_web() {
	if [ -t 0 ]; then
		printf '\nStart the web UI now? [Y/n]: '
		read -r input
		case "$input" in
			n|N|no|NO) return ;;
		esac
	fi
	mkdir -p "$1"
	echo '==> starting web UI in the background (log: '"$1/web.log"')'
	"$ingot_bin" --home "$1" web >"$1/web.log" 2>&1 &
	web_pid=$!
	sleep 1
	if kill -0 "$web_pid" 2>/dev/null; then
		echo "    listening on http://127.0.0.1:7316/ (pid $web_pid)"
		if ! $no_open && [ -n "${DISPLAY:-}" ] && command -v xdg-open >/dev/null 2>&1; then
			(xdg-open http://127.0.0.1:7316/ >/dev/null 2>&1 || true) &
		fi
	else
		echo "    web UI did not start; inspect $1/web.log" >&2
	fi
}

if [ -z "$no_apply" ]; then
	launch_web "$home"
else
	echo
	echo "Agent home is ready. Next steps:"
	echo "  $ingot_bin --home \"$home\" apply"
	echo "  $ingot_bin --home \"$home\" web   # then open http://127.0.0.1:7316/"
fi
