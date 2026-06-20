# GitHub Release Checklist

## Tag

- Tag name: `v0.1.0-linux-rc.1`
- Target branch: `release/v0.1-linux`
- Release title: `CMesh v0.1.0 Linux RC1`
- Prerelease: yes
- Latest release: no, until RC validation is accepted by early users.

## Required Assets

Upload from `dist/linux-production`:

- `v0.1.0-linux-rc.1.tar.gz`
- `v0.1.0-linux-rc.1.tar.gz.sha256`
- `v0.1.0-linux-rc.1.tar.gz.sig`
- `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

## Pre-Publish Checks

```sh
CMESH_LINUX_PACKAGE_DIR=dist/linux-production/v0.1.0-linux-rc.1 \
  CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE=true \
  scripts/linux-stable-release-smoke.sh

CMESH_LINUX_PACKAGE_DIR=dist/linux-production/v0.1.0-linux-rc.1 \
  scripts/linux-production-launch-gate.sh
```

## Publish Steps

1. Create or update branch `release/v0.1-linux`.
2. Commit release-critical source, scripts, docs, and workflow changes.
3. Tag `v0.1.0-linux-rc.1`.
4. Create a GitHub prerelease using
   `docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md`.
5. Upload all required assets.
6. Download the assets from GitHub into a clean temp directory.
7. Verify tarball checksum and signature.
8. Extract tarball and verify internal manifest/checksums signatures.

## Post-Publish Checks

- Public download links work.
- README quickstart references the release.
- Security docs identify the supported version.
- No private signing key or local evidence directory is uploaded.
