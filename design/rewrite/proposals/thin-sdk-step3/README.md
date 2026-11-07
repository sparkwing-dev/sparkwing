# Host exec (thin SDK step 3) measurements

Option A moves command execution from the SDKs into the host. P1 is `Run`: the host
writes output straight to the masked job log and returns only 64 KiB tails. P2 is
`Capture`: the host relays every byte back to the SDK. The question was how both
compare with today's real paths, not with a bare spawn.

## Variants

Every variant runs product code. `TestHostExecBenchmark` (`internal/nodehost/host_exec_bench_test.go`)
starts a real protocol session with a real Go or TypeScript SDK child and runs one bench step.
The host writes each masked log record as JSON to a buffered file, standing in for log storage.

| Variant | Who spawns | Output |
| --- | --- | --- |
| D | SDK, plain `os/exec` / `child_process` | discarded, unmasked (process spawn floor) |
| T | SDK, today's `Run` | base64 frames to the host, which masks and logs (today's real log path) |
| TC | SDK, today's `Capture` | kept in the SDK, nothing logged |
| P1 | host, `Run` with `host_exec` | host masks and logs; SDK gets tails |
| P2 | host, `Capture` with `host_exec` | host relays every byte as base64 lines; nothing logged |

Small commands are `/usr/bin/true`. Bulk commands are `yes <80-byte line> | head -c N`, so
logged output exercises the masked line presenter the way build logs do. Concurrency 1
and 8. Each round runs all five variants for a cell in a rotated order, reversed on odd
rounds. Ratios and added latency are medians of same-round pairs.

`bench.py` drives runs, keeps one gzip JSON per run (every latency sample, wall, host
and SDK CPU, log record and byte counts, load at start and end), and summarizes. Run:

```sh
go test -c -o nodehost.test ./internal/nodehost/
python3 design/rewrite/proposals/thin-sdk-step3/bench.py run --binary nodehost.test --root . --out <dir> --conditions quiet,loaded
python3 design/rewrite/proposals/thin-sdk-step3/bench.py summarize <dir>
```

## Machines and conditions

| Dataset | Machine | Binary | Runs | Small / bulk size | Conditions |
| --- | --- | --- | --- | --- | --- |
| `results/pi/` | Raspberry Pi 4 Model B, 4 cores, Ubuntu 6.8 raspi kernel, Node 22.18.0 | linux/arm64 from `d828e6545`, sha256 `e26755b97bb087fc...` | 400/400, none failed | 1000 commands / 8 MiB per command | quiet (1-min load < 2.0), loaded |
| `results/desktop/` | WSL2 desktop, 16 threads, Node 26.13.0 | linux/amd64 from `d828e6545`, sha256 `f600c93610279559...` | 142/200, stopped | 2000 commands / 32 MiB per command | shared, not quiet |
| `results/*-smoke/` | both | same | one round | reduced sizes | harness checks, kept as data |

Loaded uses the prototype's load: three CPU busy loops and one writer that fsyncs a 64 MiB
ring, stabilized for 30 s. `results/pi/monitor.ndjson` has per-second load.

Caveats, all recorded in the raw data:

- The Pi was under-voltage throttled throughout (`vcgencmd get_throttled` 0x50005, ARM clock
  sampled at 600-1300 MHz). Its power supply needs replacing before Pi numbers are compared
  across sessions. All variants ran under the same throttling, so ratios stand.
- On this Pi a first-touch page fault costs about 26 us, 17x the desktop. The Go SDK child pays
  it while growing capture buffers, while GC workers spin in `suspendG`
  (`results/pi-diagnostics/`, CPU profiles from `HOSTEXEC_SDK_CPUPROFILE`). It hits today's T and
  TC as hard as P2, which is why Go bulk T and TC are so slow on the Pi.
- The Pi driver was stopped for about 12 minutes between quiet runs while those profiles were
  taken; no run was in flight.
- The desktop was shared with other lanes. Its first 7 runs used load-average admission, the
  rest at least 60% idle CPU, and the lead stopped the run when desktop load reached about 37.
  The bench's own concurrency-8 bulk runs added to that load. Treat desktop numbers as
  corroboration, not as quiet measurements. No load generator ever ran on the desktop.
- The TypeScript child's byte counter lost concurrent updates (`bytes += await one()`), so
  `bytes_check` is false for TypeScript bulk concurrency 8. Throughput uses the requested size,
  and `TestHostExecMatchesSDKExecution` proves full capture. The harness is fixed after these runs.

Machine names, users and home directories in the raw data are replaced with
`<pi-host>`, `<pi-user>`, `<pi-home>`, `<desktop-host>`, `<desktop-user>` and
`<desktop-home>`. In the CPU profiles the placeholders are padded with `_` to the
original length so the protobuf still parses. `bench.py` no longer records argv, the
machine name or run paths.

## Results

Pi, quiet and loaded. P1 is compared with T (today's masked log path), P2 with TC (today's capture).
"vs D" compares with a bare spawn that discards output.

| Lang | Size | C | Variant | Base | Added p50 ms quiet | loaded | vs base quiet | loaded | vs D quiet | loaded |
| --- | --- | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| go | small | 1 | P1 | T | 0.906 | 1.087 | 87.3% | 88.6% | 77.7% | 66.1% |
| go | small | 1 | P2 | TC | 0.830 | 1.864 | 83.9% | 84.0% | 77.4% | 67.7% |
| go | small | 8 | P1 | T | 0.120 | 1.348 | 103.8% | 102.5% | 72.3% | 71.2% |
| go | small | 8 | P2 | TC | 0.845 | 3.038 | 103.3% | 96.0% | 68.0% | 71.5% |
| go | bulk | 1 | P1 | T | -18904 | -4850 | 909.9% | 221.4% | 1.8% | 4.7% |
| go | bulk | 1 | P2 | TC | -2033 | 1134 | 133.4% | 32.5% | 0.8% | 14.5% |
| go | bulk | 8 | P1 | T | -41142 | -47471 | 335.5% | 279.4% | 2.1% | 3.0% |
| go | bulk | 8 | P2 | TC | 8245 | 8702 | 59.6% | 27.4% | 1.6% | 6.8% |
| typescript | small | 1 | P1 | T | -9.251 | -16.005 | 245.4% | 202.3% | 235.2% | 177.8% |
| typescript | small | 1 | P2 | TC | -8.978 | -15.389 | 242.3% | 199.4% | 237.8% | 172.7% |
| typescript | small | 8 | P1 | T | -99.696 | -183.450 | 697.3% | 458.5% | 653.7% | 406.3% |
| typescript | small | 8 | P2 | TC | -88.930 | -178.160 | 441.8% | 452.2% | 416.5% | 417.3% |
| typescript | bulk | 1 | P1 | T | -3010 | -4360 | 224.1% | 218.7% | 6.4% | 10.5% |
| typescript | bulk | 1 | P2 | TC | 495 | 679 | 37.6% | 47.1% | 21.0% | 30.3% |
| typescript | bulk | 8 | P1 | T | -27367 | -39726 | 266.6% | 267.8% | 4.2% | 8.4% |
| typescript | bulk | 8 | P2 | TC | 4059 | 5557 | 27.6% | 38.4% | 13.5% | 22.5% |

Bulk rows compare MiB/s; small rows compare commands per second. A bulk "added p50" is the
whole command's duration difference. Pi spawn costs for scale: Go D p50 4.3 ms, TypeScript
D p50 14.7 ms. Today's T logs bulk output at 0.4-1.5 MiB/s on the Pi; P1 reaches 3.3-3.7 MiB/s.

Desktop (shared, 3-4 rounds per cell) agrees: P1 is 1.8-2.4x T on bulk output (about
38 MiB/s against 17-20 MiB/s) and 2-7x T on TypeScript small commands. Go small commands
at concurrency 1 add 0.30 ms p50 (70% of T's rate); at concurrency 8 P1 matches T. P2
bulk capture reaches 11-53% of TC. Full tables: `results/pi/summary.md`,
`results/desktop/summary.md`.

## Reading against the guardrail

The guardrail asks for small per-call overhead against process spawn and large-output
throughput near direct. P1 is held to today's masked log path, since that path, not a bare
spawn, is what a logged command costs now.

- P1 passes. It is faster than today on every bulk and TypeScript cell, quiet and loaded,
  because the host reads the pipe once instead of receiving base64 frames from the SDK.
  Go small commands at concurrency 1 cost 0.9-1.1 ms more on the Pi (20-25% of a 4.3 ms
  spawn) and 0.3 ms on the desktop, at 87-89% of today's rate. At concurrency 8 its throughput
  matches today's (102-104%). Under load P1 stays at or above 88% of today's rate in every cell
  and above 200% in every bulk and TypeScript cell.
- P2 passes for small commands, which is what capture on demand is for (`git rev-parse`,
  `go env`): 84-103% of today in Go and 2-4.5x in TypeScript. It fails the bulk bound, at
  27-60% of today's capture except Go concurrency 1 quiet. The base64 JSON relay costs
  CPU on both ends.
- Logged output never relays through the SDK, so log bytes stay bounded by the host's masked
  presenter. That presenter and record encoding, not the transport, cap P1 at about 38 MiB/s
  on the desktop and 3.5 MiB/s on the Pi.

## Levers not taken

- P2 bulk: binary framing on the request pipe, or P3 descriptor passing on Unix, would remove
  the base64 JSON cost. Held until a pipeline captures large output.
- Per-call cost: the SDK sends its whole environment with every command, so a delta against
  the previous command would shorten frames. The host already decodes exec frames once.
- Log presenter throughput bounds both T and P1; it is the next bottleneck for chatty builds.
