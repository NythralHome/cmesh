# CMesh Public Release Signing

CMesh public releases must be signed with an explicit release key. The local
test-key path exists only for development smoke tests and must not be used for
GitHub release artifacts.

## Required Inputs

Set these variables when signing a public Linux release:

```sh
export CMESH_LINUX_PACKAGE_DIR=dist/linux-production/<version>
export CMESH_PUBLIC_RELEASE=true
export CMESH_SIGNING_KEY_ID=cmesh-linux-release-2026q2
export CMESH_SIGNING_PRIVATE_KEY=/secure/path/cmesh-linux-release-2026q2.key
```

Optional, if the public key is already exported:

```sh
export CMESH_SIGNING_PUBLIC_KEY=/secure/path/cmesh-linux-release-2026q2.pub.pem
```

Then sign:

```sh
scripts/sign-linux-production-release.sh
CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE=true scripts/linux-stable-release-smoke.sh
```

To initialize a release key outside the repository:

```sh
scripts/init-release-signing-key.sh
```

The default key path is `$HOME/.cmesh/release-signing`, with directory mode
`700` and private key mode `600`.

## Test Keys

For local development only:

```sh
CMESH_SIGNING_GENERATE_TEST_KEY=true scripts/sign-linux-production-release.sh
```

Packages signed this way include `key_kind=generated-test` and a warning in
`SIGNING.md`. The public release smoke rejects them when
`CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE=true`.

## Public Artifacts

Publish these files together:

- `<version>.tar.gz`
- `<version>.tar.gz.sha256`
- `<version>.tar.gz.sig`
- `<version>.tar.gz.public-key.pem`

The tarball contains:

- `manifest.json`
- `manifest.json.sig`
- `checksums.txt`
- `checksums.txt.sig`
- `SIGNING.md`
- `release-signing-public-key.pem`
- `signature-manifest.json`

## Private Key Rules

- Never commit private signing keys.
- Never store a public release private key under `dist/` or inside a package.
- Prefer `$HOME/.cmesh/release-signing` or a dedicated secret manager export
  path for local signing.
- Rotate the key by publishing a new `CMESH_SIGNING_KEY_ID` and public key
  fingerprint in release notes.
- If a private key is suspected compromised, revoke trust in that key in the
  next release notes and publish a replacement key.
