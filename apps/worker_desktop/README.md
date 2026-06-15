# CMesh Worker Desktop

Flutter desktop shell for joining and controlling a local CMesh worker.

The app is intentionally thin: CMesh core behavior stays in the Go CLI and worker service scripts. This app stores local donor config, runs the existing worker installer/control actions, and displays command output.

## Run

From the repository root:

```sh
cd apps/worker_desktop
fvm flutter run -d macos
```

## Current Scope

- Manager URL and join token input.
- CPU, RAM, disk, GPU, and VRAM limits.
- Background service toggle.
- Connect, status, start, stop, and uninstall actions.
- Local config persistence at `~/.cmesh/worker-desktop.json`.

## Next Step

Replace direct script execution with a local CMesh worker control API. The Flutter app should talk to `127.0.0.1` and the Go daemon should own service installation, status, logs, and worker lifecycle.
