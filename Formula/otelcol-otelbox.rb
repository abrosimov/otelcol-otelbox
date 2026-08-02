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
  url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v1.0.0/otelcol-otelbox_darwin_arm64"
  # Mirrors dist.version in builder.yaml, the single source of truth. It says
  # nothing about the upstream collector inside; that lives in the gomod pins.
  version "1.0.0"
  # CI writes this after publishing: a checksum cannot exist before the bytes it
  # describes. Hand-editing it only makes it stale.
  sha256 "8d47ab5ae69ab19fc8d8db2711424d5675267479d0759a0ef42db6306e629e8f"

  # The release ships one binary per target and this formula wants the macOS
  # one, so the platform is a hard requirement rather than a conditional `url`.
  depends_on arch: :arm64
  depends_on :macos

  # A release asset rather than the tag's source tarball: GitHub regenerates
  # those on cache miss, and their bytes have moved under a pinned digest before.
  resource "config" do
    url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v1.0.0/otelcol-otelbox_config.tar.gz"
    sha256 "24c26833e7f9ba64f2998920cfcb839fde0d437884b6d19f800a86d7d46eb452"
  end

  def install
    bin.install "otelcol-otelbox_darwin_arm64" => "otelcol-otelbox"
    # A bare binary, not an archive, so there is no mode to preserve.
    chmod 0555, bin/"otelcol-otelbox"

    resource("config").stage do
      (pkgshare/"config").install "base.yaml", "examples"
    end
  end

  def caveats
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

      The published configuration layers are installed, unmodified, under

        #{opt_pkgshare}/config

      The shared base layer is never a runnable configuration on its own; it is
      always composed with exactly one role layer, base first:

        otelcol-otelbox validate \\
          --config #{opt_pkgshare}/config/base.yaml \\
          --config <your-role-layer>.yaml

      `config/examples` holds the edge, gateway and host-agent profiles CI
      validated for this version, so a deployed configuration can be diffed
      against the profile that was actually proven:

        diff <your-role-layer>.yaml #{opt_pkgshare}/config/examples/edge.yaml
    EOS
  end

  test do
    # The two invariants the collector exists for. `components` is the binary's
    # own report, so a thin build fails here rather than a missing file.
    components = shell_output("#{bin}/otelcol-otelbox components")
    assert_match "name: file_storage", components
    assert_match "name: redaction", components
    assert_match version.to_s, components

    # Composed with the installed base layer exactly as a deployment composes
    # one, so a base layer that failed to install fails here too. `validate`
    # parses without starting the collector, so nothing is dialled or bound.
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
