#!/bin/sh
# Emit a Homebrew formula for the archives in the dist directory.
# Usage: hack/formula.sh <version> <owner/repo> <dist-dir>
set -eu

VERSION="$1"
REPO="$2"
DIST="$3"
BASE="https://github.com/$REPO/releases/download/v$VERSION"

sha() {
	f="$DIST/ktui_${VERSION}_$1.tar.gz"
	[ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
	# shasum on macOS, sha256sum on Linux
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$f" | cut -d' ' -f1
	else
		sha256sum "$f" | cut -d' ' -f1
	fi
}

cat <<EOF
class Ktui < Formula
  desc "Terminal UI for managing kubectl contexts, with orphan cleanup"
  homepage "https://github.com/$REPO"
  version "$VERSION"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "$BASE/ktui_${VERSION}_darwin_arm64.tar.gz"
      sha256 "$(sha darwin_arm64)"
    end
    on_intel do
      url "$BASE/ktui_${VERSION}_darwin_amd64.tar.gz"
      sha256 "$(sha darwin_amd64)"
    end
  end

  on_linux do
    on_arm do
      url "$BASE/ktui_${VERSION}_linux_arm64.tar.gz"
      sha256 "$(sha linux_arm64)"
    end
    on_intel do
      url "$BASE/ktui_${VERSION}_linux_amd64.tar.gz"
      sha256 "$(sha linux_amd64)"
    end
  end

  def install
    bin.install "ktui"
  end

  test do
    assert_match "ktui #{version}", shell_output("#{bin}/ktui --version")

    # With an empty kubeconfig the tool must exit non-zero and say so, rather
    # than opening a TUI the test harness cannot drive.
    (testpath/"empty.yaml").write "apiVersion: v1\nkind: Config\n"
    output = shell_output(
      "KUBECONFIG=#{testpath}/empty.yaml #{bin}/ktui --list --no-probe 2>&1", 1
    )
    assert_match "no contexts found", output
  end
end
EOF
