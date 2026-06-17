# CMesh Alpha Release Checklist

Use this checklist before tagging or deploying an alpha release. The goal is to prove that a private cluster can connect workers, account for resources, install a model, run local inference, and clean up storage.

## 1. Local Dry Run

- Run `make release-dry-run VERSION=v0.1.0-alpha.local`.
- Confirm Go tests, Flutter analyze/tests, CLI build, Worker desktop build, DMG packaging, and manager smoke checks pass.
- Inspect `dist/release-dry-run/report.txt`.
- Confirm the dry run did not publish a tag, GitHub release, or VPS deploy.

## 2. Manager Smoke

- Start the manager locally or on the alpha host.
- Open the dashboard.
- Confirm these tabs render separately:
  - `Readiness`
  - `Workers`
  - `Installed Models`
  - `Model Catalog`
  - `Model Activity`
  - `Chat`
- Confirm offline workers are not mixed into the active worker inventory.

## 3. Worker Join

- Install the latest Worker app from the release candidate artifact.
- Open the manager invite page and use the worker invite link.
- Save the connection on first launch.
- Start the worker.
- Confirm the dashboard shows the worker online with CPU, memory, storage, job slots, runtime status, and heartbeat age.

## 4. Runtime Repair

- Open the Worker app `Runtime` tab.
- Confirm `llama.cpp` status is visible.
- If missing, run `Repair runtime`.
- Confirm the runtime becomes ready and the manager readiness screen reflects a runtime-ready worker after heartbeat refresh.

## 5. Storage Accounting

- On the manager `Workers` tab, record:
  - allowed storage
  - free disk
  - CMesh cache usage
  - model usage
  - runtime usage
- Confirm values change after model install and delete.
- Confirm install is blocked when free disk is lower than the model requirement.

## 6. Model Install

- Install the tiny smoke model first.
- Watch Worker app job progress while the model downloads.
- Confirm manager `Installed Models` shows the model, worker, path, size, and runtime readiness.
- Confirm `Model Activity` shows a successful `model.install` job.

## 7. Model Chat

- Open `Chat`.
- Select a ready model and worker.
- Ask a short prompt.
- Confirm the response is local and `Model Activity` records a `model.generate` job.
- Ask a follow-up in the same conversation and confirm conversation context is preserved.

## 8. Model Delete

- Delete the installed model from `Installed Models`.
- Confirm `Model Activity` shows `model.delete` succeeded.
- Confirm the model disappears from `Installed Models` after heartbeat refresh.
- Confirm CMesh model storage usage decreases.
- Confirm model-scoped memory and conversations are removed or no longer active for that model.

## 9. Multi-Worker Smoke

- Connect at least two workers.
- Run a cluster benchmark.
- Confirm scheduler respects job slots and does not assign work to full or under-resourced workers.
- Install a model on one worker and confirm chat targets only workers where that model is installed and runtime-ready.

## 10. Release Decision

- Review known issues in the release notes.
- Confirm the release candidate version is visible in the Worker app.
- Confirm notarization/signing status for macOS artifacts when publishing public builds.
- Only tag and publish after the checklist is complete.
