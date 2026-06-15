# CMesh Worker Desktop

Flutter desktop shell for joining and controlling a local CMesh worker.

The app is intentionally thin: CMesh core behavior stays in the Go CLI. This app talks to the local worker control API and displays status/output.

Start the local control API before opening the app:

```sh
cmesh worker control
```

## Run

From the repository root:

```sh
cd apps/worker_desktop
fvm flutter run -d macos
```

## Current Scope

- Manager URL and join token input.
- CPU, RAM, disk, GPU, and VRAM limits.
- Connect, status, start, stop, and disconnect actions.
- Local config persistence at `~/.cmesh/worker-desktop.json`.
- Worker process output from the local control API.

## Next Step

Bundle the Go `cmesh` binary with the desktop app and start the local control API automatically. After that, add signed installers and a privileged helper for OS service installation/removal.
