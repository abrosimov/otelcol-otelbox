# Homebrew formula for the otelbox collector's macOS artefact — an installation
# channel, not a deployment.
#
# NO `service` BLOCK, AND ONE MUST NOT BE ADDED: `devbox-setup` supervises the
# edge role with launchd, and a second supervisor would race it for the same
# loopback listeners, surfacing as vanished telemetry rather than as a packaging
# mistake.
#
# CI rewrites the version-bearing literals after every release, anchored on
# shape rather than marker comments: the `version` line, the tag segment after
# `/releases/download/`, and the two `sha256` lines told apart by indentation.
# Keep one literal per line and `brew style`'s indentation, and never spell a
# complete download path out in prose here — the anchors cannot tell a comment
# from code.
class OtelcolOtelbox < Formula
  desc "Durable OpenTelemetry collector with redaction and an on-disk WAL per leg"
  homepage "https://github.com/abrosimov/otelcol-otelbox"
  # The tag is spelled out rather than derived from `version`: the URL must
  # precede the version line, so there is nothing to read yet, and every other
  # way of deriving it costs a `brew style` offence.
  url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v2.4.1/otelcol-otelbox_darwin_arm64"
  # Mirrors dist.version in builder.yaml, the single source of truth. It says
  # nothing about the upstream collector inside; that lives in the gomod pins.
  version "2.4.1"
  # CI writes this after publishing: a checksum cannot exist before the bytes it
  # describes. Hand-editing it only makes it stale.
  sha256 "467a0939857ab8905f6a9b0434a4b827a8bb9038c98816bc96a7003a65481fdc"

  # The release ships one binary per target and this formula wants the macOS
  # one, so the platform is a hard requirement rather than a conditional `url`.
  depends_on arch: :arm64
  depends_on :macos

  # A release asset rather than the tag's source tarball: GitHub regenerates
  # those on cache miss, and their bytes have moved under a pinned digest before.
  resource "config" do
    url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v2.4.1/otelcol-otelbox_config.tar.gz"
    sha256 "b559abda32e9dcb996d061b86deb27fddabf5e44501eb5cf2e5ef80d9d30ac13"
  end

  def install
    bin.install "otelcol-otelbox_darwin_arm64" => "otelcol-otelbox"
    # A bare binary, not an archive, so there is no mode to preserve.
    chmod 0555, bin/"otelcol-otelbox"

    resource("config").stage do
      # The structural change lands while this formula still names the last
      # published 1.x archive; CI rewrites the release literals only after the
      # 2.0 archive exists. Keep that intermediate checkout installable.
      if File.exist?("edge.yaml")
        (pkgshare/"config").install "edge.yaml", "gateway.yaml", "host-agent.yaml"
      else
        (pkgshare/"config").install "base.yaml", "examples"
      end
    end
  end

  def caveats
    config_guidance = if version >= Version.new("2.0.0")
      <<~EOS
        There is no shared base layer to compose: each profile is one
        self-contained file, and a role is run by loading exactly one of them
        and supplying the OTELBOX_* variables it references.

          otelcol-otelbox validate --config #{opt_pkgshare}/config/edge.yaml

        Every value that names a neighbour, a socket or a disk budget is an
        ${env:...} reference. Numeric ones carry a default, so forgetting one
        yields a working but unintended number rather than a failure — check
        them against the comments in the profile before relying on it.

        A deployed configuration can be diffed against the profile CI validated
        for this version:

          diff <your-config>.yaml #{opt_pkgshare}/config/edge.yaml
      EOS
    else
      <<~EOS
        The 1.x archive uses a base layer followed by exactly one role layer:

          otelcol-otelbox validate \
            --config #{opt_pkgshare}/config/base.yaml \
            --config #{opt_pkgshare}/config/examples/edge.yaml

        Upgrade the binary and configuration together when moving to 2.0; the
        two layout contracts are intentionally incompatible.
      EOS
    end

    <<~EOS
      This formula installs the artefact only. It does not supervise the
      collector and ships no `brew services` definition, on purpose: on a
      workstation the edge role is run by a launchd agent that `devbox-setup`
      installs, which also supplies the gateway endpoint and the ingestion token.
      Running `brew services` alongside it would put two supervisors on the same
      loopback listeners.

      The same hazard, one layer down: on a workstation that `devbox-setup`
      manages, this is not the installation channel either. There Ansible owns
      the binary. It downloads the release asset, checks it against the
      published digest, installs it under ~/.local/bin, and points the launchd
      agent at that copy. Installing through Homebrew as well puts two
      collectors on disk at two paths, free to drift to different versions,
      while launchd goes on running the one Homebrew did not install. What you
      would see is `otelcol-otelbox --version` in a terminal disagreeing with
      the version that is actually collecting anything — a symptom a long way
      from its cause. This formula is for machines the playbook does not
      manage, and for use by hand.

      The published role profiles are installed, unmodified, under

        #{opt_pkgshare}/config

      #{config_guidance}
    EOS
  end

  test do
    # The two invariants the collector exists for. `components` is the binary's
    # own report, so a thin build fails here rather than a missing file.
    components = shell_output("#{bin}/otelcol-otelbox components")
    assert_match "name: file_storage", components
    assert_match "name: redaction", components
    assert_match version.to_s, components

    edge_profile = pkgshare/"config/edge.yaml"
    if edge_profile.exist?
      # The credential file is real: `headers_setter` reads the complete value
      # and a validation against a missing path would prove less.
      (testpath/"auth-header").write "Bearer formula-test-placeholder\n"

      ENV["OTELBOX_BIND_HOST"] = "127.0.0.1"
      ENV["OTELBOX_HEALTH_ENDPOINT"] = "127.0.0.1:13133"
      ENV["OTELBOX_STORAGE_DIR"] = "#{testpath}/state"
      ENV["OTELBOX_UPSTREAM_ENDPOINT"] = "127.0.0.1:14319"
      ENV["OTELBOX_UPSTREAM_AUTH_HEADER_FILE"] = "#{testpath}/auth-header"

      system bin/"otelcol-otelbox", "validate", "--config", edge_profile
    else
      # Keep the last published 1.x formula testable until CI rewrites its
      # release literals after the first 2.0 publish.
      (testpath/"role.yaml").write <<~YAML
        receivers:
          otlp:
            protocols:
              grpc:
                endpoint: 127.0.0.1:14317
        extensions:
          file_storage/formula_test:
            directory: #{testpath}/wal
            create_directory: true
        exporters:
          file/formula_test:
            path: #{testpath}/out.json
        service:
          extensions: [file_storage/formula_test]
          pipelines:
            logs:
              receivers: [otlp]
              processors: [memory_limiter, redaction/secrets, batch]
              exporters: [file/formula_test]
      YAML

      system bin/"otelcol-otelbox", "validate",
             "--config", pkgshare/"config/base.yaml",
             "--config", testpath/"role.yaml"
    end
  end
end
