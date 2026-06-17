# First Real Cluster Test

This runbook is for the first alpha test with real machines joining one private CMesh manager.

## Goal

Prove that several machines can join one manager, advertise resources, run benchmarks, install a local model, run a prompt, delete the model, and remain controllable from the donor desktop app.

## Test Shape

- One manager: `https://cmesh.nythral.com` or another self-hosted manager.
- Two or more worker machines on different networks.
- At least one macOS machine and one Windows or Linux machine when possible.
- Worker donors use the desktop app first. CLI install scripts are fallback only.

## Manager Operator

1. Open the manager invite page.
2. Create or copy a join invite.
3. Send the invite link to each worker donor.
4. Watch Workers, Models, Model Activity, Jobs, and Benchmarks during the test.
5. Record the node count and total advertised CPU, RAM, disk, GPU, and VRAM before and after each worker joins.
6. Confirm each worker reports runtime status and installed model inventory.
7. Confirm `/v1/models` lists installed models under `installed_on` and only runtime-ready workers under `generatable_on`.

## Worker Donor

1. Download the desktop worker package for the OS from the latest GitHub release.
2. Start the app once so it can register the `cmesh://` invite handler.
3. Open the invite link from the manager.
4. Confirm that Manager URL and Join token are filled.
5. Set resource limits:
   - CPU cores: the maximum cores the donor agrees to share.
   - RAM GB: the maximum memory the donor agrees to share.
   - Disk GB: the cache/storage budget.
   - VRAM GB: GPU memory budget, or `0` when unavailable.
6. Keep `Run benchmark after connect` enabled.
7. Click `Connect worker`.
8. Confirm that the status card changes to `Running`.
9. Use `Status`, `Stop`, `Start`, and `Disconnect` to verify local control.

## Success Criteria

- Each worker appears in manager nodes.
- Resource totals increase when a worker joins.
- Benchmark results appear for each worker.
- Workers report `llama.cpp` runtime status.
- Model catalog shows why each model can or cannot install on each worker.
- Installing a model creates a visible model job and eventually updates installed inventory.
- Chat can generate a response against an installed model and selected worker.
- Conversation history persists across messages.
- Manual model memory appears in prompt preview and affects future chat context.
- Deleting a model removes it from worker inventory and reports freed bytes.
- The desktop app can show running/stopped state without using terminal commands.
- `Stop` stops the local worker process.
- `Start` brings the worker back.
- `Disconnect` removes the active worker process from the donor machine.

## Known Alpha Limitations

- macOS packages are distributed as DMG builds and should be signed/notarized when release secrets are configured.
- Windows and Linux desktop packaging is still alpha-level.
- Background service installation is still alpha-level and will move to signed installers or privileged helpers later.
- Model execution currently runs one model on one selected worker. This test does not yet prove distributed inference for one model split across many machines.

## Evidence To Capture

- Screenshot of the manager before workers join.
- Screenshot after each worker joins.
- Screenshot of each worker desktop status card.
- `/v1/nodes` response after all workers join.
- `/v1/benchmarks` response after benchmarks complete.
- `/v1/models` response before and after model install.
- Model Activity screenshot for install, generate, and delete.
- Chat screenshot showing selected model and worker.
- Prompt Debug screenshot when manual memory is used.
- Any donor OS warning or permission prompt.
