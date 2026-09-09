#!/usr/bin/env bash
# Publish ktui 0.1.1 and its Homebrew tap to github.com/razvanbalsan.
#
# Creates two public repos, pushes both, cuts the v0.1.1 release, generates the
# formula from the archives that actually ended up on that release, and verifies
# `brew install` works end to end.
#
# Prerequisites: gh, authenticated as razvanbalsan
#     brew install gh && gh auth login
# Go is optional. With it, the release archives are built here. Without it, the
# repo's GitHub Actions workflow builds them and this script waits for them.
#
# Run it from anywhere: hack/publish.sh. Safe to re-run.
#
# The Homebrew tap is expected as a checkout next to this repo; set TAP_DIR to
# point elsewhere. If it is missing it is cloned from $OWNER/homebrew-tap.

set -euo pipefail

OWNER=razvanbalsan
VERSION=0.1.1
TAG="v$VERSION"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
TAP_DIR="${TAP_DIR:-$(dirname "$REPO")/homebrew-tap}"
# Where the archives are downloaded back to for checksumming. Inside dist/, so
# it is gitignored and `make clean` takes it with the rest of the build output.
PUBLISHED="$REPO/dist/published"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn:\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

command -v gh >/dev/null || die "gh not found — brew install gh"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated — run: gh auth login"
who=$(gh api user -q .login)
[ "$who" = "$OWNER" ] || die "gh is authenticated as '$who', expected '$OWNER'"

push_repo() { # <dir> <name> <description>
	cd "$1"
	if gh repo view "$OWNER/$2" >/dev/null 2>&1; then
		info "$OWNER/$2 exists — pushing to it"
		git remote remove origin 2>/dev/null || true
		git remote add origin "https://github.com/$OWNER/$2.git"
		git push -u origin main
	else
		info "creating $OWNER/$2"
		gh repo create "$OWNER/$2" --public --source=. --remote=origin --push --description "$3"
	fi
}

# ---------------------------------------------------------------- 1. source
push_repo "$REPO" ktui \
	"Terminal UI for managing kubectl contexts — switch, rename, set namespace, and delete with cluster/user orphan cleanup"

# --------------------------------------------------------------- 2. release
cd "$REPO"
if gh release view "$TAG" --repo "$OWNER/ktui" >/dev/null 2>&1; then
	info "release $TAG already exists"
elif command -v go >/dev/null; then
	# Build here so the release is cut immediately and deterministically.
	# Creating the release against main also creates the tag server-side; the
	# workflow then sees archives already attached and leaves them alone.
	info "building release archives locally"
	make dist VERSION="$VERSION"
	info "cutting release $TAG"
	gh release create "$TAG" --repo "$OWNER/ktui" --target main --title "$TAG" \
		--notes "First release. macOS and Linux, arm64 and amd64.

    brew tap $OWNER/tap
    brew install ktui

Checksums are in checksums.txt." \
		dist/*.tar.gz dist/checksums.txt
else
	warn "go not installed — letting GitHub Actions build the archives"
	git push origin "$TAG"
	info "waiting for the release workflow (up to 10 minutes)"
	for i in $(seq 1 60); do
		if gh release view "$TAG" --repo "$OWNER/ktui" --json assets \
			-q '.assets[].name' 2>/dev/null | grep -q '\.tar\.gz$'; then
			break
		fi
		sleep 10
		printf '.'
	done
	echo
	gh release view "$TAG" --repo "$OWNER/ktui" --json assets \
		-q '.assets[].name' 2>/dev/null | grep -q '\.tar\.gz$' \
		|| die "the release workflow did not publish archives — check: gh run list --repo $OWNER/ktui"
fi

# ---------------------------------------------- 3. formula from the release
# The formula's checksums must match the bytes on the release, whoever built
# them, so always derive them from the published archives.
info "downloading the published archives to checksum them"
rm -rf "$PUBLISHED" && mkdir -p "$PUBLISHED"
gh release download "$TAG" --repo "$OWNER/ktui" --pattern '*.tar.gz' --dir "$PUBLISHED"
ls "$PUBLISHED"/*.tar.gz >/dev/null || die "no archives downloaded"

if [ ! -d "$TAP_DIR/.git" ]; then
	info "cloning $OWNER/homebrew-tap to $TAP_DIR"
	gh repo clone "$OWNER/homebrew-tap" "$TAP_DIR" \
		|| die "no tap checkout at $TAP_DIR — clone it there or set TAP_DIR"
fi

info "generating the formula"
sh "$REPO/hack/formula.sh" "$VERSION" "$OWNER/ktui" "$PUBLISHED" \
	> "$TAP_DIR/Formula/ktui.rb"

cd "$TAP_DIR"
if ! git diff --quiet -- Formula/ktui.rb; then
	git add Formula/ktui.rb
	git -c commit.gpgsign=false commit -q -m "Update ktui formula checksums to match the published $TAG archives"
fi

push_repo "$TAP_DIR" homebrew-tap "Homebrew tap for razvanbalsan tools"

# ---------------------------------------------------------------- 4. verify
info "verifying every formula URL resolves"
grep -o 'https://[^"]*\.tar\.gz' "$TAP_DIR/Formula/ktui.rb" | while read -r url; do
	code=$(curl -sIL -o /dev/null -w '%{http_code}' "$url")
	[ "$code" = "200" ] || die "$url is not downloadable (HTTP $code)"
	printf '    %s\n' "${url##*/}"
done

info "installing from the tap"
brew tap "$OWNER/tap"

# brew tap on an already-tapped repo does not fetch, and brew refuses to untap
# one you have a formula installed from — so Homebrew can keep reading the
# previous formula and call the old version up to date. Pull the clone forward
# explicitly, or this whole verification step proves nothing.
TAP_REPO="$(brew --repo "$OWNER/tap")"
if [ -d "$TAP_REPO/.git" ]; then
	info "refreshing $TAP_REPO"
	git -C "$TAP_REPO" fetch -q origin main \
		&& git -C "$TAP_REPO" merge -q --ff-only origin/main \
		|| warn "could not fast-forward the tap clone — run: git -C $TAP_REPO pull"
fi

# Recent Homebrew treats third-party taps as untrusted until you say otherwise.
# Trusting your own tap is the point of publishing it, so do it here rather than
# leaving the install to fail with an opaque "skipping because it is not trusted".
if brew help trust >/dev/null 2>&1; then
	info "trusting $OWNER/tap"
	brew trust "$OWNER/tap" || warn "brew trust failed — run it yourself if the install refuses"
fi

if brew list --versions ktui >/dev/null 2>&1; then
	info "upgrading the installed ktui"
	brew upgrade ktui || warn "brew upgrade reported a problem — the check below decides"
else
	brew install ktui
fi

# The point of the last three steps. Homebrew is happy to leave an older build
# in place and say nothing, so compare what landed against what was published.
installed=$(ktui --version 2>/dev/null || true)
[ "$installed" = "ktui $VERSION" ] || die "installed '$installed', expected 'ktui $VERSION'"

info "done"
echo "$installed"
cat <<EOF

  repo:    https://github.com/$OWNER/ktui
  tap:     https://github.com/$OWNER/homebrew-tap
  release: https://github.com/$OWNER/ktui/releases/tag/$TAG

  anyone can now install it with:
      brew tap $OWNER/tap
      brew trust $OWNER/tap    # recent Homebrew: third-party taps start untrusted
      brew install ktui
EOF
