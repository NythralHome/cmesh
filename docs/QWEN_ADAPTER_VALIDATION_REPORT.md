# Qwen Adapter Validation Report

Status: complete for the current Qwen catalog.

Standard prompt:

```text
You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?
```

## Results

| Model | Verdict | Evidence | Notes |
|---|---|---|---|
| `qwen2.5-0.5b-instruct-q4-k-m` | real single + distributed evidence | `/tmp/cmesh-qwen-validation-20260620180622/qwen2.5-0.5b-instruct-q4-k-m` | `max_tokens=16` quality canary. |
| `qwen2.5-1.5b-instruct-q4-k-m` | real single + distributed evidence | `/tmp/cmesh-qwen-validation-20260620181455/qwen2.5-1.5b-instruct-q4-k-m` | `max_tokens=16` comparison. |
| `qwen2.5-3b-instruct-q4-k-m` | real single + distributed evidence | `/tmp/cmesh-qwen-validation-20260620181455/qwen2.5-3b-instruct-q4-k-m` | `max_tokens=16` comparison. |
| `qwen2.5-7b-instruct-q4-k-m` | distributed sliced passed, single blocked | `/tmp/cmesh-qwen-validation-20260620183225/qwen2.5-7b-instruct-q4-k-m` | Single-worker baseline blocked by cheap AWS capability placement. |
| `qwen2.5-coder-7b-instruct-q4-k-m` | distributed sliced passed, single blocked | `/tmp/cmesh-qwen-validation-20260620185509/qwen2.5-coder-7b-instruct-q4-k-m` | Single-worker baseline blocked by cheap AWS readiness/capability. |
| `qwen2.5-14b-instruct-q4-k-m` | distributed sliced passed, single blocked | `/tmp/cmesh-qwen-validation-20260620192806/qwen2.5-14b-instruct-q4-k-m` | Resident KV in memory; output: `CMesh зараз тестує оптимізацію`. |
| `qwen2.5-32b-instruct-q4-k-m` | short distributed proof passed; normal prompt single-step passed | `/tmp/cmesh-32b-validation-20260620215948/summary.recovered.json`, `/tmp/cmesh-32b-normal-20260620222026`, `/tmp/cmesh-32b-normal-fix-20260620225035`, `/tmp/cmesh-32b-normal-fix3-20260620232342`, `/tmp/cmesh-32b-normal-fix4-20260620234227` | Short `max_tokens=3` proof returned `CMesh`; normal prompt single-step distributed decode returned ` За`. The client-side 1 MiB stage-daemon response cap was fixed to 256 MiB. The longer multi-token dispatch loop was progressing and was stopped manually to control AWS cost before final completion. |
| `qwen2.5-coder-32b-instruct-q4-k-m` | blocked by 3x8GB stage shape | `/tmp/cmesh-qwen-placement-preflight-20260620201758/qwen2.5-coder-32b-instruct-q4-k-m` | Required RAM `36507222016`, aggregate stage RAM `24159191040`. |
| `deepseek-r1-distill-qwen-32b-q4-k-m` | blocked by 3x8GB stage shape | `/tmp/cmesh-qwen-placement-preflight-20260620201758/deepseek-r1-distill-qwen-32b-q4-k-m` | Required RAM `36507222016`, aggregate stage RAM `24159191040`. |

## Comparison Tables

- `/tmp/cmesh-qwen-validation-20260620180622/comparison.md`
- `/tmp/cmesh-qwen-validation-20260620181455/comparison.md`
- `/tmp/cmesh-qwen-validation-20260620183225/comparison.md`
- `/tmp/cmesh-qwen-validation-20260620185509/comparison.md`
- `/tmp/cmesh-qwen-validation-20260620192806/comparison.md`
- `/tmp/cmesh-qwen-validation-20260620195420/comparison.md`
- `/tmp/cmesh-qwen-placement-preflight-20260620201758/comparison.md`
- `/tmp/cmesh-32b-validation-20260620215948/summary.recovered.json`

## 32B AWS Validation

The first 32B execution-ready run used four `r7i.xlarge` instances in `us-east-1`:
one manager and three stage workers. The executed model was
`qwen2.5-32b-instruct-q4-k-m` from `Qwen2.5-32B-Instruct-Q4_K_M.gguf`.

Stage split:

| Stage | Layers | Physical shard size | Load probe |
|---|---:|---:|---|
| 0 | 0-21 | 6,927,540,224 bytes | passed |
| 1 | 22-42 | 6,026,022,912 bytes | passed |
| 2 | 43-63 | 6,891,794,432 bytes | passed |

Prompt:

```text
You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?
```

Distributed output:

```text
 CMesh
```

Timing summary:

| Metric | Value |
|---|---:|
| Activation frame size | 450,560 bytes |
| Relay write total | 16 ms |
| Receive wait total | 29,988 ms |
| Stage compute total | 26,059 ms |
| Stage total sum | 64,661 ms |
| Terminal step total | 24,895 ms |

Execution evidence says `resident_kv_in_memory=true` and
`execution_mode=resident-stage-daemon`.

## 32B Normal Prompt Validation

A follow-up 32B normal-prompt run used the same four-instance shape with one
manager and three `r7i.xlarge` stage workers. The goal was to move beyond the
short proof and validate a longer Ukrainian prompt with a larger requested
decode budget.

Evidence:

- `/tmp/cmesh-32b-normal-20260620222026`
- `/tmp/cmesh-32b-normal-fix-20260620225035`

Prompt:

```text
Ти працюєш всередині розподіленого CMesh кластера на Qwen2.5 32B. Поясни українською у 5 реченнях, що саме зараз тестується, чому це важливо для запуску великих моделей на кількох машинах, і які головні обмеження такого підходу.
```

Stage split and load probes:

| Stage | Layers | Physical shard size | Load probe |
|---|---:|---:|---|
| 0 | 0-21 | 6,927,540,224 bytes | passed |
| 1 | 22-42 | 6,026,022,912 bytes | passed |
| 2 | 43-63 | 6,891,794,432 bytes | passed |

Initial result:

```text
failed at stage 0 source_decode:
resident-decode requires native in-process llama.cpp stage context and KV ownership hooks
```

Placement, Linux service install, physical stage shard materialization, runtime
cache, and stage load probes worked. The observed failure was emitted by the
legacy `resident-decode` fallback, which masked the resident-loop error that
triggered it. That fallback is now disabled for resident-loop sessions, so the
next normal 32B validation will report the real native decode result directly.

During this run the harness also exposed two runtime/test issues:

- the first physical distributed request was hardcoded to `max_tokens: 1`
  instead of `CMESH_DISTRIBUTED_MAX_TOKENS`;
- the resident stage context default was `64`, while this normal prompt already
  produced an activation shape of `1,111,5120`; the backend then fell back to
  the legacy blocked `resident-decode` command, masking the real resident-loop
  failure.

Both issues are patched. Future physical distributed runs use the requested
token budget, resident stage sessions default to a `2048` token context, and
resident-loop decode failures are returned directly instead of being rewritten
as `blocked_missing_native_hooks`.

Follow-up result:

```text
failed at stage 0 source_decode:
decode stage daemon response: unexpected end of JSON input
```

The follow-up run confirmed physical shards, service install, stage daemon
registration, stage prepare, and decode dispatch. It then exposed a separate
client-side bug: `postStageDaemonDecodeWithPayload` capped successful daemon
responses to 1 MiB. A Qwen2.5 32B normal prompt produced an activation envelope
with shape `1,111,5120`; its raw tensor payload is about 2.27 MiB and its
base64 JSON response is about 3 MiB, so the client truncated the body and parsed
incomplete JSON. The response cap is now 256 MiB, configurable with
`CMESH_STAGE_DAEMON_DECODE_RESPONSE_LIMIT_BYTES`, and regression tests cover
large stage-daemon decode responses.

Post-fix result:

```text
 За
```

Evidence:

- `/tmp/cmesh-32b-normal-fix3-20260620232342/parent.json`
- `/tmp/cmesh-32b-normal-fix4-20260620234227/parent.json`

The fixed package completed a real single-step distributed decode for the normal
prompt through three physical Qwen2.5 32B stage shards. In the latest run,
terminal-stage timing reported `receive_wait_ms=44740`, `stage_daemon_ms=14438`,
and `total_ms=59180`. Stage 0 wrote its activation relay frame in `29 ms`, and
stage 1 wrote its relay frame in `26 ms`.

The subsequent 64-token dispatch loop advanced through multiple decode steps
and was stopped manually to prevent unnecessary AWS spend. That run is not
recorded as a completed long-generation pass.

## Cleanup

AWS cleanup was verified after every AWS validation run. The latest worker test
instances were terminated:

- `i-005676a09683c1eeb`
- `i-0e0536ccecfe7b948`
- `i-03114ce76ad21e1e7`
- `i-0cdefc0cce92fa7ee`

Only the pre-existing non-CMesh production instances remained running.

## Next Product Work

Qwen 32B-family validation now has a passing 32 GB stage-worker shape for the
base Qwen2.5 32B model. The next validation targets are the 32B coder and
DeepSeek-distill-Qwen variants, plus a cheaper topology search once placement
supports more than three stage workers.
