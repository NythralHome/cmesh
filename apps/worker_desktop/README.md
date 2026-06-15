# CMesh Worker Desktop

Flutter desktop shell for joining and controlling a local CMesh worker.

The app is intentionally thin: CMesh core behavior stays in the Go CLI. This app talks to the local worker control API and displays status/output.

The app starts the local control API automatically when it can find a `cmesh` binary. For development, use the repository Make target so the binary path is passed in:

```sh
make worker-desktop-run
```

## Run

Manual run from the repository root:

```sh
make build
cd apps/worker_desktop
CMESH_WORKER_CONTROL_BIN=../../bin/cmesh fvm flutter run -d macos
```

The app passes a random local control token to `cmesh worker control` and sends it with `X-CMesh-Control-Token` for `/v1/*` requests.

Invite links can prefill the manager URL and join token:

```text
cmesh://join?manager=https%3A%2F%2Fcmesh.example.com&token=replace-with-token
```

For development, pass an invite through `CMESH_INVITE_URL`.

## Current Scope

- Manager URL and join token input.
- CPU, RAM, disk, GPU, and VRAM limits.
- Connect, status, start, stop, and disconnect actions.
- Local config persistence at `~/.cmesh/worker-desktop.json`.
- Worker process output from the local control API.

## Next Step

Add signed installers and a privileged helper for OS service installation/removal.
