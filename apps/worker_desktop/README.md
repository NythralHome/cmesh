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

## Current Scope

- Manager URL and join token input.
- CPU, RAM, disk, GPU, and VRAM limits.
- Connect, status, start, stop, and disconnect actions.
- Local config persistence at `~/.cmesh/worker-desktop.json`.
- Worker process output from the local control API.

## Next Step

Add signed installers and a privileged helper for OS service installation/removal.
