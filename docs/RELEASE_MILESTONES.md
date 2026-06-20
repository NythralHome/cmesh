# CMesh Public Release Milestones

This file is the release-track source of truth after the Linux production
launch candidate. Update it after every meaningful release step and include the
checklist in status reports.

Last updated: 2026-06-20T15:35:00Z

## Checklist

- [DONE] R1. Release scope freeze
- [DONE] R2. Clean release branch
- [DONE] R3. Real signing key
- [DONE] R4. Final Linux artifact rebuild
- [DONE] R5. Release verification gate
- [DONE] R6. GitHub release draft
- [DONE] R7. Public install docs
- [DONE] R8. Security disclosure docs
- [DONE] R9. License and governance cleanup
- [DONE] R10. Demo deployment
- [DONE] R11. Early adopter validation
- [DONE] R12. Release announcement package
- [DONE] R13. Public release publish
- [DONE] R14. Post-release monitoring
- [DONE] R15. v0.1.1 stabilization plan

## Current Focus

R15 is closed. The public Linux RC release is published, post-release
monitoring is documented, and the v0.1.1 stabilization backlog is ready.

## R1 Exit Criteria

- The first public release scope is explicit and narrow.
- Supported platforms, install paths, models, and distributed execution mode
  are named.
- Unsupported or future capabilities are listed clearly.
- Existing Linux production evidence is linked.
- The scope document is suitable to copy into release notes.

## R1 Evidence

R1 is closed by the explicit release scope document:

- Scope: `docs/RELEASE_SCOPE.md`
- Release boundary:
  - Linux hosts only.
  - Linux amd64 and arm64 manager/worker CLI binaries.
  - Linux amd64 llama.cpp stage runtime.
  - Supported sliced model: `qwen2.5-14b-instruct-q4-k-m`.
  - Signed release tarball flow.
  - No Windows/macOS production claim in this release.

## R2 Exit Criteria

- A release branch strategy is documented.
- Dirty tree changes are categorized into release-critical, documentation,
  generated artifacts, and defer/remove.
- No user work is reverted.
- Release candidate artifacts are reproducible from tracked scripts.

## R2 Evidence

R2 is closed by the release branch audit:

- Audit: `docs/RELEASE_BRANCH_AUDIT.md`
- Validation:
  - `git diff --check` passed.
  - `.gitignore` excludes `dist/`, `bin/`, and local env/state files.
  - Local generated private test key is under ignored `dist/`.
  - Release-critical source, scripts, docs, and support files are categorized.

## R3 Exit Criteria

- Test signing key is no longer used for the public release.
- A real release public key is published with verification instructions.
- Private key handling and rotation policy are documented.
- Package, manifest, checksum, and tarball signatures verify with the public
  release key.

## R3 Evidence

R3 is closed by a real local release signing key and strict public-release
signature validation:

- Signing process: `docs/RELEASE_SIGNING.md`
- Key init script: `scripts/init-release-signing-key.sh`
- Signing script guard: `scripts/sign-linux-production-release.sh`
- Strict signature smoke: `scripts/linux-stable-release-smoke.sh`
- Release key id: `cmesh-linux-release-2026q2`
- Release public key SHA256:
  `706857b490062911eaa8b92b486db5faf56db9bb153ea4a044a2f86f083fc6c8`
- Validation:
  - `bash -n scripts/sign-linux-production-release.sh scripts/linux-stable-release-smoke.sh` passed.
  - existing local-test package still passes normal signature smoke.
  - strict public-release smoke rejects the existing local-test package.
  - `CMESH_PUBLIC_RELEASE=true` plus `CMESH_SIGNING_GENERATE_TEST_KEY=true`
    fails before signing.
  - `v0.1.0-linux-rc.1` signed with `CMESH_PUBLIC_RELEASE=true`.
  - strict public-release signature smoke passed against `v0.1.0-linux-rc.1`.

## R4 Exit Criteria

- Final Linux release package is rebuilt from the release branch.
- Package version, docs, manifest, checksums, signatures, and tarball sidecars
  are internally consistent.
- The package does not depend on local dev paths.

## R4 Evidence

R4 is closed by the rebuilt signed Linux RC artifact:

- Package directory:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1`
- Tarball:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1.tar.gz`
- Included release docs:
  - `docs/RELEASE_SCOPE.md`
  - `docs/RELEASE_SIGNING.md`
  - `docs/RELEASE_MILESTONES.md`
  - `docs/RELEASE_BRANCH_AUDIT.md`
- Validation:
  - `scripts/release-linux-production.sh` rebuilt Linux amd64/arm64 binaries.
  - `scripts/sign-linux-production-release.sh` signed package and tarball.
  - `scripts/linux-stable-release-smoke.sh` passed with
    `CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE=true`.

## R5 Exit Criteria

- Full release verification gate passes against the final signed artifact.
- Fresh-user validation passes from only the public tarball and public key.
- Local smoke tests, docs smoke, runbook smoke, reliability smoke, and Go
  regression tests pass.
- Existing AWS beta evidence is still valid or a fresh cautious AWS proof is
  recorded and cleaned up.

## R5 Evidence

R5 is closed by the full Linux production launch gate against the signed RC:

- Package:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1`
- Tarball:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1.tar.gz`
- Launch gate evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-launch-gate-20260620144800`
- AWS beta evidence reused and cleanup-verified:
  `/tmp/cmesh-linux-beta-deployment-20260620135153`
- Validation:
  - signed release package and tarball
  - public production docs
  - fresh-user signed tarball flow
  - manager and worker installer dry-runs
  - runtime artifact verification
  - sliced runbook
  - security and observability docs
  - backup/restore
  - repeated local reliability
  - AWS installer and sliced beta evidence with cleanup
  - Go regression tests
  - git diff whitespace check

## R6 Exit Criteria

- GitHub tag strategy is chosen.
- Draft release notes exist.
- Release assets list is complete.
- Upload/verification checklist is ready.

## R6 Evidence

R6 is closed by release draft docs:

- Release notes: `docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md`
- GitHub release checklist: `docs/GITHUB_RELEASE_CHECKLIST.md`
- Tag strategy:
  - tag: `v0.1.0-linux-rc.1`
  - branch: `release/v0.1-linux`
  - prerelease: yes
- Required assets:
  - `v0.1.0-linux-rc.1.tar.gz`
  - `v0.1.0-linux-rc.1.tar.gz.sha256`
  - `v0.1.0-linux-rc.1.tar.gz.sig`
  - `v0.1.0-linux-rc.1.tar.gz.public-key.pem`
- Validation:
  - `git diff --check` passed.

## R7 Exit Criteria

- README quickstart is ready for a new Linux user.
- Public install docs cover manager, worker, runtime, model matrix, first
  sliced run, troubleshooting, and uninstall.
- Docs link only to public release artifacts or generic latest-release URLs.

## R7 Evidence

R7 is closed by README and Linux production guide updates:

- README public Linux quickstart:
  `README.md`
- Linux production guide:
  `docs/LINUX_PRODUCTION.md`
- Validated release asset names:
  - `v0.1.0-linux-rc.1.tar.gz`
  - `v0.1.0-linux-rc.1.tar.gz.sha256`
  - `v0.1.0-linux-rc.1.tar.gz.sig`
  - `v0.1.0-linux-rc.1.tar.gz.public-key.pem`
- Validation:
  - `scripts/linux-production-docs-smoke.sh` passed.

## R8 Exit Criteria

- SECURITY.md describes supported versions and reporting process.
- Public VPS threat model summary exists.
- Operator secret, join token, firewall, TLS, backup, and logging guidance are
  documented.

## R8 Evidence

R8 is closed by security disclosure and hardening docs:

- Security policy: `SECURITY.md`
- Linux hardening guide: `docs/LINUX_SECURITY_HARDENING.md`
- Validation:
  - `scripts/linux-production-security-doc-smoke.sh` passed.
  - supported version is `v0.1.0-linux-rc.1`.
  - vulnerability reporting path uses GitHub Security Advisories.
  - public issue disclosure warning is documented.

## R9 Exit Criteria

- License, notice, contribution, and code-of-conduct files are checked for
  public release.
- Third-party runtime/model notices are documented where applicable.
- Governance expectations for external contributors are clear.

## R9 Evidence

R9 is closed by governance and third-party notice updates:

- Third-party notices: `docs/THIRD_PARTY_NOTICES.md`
- Notice file: `NOTICE`
- Contribution guidance: `CONTRIBUTING.md`
- Governance smoke: `scripts/release-governance-smoke.sh`
- Make target: `make release-governance-smoke`
- Package inclusion:
  - `docs/THIRD_PARTY_NOTICES.md`
  - `docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md`
  - `docs/GITHUB_RELEASE_CHECKLIST.md`
- Validation:
  - `scripts/release-governance-smoke.sh` passed.

## R10 Exit Criteria

- A demo or beta deployment is created from public release artifacts only.
- Deployment evidence records manager, workers, model install, sliced generate,
  cleanup, and costs/resources.
- All temporary cloud resources are terminated.

## R10 Evidence

R10 is closed by an AWS package-based demo deployment from the signed RC:

- Package:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1`
- Evidence:
  `/tmp/cmesh-release-demo-deployment-v0.1.0-linux-rc.1-20260620145444`
- Installer evidence:
  `/tmp/cmesh-release-demo-deployment-v0.1.0-linux-rc.1-20260620145444/installers`
- Sliced evidence:
  `/tmp/cmesh-release-demo-deployment-v0.1.0-linux-rc.1-20260620145444/sliced`
- AWS shape:
  - instance type: `t3.large`
  - volume size: `80 GB`
- Validated:
  - package-based manager install
  - package-based worker install
  - `qwen2.5-14b-instruct-q4-k-m`
  - physical stage GGUF shards
  - remote source/relay/terminal decode
  - decode-loop dispatch job
  - resident stage daemon path
- Cleanup:
  - installer instances terminated:
    `i-0fb17b90444f014d7`, `i-0b444d8dc2382b6fc`,
    `i-0fd7ea9fda0d2bc6d`
  - sliced instances terminated:
    `i-0592a2da5f22343ce`, `i-0c78049858782bb66`,
    `i-0790f2acd499fd88c`
  - sliced security group deleted: `sg-0bf0af54f02d3311d`

## R11 Exit Criteria

- A fresh user flow is validated without local repository state.
- Download, signature verification, install, worker join, model run, and
  cleanup are recorded.
- Any user-facing friction is either fixed or documented as a known issue.

## R11 Evidence

R11 is closed by clean Linux fresh-user validation:

- Package:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1`
- Tarball:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-rc.1.tar.gz`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-fresh-user-validation-20260620151058`
- Platform:
  - `ubuntu:24.04`
  - `linux/amd64`
- Validation:
  - tarball SHA256 verified
  - tarball signature verified
  - internal `manifest.json.sig` verified
  - internal `checksums.txt.sig` verified
  - package checksums verified
  - manager installer dry-run passed
  - worker installer dry-run passed

## R12 Exit Criteria

- Announcement copy explains what CMesh does now, what it does not do yet, and
  the roadmap.
- Contribution links, issue templates, and discussion path are ready.
- Claims are consistent with evidence.

## R12 Evidence

R12 is closed by the announcement draft:

- Announcement: `docs/RELEASE_ANNOUNCEMENT_v0.1.0-linux-rc.1.md`
- Smoke: `scripts/release-announcement-smoke.sh`
- Validation:
  - `scripts/release-announcement-smoke.sh` passed.
  - claims explicitly exclude Windows/macOS production installs.
  - claims explicitly exclude arbitrary model slicing.
  - contribution and roadmap sections are included.

## R13 Exit Criteria

- GitHub release is published with all assets.
- Public download links work.
- Signature verification instructions work from a clean shell.
- Landing/docs links point to the release.

## R13 Evidence

R13 is closed by the published GitHub prerelease:

- Release:
  `https://github.com/NythralHome/cmesh/releases/tag/v0.1.0-linux-rc.1`
- Branch:
  `release/v0.1-linux`
- Tag:
  `v0.1.0-linux-rc.1`
- Uploaded assets:
  - `v0.1.0-linux-rc.1.tar.gz`
  - `v0.1.0-linux-rc.1.tar.gz.sha256`
  - `v0.1.0-linux-rc.1.tar.gz.sig`
  - `v0.1.0-linux-rc.1.tar.gz.public-key.pem`
- Public asset verification evidence:
  `/tmp/cmesh-github-release-verify-EyVJTD`
- Validation:
  - public tarball downloaded from GitHub
  - tarball SHA256 verified
  - tarball signature verified
  - internal `manifest.json.sig` verified
  - internal `checksums.txt.sig` verified
  - internal package checksums verified

## R14 Exit Criteria

- Post-release monitoring checklist is active.
- Broken links, install failures, first issues, and user reports are triaged.
- Cloud resources created during release validation are confirmed cleaned up.

## R14 Evidence

R14 is closed by post-release monitoring docs and GitHub release inspection:

- Monitoring checklist: `docs/POST_RELEASE_MONITORING.md`
- Smoke: `scripts/post-release-monitoring-smoke.sh`
- GitHub release:
  `https://github.com/NythralHome/cmesh/releases/tag/v0.1.0-linux-rc.1`
- Release state:
  - tag: `v0.1.0-linux-rc.1`
  - prerelease: `true`
  - assets present:
    - `v0.1.0-linux-rc.1.tar.gz`
    - `v0.1.0-linux-rc.1.tar.gz.public-key.pem`
    - `v0.1.0-linux-rc.1.tar.gz.sha256`
    - `v0.1.0-linux-rc.1.tar.gz.sig`
- Validation:
  - `scripts/post-release-monitoring-smoke.sh` passed.
  - `gh release view v0.1.0-linux-rc.1` returned the expected assets.

## R15 Exit Criteria

- v0.1.1 stabilization backlog exists.
- Bugs and follow-up improvements are prioritized.
- Windows/macOS parity work is separated from Linux patch work.
- Release automation can be stopped because the public release loop has a next
  patch plan.

## R15 Evidence

R15 is closed by the stabilization plan:

- Plan: `docs/V0_1_1_STABILIZATION_PLAN.md`
- Smoke: `scripts/stabilization-plan-smoke.sh`
- Validation:
  - `scripts/stabilization-plan-smoke.sh` passed.
  - Linux v0.1.1 patch work is separated from Windows/macOS parity.
  - deferred work explicitly excludes GPU, public marketplace, payments, and
    arbitrary model slicing.
