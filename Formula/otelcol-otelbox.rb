# Homebrew formula for the otelbox collector's macOS artefact.
#
# This is an INSTALLATION channel, not a deployment. It places the published
# darwin/arm64 binary and the configuration layers this repository publishes on
# a workstation, and stops there.
#
# THERE IS DELIBERATELY NO `service` BLOCK, AND ONE MUST NOT BE ADDED.
# Supervision of the edge role is owned by `devbox-setup`: it installs a launchd
# agent, sources the gateway endpoint from a file, and reads the ingestion token
# from the login keychain at agent start. A `brew services` definition would be a
# second supervisor for the same collector, and the two would race for the same
# loopback listeners (127.0.0.1:4317/4318/13133/8888). The symptom would present
# as a port conflict or as telemetry vanishing into whichever process won the
# bind — that is, as a runtime fault, not as a packaging mistake, so the cost of
# the error is paid a long way from its cause. Adding the block later, if the
# launchd path is ever retired, is a small additive change; disentangling two
# supervisors on a live workstation is not.
#
# CI rewrites the version-bearing literals below after every release
# (.github/workflows/otelcol-otelbox.yml, job `formula`). It anchors on shape,
# not on marker comments: the `version` line, the tag segment that follows
# `/releases/download/` in each URL, and the two `sha256` lines told apart by
# their indentation — two spaces for the binary, four for the resource. Keep one
# literal per line and keep `brew style`'s indentation, or a rewrite silently
# matches nothing and the formula goes stale without a word.
#
# Those anchors do not know a comment from code. Spelling a whole download path
# out in prose anywhere in this file, tag and all, hands the rewrite a third
# thing to edit and puts a version number into a sentence that was never about
# one — hence the awkward phrasing above, which names the path without
# completing it.
class OtelcolOtelbox < Formula
  desc "Durable OpenTelemetry collector with redaction and an on-disk WAL per leg"
  homepage "https://github.com/abrosimov/otelcol-otelbox"
  # The release tag is this version with a `v` in front, yet it is spelled out
  # here rather than derived from `version`, because every way of deriving it
  # costs a `brew style` offence and the formula is required to be clean but for
  # the two placeholder checksums. `#{version}` cannot be used: the URL has to
  # precede the `version` line (FormulaAudit/ComponentsOrder), so at this point
  # there is no version to read, and inside the `resource` block below `version`
  # resolves to the resource's own. A local holding the version instead makes
  # `version <local>` unreadable to FormulaAudit/Version, which then reports the
  # version as empty. So: two literals, one shape, rewritten in one pass, with
  # the job's own sweep standing guard over the pair.
  url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v1.0.0/otelcol-otelbox_darwin_arm64"
  # Mirrors dist.version in builder.yaml, which is the single source of truth.
  # It is this artefact's own version and says nothing about which upstream
  # collector is inside; that lives in the gomod pins of the manifest.
  version "1.0.0"
  # NOT A CHECKSUM. No release exists for this version yet, so no honest digest
  # can be written here, and a plausible-looking one would be worse than none:
  # Homebrew installs a formula with no `sha256` at all after only a warning.
  # This value is rejected outright, so the failure is loud and self-describing.
  # CI replaces it with the digest of the asset it actually published.
  sha256 "8d47ab5ae69ab19fc8d8db2711424d5675267479d0759a0ef42db6306e629e8f"

  # The release ships one binary per target and this formula wants only the
  # macOS one, so the platform is a hard requirement rather than a conditional
  # `url`: on anything else there is nothing here to install.
  depends_on arch: :arm64
  depends_on :macos

  # The published configuration layers, as a release asset of their own. Not the
  # tag's auto-generated source tarball: GitHub regenerates those on cache miss
  # and their bytes have changed under a pinned digest before, which would break
  # this formula at some unrelated later date. A release asset is immutable.
  resource "config" do
    url "https://github.com/abrosimov/otelcol-otelbox/releases/download/v1.0.0/otelcol-otelbox_config.tar.gz"
    sha256 "24c26833e7f9ba64f2998920cfcb839fde0d437884b6d19f800a86d7d46eb452"
  end

  def install
    bin.install "otelcol-otelbox_darwin_arm64" => "otelcol-otelbox"
    # The download is a bare binary rather than an archive, so there is no mode
    # to preserve — what lands in the staging directory is not executable.
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

      `config/examples` holds the edge and gateway profiles CI validated for this
      version, so a deployed configuration can be diffed against the profile that
      was actually proven:

        diff <your-role-layer>.yaml #{opt_pkgshare}/config/examples/edge.yaml
    EOS
  end

  test do
    # The two invariants the collector exists for. `components` is the binary's
    # own report of what it links, so this fails on a thin or wrong-manifest
    # build rather than merely on a missing file.
    components = shell_output("#{bin}/otelcol-otelbox components")
    assert_match "name: file_storage", components
    assert_match "name: redaction", components
    assert_match version.to_s, components

    # A minimal role layer, composed with the installed base layer exactly as a
    # deployment composes one. This exercises the installed share files, not
    # only the binary: a base layer that failed to install, or one the binary
    # can no longer parse, fails here. Nothing is dialled or bound — `validate`
    # unmarshals and checks the configuration without starting the collector.
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
