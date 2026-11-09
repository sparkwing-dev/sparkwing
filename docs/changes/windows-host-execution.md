### Changed

- **SDK (Breaking):** Windows runs Go and TypeScript commands in the host, with Job Object tree cleanup, masked 64 KiB `Run` tails and full `Capture` relay. Upgrade SDK pins with the host. See [the migration guide](docs/migrations/_unreleased.md#windows-commands-run-in-the-host).

### Fixed

- **Windows CLI:** Native shell drive-directory metadata no longer blocks environment capture, so commands launched through `cmd.exe` can submit pipelines.
- **Windows controller:** Reused named-pipe HTTP connections keep requests alive, so local runners can claim work and reconnect after daemon restart.
- **Windows credentials:** Created and rotated credential-epoch files receive private access lists, so the controller can reopen its state.
- **CLI:** Standalone runs close unused API connections before controller shutdown, preventing an intermittent exit failure after a successful pipeline on Windows.
