# Third-Party Notices

CMesh source code is licensed under Apache License 2.0 unless a file states
otherwise. Release packages may reference or bundle third-party artifacts as
described below.

## llama.cpp Runtime

The Linux production package includes a pinned `llama.cpp` stage runtime
artifact:

- artifact: `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`
- upstream project: `llama.cpp`
- upstream license: MIT
- pinned ref: `b9704`

CMesh verifies the runtime artifact by checksum before install/use. Operators
should keep the runtime archive and its `.sha256` sidecar together.

## GGUF Models

CMesh does not claim ownership of model weights. The Linux production model
matrix references exact GGUF model artifacts and checksums, including:

- `qwen2.5-14b-instruct-q4-k-m`
- file: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
- SHA256:
  `d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b`

Operators are responsible for reviewing and complying with model licenses,
acceptable-use terms, and distribution restrictions before downloading or using
model artifacts.

## Go Dependencies

Go module dependencies are declared in `go.mod` and `go.sum`. Review upstream
licenses before redistributing modified dependency bundles.

## Release Signing Keys

Public release signing keys verify CMesh release artifacts only. They do not
grant trust to third-party model weights or external runtime mirrors unless
those exact artifacts are listed in the signed package manifest and checksums.
