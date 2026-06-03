class Trooper < Formula
  desc "Zero-instrumentation agent observability proxy with local LLM fallback"
  homepage "https://github.com/shouvik12/trooper"
  url "https://github.com/shouvik12/trooper/archive/7a5ed6e29a7bd161e70294f3ec9c5ad293d06cb2.tar.gz"
  sha256 "4f2f044a3a233d782ee07348b4e08571acaba1ae8d611cb5802bfd442bd59dc4"
  version "3.1.0"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "."
  end

  test do
    port = free_port
    pid = fork do
      ENV["TROOPER_PORT"] = port.to_s
      exec bin/"trooper"
    end
    sleep 1
    begin
      output = shell_output("curl -s http://127.0.0.1:#{port}/sessions")
      assert_match "sessions", output
    ensure
      Process.kill("TERM", pid)
      Process.wait(pid)
    end
  end
end
