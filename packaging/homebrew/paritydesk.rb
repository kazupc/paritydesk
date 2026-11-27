# Template for Formula/paritydesk.rb in the kazupc/homebrew-tap repository.
#
# It builds paritydesk from source, so Homebrew never installs a
# downloaded, unsigned binary that Gatekeeper could quarantine (design
# risk R8; paritydesk has no Developer ID and is not notarized).
#
# To publish a release (docs/release-checklist.md, "Release and install"):
#   1. Replace 0.0.0 in url with the release version (without the "v").
#   2. Replace the sha256 with the output of
#        curl -fsSL <url> | shasum -a 256
#   3. Commit the file to the tap as Formula/paritydesk.rb.
class Paritydesk < Formula
  desc "Switch the Claude desktop app between accounts on one Mac"
  homepage "https://github.com/kazupc/paritydesk"
  url "https://github.com/kazupc/paritydesk/archive/refs/tags/v0.0.0.tar.gz"
  sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  license "MIT"
  head "https://github.com/kazupc/paritydesk.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    ENV["CGO_ENABLED"] = "0"
    ldflags = %W[
      -buildid=
      -X github.com/kazupc/paritydesk/internal/cli.Version=#{version}
      -X github.com/kazupc/paritydesk/internal/cli.Commit=homebrew
    ]
    system "go", "build", *std_go_args(ldflags: ldflags), "./cmd/paritydesk"
    generate_completions_from_executable(bin/"paritydesk", "completion")
  end

  def caveats
    <<~EOS
      Run `paritydesk init` once before the first switch.
      paritydesk is unofficial and not affiliated with Anthropic.
    EOS
  end

  test do
    # Only prints the version: never touches Claude or its data.
    assert_match version.to_s, shell_output("#{bin}/paritydesk version")
  end
end
