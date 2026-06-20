# CMesh v0.1.1 Stabilization Plan

This backlog starts after `v0.1.0-linux-rc.1`. It is intentionally scoped to
Linux stabilization unless an item is explicitly marked as cross-platform
research.

## Priority 0: Release Integrity

- Verify GitHub asset downloads from at least one clean Linux VPS.
- Re-run fresh-user validation after any release asset re-upload.
- Keep the release signing key fingerprint stable:
  `706857b490062911eaa8b92b486db5faf56db9bb153ea4a044a2f86f083fc6c8`
- Document any key rotation before publishing new assets.

## Priority 1: Installer Reliability

- Improve manager installer diagnostics for missing `systemd`, `curl`, `jq`, or
  bad token configuration.
- Improve worker installer diagnostics for runtime checksum failures.
- Add a clear repair command for stage runtime corruption.
- Add idempotent reinstall checks for manager and worker services.

## Priority 2: Runtime And Sliced Execution Recovery

- Add clearer operator output for stage daemon health failures.
- Add cleanup command for stale stage sessions and shard work directories.
- Add retry policy around relay/terminal stage transient failures.
- Add evidence capture command that bundles plan, jobs, observability, and
  service logs with tokens redacted.

## Priority 3: Documentation And User Support

- Convert the first external user failures into troubleshooting entries.
- Add a minimal VPS sizing guide for manager and workers.
- Add a cost note for AWS `t3.large` validation runs.
- Add "known limitations" to release notes if early users hit repeat failures.

## Priority 4: Security Hardening

- Add token rotation commands to the installer or CLI.
- Add explicit Caddy/TLS verification smoke.
- Add a redaction helper for logs before issue reports.
- Review systemd sandboxing after first external deployments.

## Deferred From v0.1.1

- Windows production installer.
- macOS production installer.
- GPU production path.
- Public untrusted worker marketplace.
- Payments, credits, reputation, and fraud resistance.
- Arbitrary model slicing outside the supported model matrix.

## Cross-Platform Research Track

Windows and macOS parity should have their own milestone plans. Do not mix
Windows/macOS production claims into Linux v0.1.1 patch work until they have:

- installer packaging;
- runtime packaging;
- service lifecycle management;
- fresh-user validation;
- security docs;
- release artifacts;
- platform-specific launch gates.
