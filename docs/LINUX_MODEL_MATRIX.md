# CMesh Linux Production Model Matrix

This matrix defines what the Linux production release is allowed to claim.
Anything outside this file is catalog or experimental until it has the same
metadata, evidence, and validation.

## Supported Sliced Model

### Qwen2.5 14B Instruct Q4_K_M

- Catalog ID: `qwen2.5-14b-instruct-q4-k-m`
- Runtime: `llama.cpp-b9704-linux-amd64-rpc-stage`
- Platform: `linux/amd64`
- Source: `https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf`
- File: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
- SHA256: `d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b`
- License: `Apache-2.0`
- Layers: `48`
- Quantization: `Q4_K_M`
- Context: `32768`
- Catalog RAM estimate: `16 GB`
- Catalog disk estimate: `10 GB`
- Minimum production stages: `3`
- Recommended production stages: `3`
- Minimum worker allocation per stage: `6 GB RAM`, `20 GB disk`
- Placement policy: `memory_disk_weighted_layers`
- Validated topology: `3 x t3.large`
- Validated execution mode: `resident-stage-daemon`
- Validated runner mode: `llama.cpp-stage-daemon`
- Evidence: `/tmp/cmesh-linux-beta-deployment-20260620135153/sliced`

## Production Claim

CMesh Linux production currently supports one documented sliced model path:
Qwen2.5 14B Instruct Q4_K_M split across three Linux amd64 workers with resident
stage daemons, physical GGUF stage artifacts, resident KV/session state, and
memory-aware placement.

Other catalog models may install and run locally, but they are not part of the
Linux production sliced support matrix until they have exact checksums, stage
metadata, and successful local/AWS sliced evidence.

## Adding Another Production Model

To add a model to this matrix:

1. Add exact catalog metadata including `SHA256`.
2. Add `ProductionSupport` metadata in `internal/models/catalog.go`.
3. Validate physical GGUF stage artifacts.
4. Run local sliced smoke.
5. Run AWS sliced proof with cleanup.
6. Add evidence paths to this file and `docs/LINUX_PRODUCTION_MILESTONES.md`.
7. Extend `TestLinuxProductionCatalogDeclaresSlicedModel` or split it into a
   table test when more than one model is supported.
