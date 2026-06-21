# CMesh Real 4-Node Test Milestones

This track validates the published Linux RC on a new isolated test cluster and
adds operator-facing execution mode clarity in the manager dashboard.

Last updated: 2026-06-20T16:39:00Z

## Checklist

- [DONE] T1. Test scope and isolation
- [DONE] T2. Four-machine test automation
- [DONE] T3. New test subdomain deployment
- [DONE] T4. Distributed Qwen prompt evidence
- [DONE] T5. Single-worker regression evidence
- [DONE] T6. Distributed vs single timing report
- [DONE] T7. Dashboard execution mode selector
- [DONE] T8. Catalog availability badges
- [DONE] T9. API/manager regression tests
- [DONE] T10. Final evidence report

## Current Focus

This milestone set is complete for the isolated four-node Qwen validation. The
passing run used one manager and three dedicated stage workers on AWS, attached
a temporary Route53 subdomain, ran the distributed sliced Qwen path, ran a
single-worker repair/generate regression, recorded timings, and terminated all
created AWS resources.

Evidence directory:

`/tmp/cmesh-4node-qwen-test-20260620162409`

Important scope note: execution used the cost-conscious Qwen2.5 0.5B GGUF
fixture for real sliced and single-worker runs. The same run also verified a
memory-aware placement plan for Qwen2.5 14B across three 8 GB stage workers,
but did not download or execute the full 14B artifact.

Recorded timings from `summary.json`:

- Distributed sliced one-token job: `26244 ms`
- Distributed sliced decode-loop dispatch job: `36895 ms`
- Single-worker repair/generate regression: `12622 ms`

Recorded outputs:

- Distributed one-token output: ` C`
- Distributed dispatch output: `Mesh`
- Single-worker output: `CMesh is` with the upstream prompt wrapper truncated
  by the worker result guard.

Cleanup:

- EC2 instances terminated: `i-0ccd09b648fda3689`,
  `i-04d280cd57ef12176`, `i-0ed84ddf583898950`,
  `i-07bd8e644e6bbebb3`
- Test DNS record deleted:
  `qwen4test-20260620162409.cmesh.nythral.com`

## T1 Exit Criteria

- New branch created for this work.
- Test domain is separate from existing CMesh domains.
- Prompt, model, topology, and success criteria are fixed.
- No existing deployment is modified.

## T2 Exit Criteria

- A script can run a package-based four-machine test.
- It records manager host, three worker hosts, instance IDs, timings, prompt,
  response, and cleanup state.
- It supports a configurable test domain.
- It can run without publishing a new release.

## T3 Exit Criteria

- A new subdomain is created or documented for the test cluster.
- DNS points to the test manager only.
- Existing domains are not changed.
- TLS/domain setup is validated if DNS credentials are available.

## T4 Exit Criteria

- Distributed Qwen prompt is executed through the sliced path.
- Prompt, response, model id, stage placement, elapsed time, and job ids are
  recorded.
- Evidence contains enough data to reproduce the run.

## T5 Exit Criteria

- Single-worker local model run is validated on one worker.
- It proves existing one-model-on-one-machine behavior still works.
- Prompt, response, model id, worker id, elapsed time, and job id are recorded.

## T6 Exit Criteria

- A report compares distributed and single-worker runs.
- It includes setup/install time separately from inference time.
- It explicitly calls out limitations of comparing sliced Qwen 14B with any
  smaller single-worker model if RAM does not allow the exact same model.

## T7 Exit Criteria

- Dashboard chat/run UI lets the operator choose execution mode:
  - automatic
  - single worker
  - distributed sliced
- Invalid choices are disabled or explained.
- Submitted API payload contains the selected mode.

## T8 Exit Criteria

- Model catalog clearly shows:
  - single-worker availability
  - distributed sliced availability
  - unsupported/experimental state
- Operators can see why a model is unavailable for a mode.

## T9 Exit Criteria

- Go tests cover mode selection and model placement metadata.
- Existing distributed and single-worker paths still pass.
- Dashboard JS changes have a smoke or static guard where practical.

## T10 Exit Criteria

- Final report links evidence directories.
- AWS resources are confirmed terminated.
- Test subdomain ownership/state is documented.
- Remaining blockers are listed for v0.1.1/v0.2.0.
