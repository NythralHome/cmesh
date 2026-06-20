# CMesh v0.1.0 Linux RC1 Post-Release Monitoring

Use this checklist after publishing the GitHub prerelease.

## Release Links

- Release:
  `https://github.com/NythralHome/cmesh/releases/tag/v0.1.0-linux-rc.1`
- Required assets:
  - `v0.1.0-linux-rc.1.tar.gz`
  - `v0.1.0-linux-rc.1.tar.gz.sha256`
  - `v0.1.0-linux-rc.1.tar.gz.sig`
  - `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

## Immediate Checks

- Download all assets from GitHub.
- Verify tarball checksum.
- Verify tarball signature.
- Extract tarball.
- Verify `manifest.json.sig`.
- Verify `checksums.txt.sig`.
- Verify internal `checksums.txt`.
- Confirm release notes do not claim Windows/macOS production support.
- Confirm no private signing keys or local evidence directories are uploaded.

## First 48 Hours

- Watch GitHub issues for install failures.
- Watch GitHub discussions or direct feedback for broken quickstart steps.
- Triage security reports privately through GitHub Security Advisories.
- Track whether users fail at:
  - signature verification;
  - manager install;
  - worker join;
  - runtime auto-management;
  - model download/checksum;
  - sliced runbook execution.

## Cloud Cleanup

For every validation run, record:

- AWS instance IDs;
- security group IDs;
- evidence directory;
- cleanup status;
- estimated instance type and runtime.

Latest release validation cleanup:

- installer instances:
  `i-0fb17b90444f014d7`, `i-0b444d8dc2382b6fc`,
  `i-0fd7ea9fda0d2bc6d`
- sliced instances:
  `i-0592a2da5f22343ce`, `i-0c78049858782bb66`,
  `i-0790f2acd499fd88c`
- sliced security group:
  `sg-0bf0af54f02d3311d`

All listed resources were cleaned up during the RC1 validation run.
