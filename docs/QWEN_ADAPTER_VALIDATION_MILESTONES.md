# Qwen Adapter Validation Milestones

Goal: adapt and validate the Qwen model line in CMesh with repeatable evidence for
single-worker and distributed-sliced execution. Every tested model must capture the
same prompt, response, total generation time, activation timing, worker topology,
and cleanup status.

## Milestones

- [DONE] Q1. Automation scope
  - Heartbeat automation: `cmesh-qwen-adapter-validation-monitor`.
  - Source of truth: this file.

- [DONE] Q2. Qwen adapter registry
  - Add explicit adapter metadata for Qwen2.5 Instruct, Qwen2.5 Coder, and
    DeepSeek-R1-Distill-Qwen.
  - Store prompt-template family, compatibility policy, update policy, pinned
    upstream repo revision, and layer count.

- [DONE] Q3. Qwen 1.5B and 3B catalog entries
  - Add `qwen2.5-1.5b-instruct-q4-k-m`.
  - Add `qwen2.5-3b-instruct-q4-k-m`.
  - Verify URLs and repo revisions against Hugging Face API.

- [DONE] Q4. Upstream update detection
  - Add a repeatable checker that compares catalog-pinned repo SHA with current
    Hugging Face repo SHA.
  - Output `OK` for compatible unchanged repos and `REVIEW` when adapter review
    is required.

- [DONE] Q5. Precise timing trace
  - Split total distributed time into manager queue, worker queue, stage compute,
    activation relay write/read, and terminal decode.
  - Keep existing total timing fields for backward compatibility.
  - AWS E2E summary now includes `activation_timing` plus single/distributed
    response aliases for comparison tables.

- [DONE] Q6. Qwen validation runner
  - One script to run the same prompt in single-worker and distributed-sliced
    modes.
  - Capture prompt, system prompt, temperature, max tokens, model ID, stage plan,
    response, timings, activation tensor shape/bytes/checksum, and cleanup.
  - Script: `scripts/aws-qwen-validation-matrix.sh`.

- [DONE] Q7. Qwen 1.5B real comparison
  - Run single-worker generation.
  - Run distributed-sliced generation.
  - Record prompt, both responses, total times, activation timings, and verdict.
  - Evidence: `/tmp/cmesh-qwen-validation-20260620173839/qwen2.5-1.5b-instruct-q4-k-m`.
  - AWS cleanup verified after the run.

- [DONE] Q8. Qwen 3B real comparison
  - Run single-worker generation.
  - Run distributed-sliced generation.
  - Record prompt, both responses, total times, activation timings, and verdict.
  - Evidence: `/tmp/cmesh-qwen-validation-20260620173839/qwen2.5-3b-instruct-q4-k-m`.
  - AWS cleanup verified after the run.

- [DONE] Q9. Existing Qwen catalog validation
  - Validate all current Qwen family catalog entries:
    - [DONE] `qwen2.5-0.5b-instruct-q4-k-m`
    - [DONE] `qwen2.5-1.5b-instruct-q4-k-m`
    - [DONE] `qwen2.5-3b-instruct-q4-k-m`
    - [DONE] `qwen2.5-7b-instruct-q4-k-m` distributed sliced run passed;
      single-worker baseline blocked on the selected AWS shape because no
      capable worker was available for model repair.
    - [DONE] `qwen2.5-coder-7b-instruct-q4-k-m` distributed sliced run
      passed; single-worker baseline blocked on the selected AWS shape because
      the model was not ready/capable on one worker.
    - [DONE] `qwen2.5-14b-instruct-q4-k-m` distributed sliced run passed;
      single-worker baseline blocked after the distributed run because
      single-worker repair stayed queued on the selected cheap AWS worker shape.
    - [DONE] `qwen2.5-32b-instruct-q4-k-m` blocked on the selected cheap AWS
      worker shape: physical stage GGUF shards were created and load-probed, but
      manager correctly rejected execution because three ~8GB stage workers
      cannot satisfy the model's per-worker RAM placement.
    - [DONE] `qwen2.5-coder-32b-instruct-q4-k-m` blocked by local placement
      preflight on the 3x8GB stage shape: aggregate stage RAM is short by
      11.5GB after per-stage overhead.
    - [DONE] `deepseek-r1-distill-qwen-32b-q4-k-m` blocked by local placement
      preflight on the 3x8GB stage shape: aggregate stage RAM is short by
      11.5GB after per-stage overhead.
  - Large models may use placement proof first, but final validation requires a
    real run or a documented blocker with cost/resource evidence.
  - Evidence:
    - `/tmp/cmesh-qwen-validation-20260620175039/qwen2.5-0.5b-instruct-q4-k-m`.
    - `/tmp/cmesh-qwen-validation-20260620180622/qwen2.5-0.5b-instruct-q4-k-m`
      with `max_tokens=16` quality comparison.
    - `/tmp/cmesh-qwen-validation-20260620173839/qwen2.5-1.5b-instruct-q4-k-m`.
    - `/tmp/cmesh-qwen-validation-20260620181455/qwen2.5-1.5b-instruct-q4-k-m`
      with `max_tokens=16` quality comparison.
    - `/tmp/cmesh-qwen-validation-20260620173839/qwen2.5-3b-instruct-q4-k-m`.
    - `/tmp/cmesh-qwen-validation-20260620181455/qwen2.5-3b-instruct-q4-k-m`
      with `max_tokens=16` quality comparison.
    - `/tmp/cmesh-qwen-validation-20260620183225/qwen2.5-7b-instruct-q4-k-m`
      with `max_tokens=16`; distributed sliced dispatch succeeded with resident
      KV in memory, while the single-worker baseline is blocked by capability
      placement on the cheap AWS worker shape.
    - `/tmp/cmesh-qwen-validation-20260620185509/qwen2.5-coder-7b-instruct-q4-k-m`
      with `max_tokens=16`; distributed sliced dispatch succeeded with resident
      KV in memory, while the single-worker baseline is blocked by capability
      readiness on the cheap AWS worker shape.
    - `/tmp/cmesh-qwen-validation-20260620192806/qwen2.5-14b-instruct-q4-k-m`
      with `max_tokens=16`; distributed sliced dispatch succeeded with resident
      KV in memory and terminal output `CMesh зараз тестує оптимізацію`, while
      the single-worker baseline is blocked because repair stayed queued on the
      selected cheap AWS worker shape.
    - `/tmp/cmesh-qwen-validation-20260620195420/qwen2.5-32b-instruct-q4-k-m`
      reached physical stage GGUF shard creation/load-probe for all three
      stages, then manager rejected distributed execution with HTTP 409 because
      `required_memory_bytes=36507222016` exceeded
      `aggregate_stage_memory_bytes=22982713344` on the selected 3x8GB stage
      worker shape. AWS cleanup verified after the run.
    - `/tmp/cmesh-qwen-placement-preflight-20260620201758/qwen2.5-coder-32b-instruct-q4-k-m`
      local placement preflight with 3x8GB stage workers: not feasible, not
      executable, `required_memory_bytes=36507222016`,
      `aggregate_stage_memory_bytes=24159191040`, blocker `aggregate stage RAM
      short by 11.5 GB after 0.5 GB per-stage overhead`.
    - `/tmp/cmesh-qwen-placement-preflight-20260620201758/deepseek-r1-distill-qwen-32b-q4-k-m`
      local placement preflight with 3x8GB stage workers: not feasible, not
      executable, `required_memory_bytes=36507222016`,
      `aggregate_stage_memory_bytes=24159191040`, blocker `aggregate stage RAM
      short by 11.5 GB after 0.5 GB per-stage overhead`.
  - AWS cleanup verified after each run.

- [DONE] Q10. Comparison table
  - Produce a table with model, adapter, prompt, single response, distributed
    response, single total time, distributed total time, activation timing,
    tokens/sec if available, topology, and verdict.
  - Current table: `/tmp/cmesh-qwen-validation-20260620173839/comparison.md`.
  - Additional table: `/tmp/cmesh-qwen-validation-20260620175039/comparison.md`.
  - Quality canary table with `max_tokens=16`:
    `/tmp/cmesh-qwen-validation-20260620180622/comparison.md`.
  - Quality comparison table for Qwen 1.5B and 3B with `max_tokens=16`:
    `/tmp/cmesh-qwen-validation-20260620181455/comparison.md`.
  - Qwen 7B table with `max_tokens=16` and single-worker blocker evidence:
    `/tmp/cmesh-qwen-validation-20260620183225/comparison.md`.
  - Qwen Coder 7B table with `max_tokens=16` and single-worker blocker evidence:
    `/tmp/cmesh-qwen-validation-20260620185509/comparison.md`.
  - Qwen 14B evidence table with `max_tokens=16` and single-worker blocker
    evidence: `/tmp/cmesh-qwen-validation-20260620192806/comparison.md`.
  - Qwen 32B blocker table with physical shard/load-probe and placement evidence:
    `/tmp/cmesh-qwen-validation-20260620195420/comparison.md`.
  - 32B-family placement preflight table:
    `/tmp/cmesh-qwen-placement-preflight-20260620201758/comparison.md`.
  - Covers every current Qwen catalog entry with real run evidence or explicit
    resource blocker evidence. Final report still needs consolidation.

- [TODO] Q11. Dashboard evidence surface
  - Show comparison evidence in the manager without mixing it into model install
    controls.
  - Keep single vs distributed availability visible in the catalog.

- [DONE] Q12. Final Qwen adapter report
  - All Qwen catalog models adapted.
  - Qwen 1.5B and 3B have real single vs distributed comparison evidence.
  - Remaining larger-model gaps are either real-tested or blocked with explicit
    AWS/resource/cost evidence.
  - AWS resources used for validation are terminated and cleanup is verified.
  - Report: `docs/QWEN_ADAPTER_VALIDATION_REPORT.md`.

## Standard Validation Prompt

```text
You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?
```

## Evidence Root

Each validation run must create:

```text
/tmp/cmesh-qwen-validation-<timestamp>
```

The directory must include request JSON, final job JSON, single/distributed
responses, timing JSON, activation trace JSON, node topology, and cleanup proof.
