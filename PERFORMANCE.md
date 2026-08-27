# Performance architecture

This document describes the reusable performance work in this fork. It is
deliberately public-safe: it does not contain deployment paths, device IDs,
captured messages, hostnames, raw RF data, or internal experiment records.

## Scope and measurement

The fork starts from upstream rtlamr `v0.9.5` and keeps the portable Go decoder
as the semantic reference. Architecture-specific paths are selected only when
their build, CPU, geometry, and self-test gates pass; otherwise execution falls
back to the portable implementation.

Performance claims use fixed-work comparisons on identical input, with an
A/A noise calibration followed by counterbalanced A/B runs. Candidate output
must match the reference messages, checksums, ordering, work counts, and native
dispatch identities. Stage improvements below are sequential checkpoints and
must not be added together.

The main reference workload uses the combined IDM and R900 configuration on a
Cortex-A72. Results on other CPUs, protocol mixtures, SDRs, sample rates, and RF
conditions may differ.

## Accepted decoder changes

### Integer power and Manchester fusion

The original floating-point magnitude path was replaced with exact integer
power, then fused with fixed-geometry Manchester production. A native NEON
implementation handles the verified Cortex-A72 geometry while portable Go
remains available.

At the complete fixed decoder boundary, this checkpoint reduced wall time by
about **45.19%** on the reference workload.

### Packed Power16 history and search

The decoder writes packed Power16 decisions directly into its history ring and
searches the packed representation without rematerializing the earlier byte
layout. The accepted packed search/layout checkpoint reduced the same complete
boundary by a further **12.54%** relative to its immediate control.

### Fixed-L72 R900 correlation

The common R900 geometry uses a gated Cortex-A72 SIMD quantizer. Its complete
decoder-boundary checkpoint was **8.93%** faster than the corresponding Go
control. Unsupported geometry, CPU identity, self-test failure, or the kill
switch selects the Go implementation.

### Parser and allocation cleanup

Packet extraction now walks owned rings and candidate indices directly. R900
parsing avoids unused packet materialization and repeated string work. On the
fixed replay used during development, timed parser allocation fell from roughly
`298 KiB / 7,173 allocations` to `384 B / 17 allocations` while preserving the
decoded output.

## Input pipeline

### Direct RTL-SDR source

Linux builds with the `rtlsdr` tag can read the SDR directly. This removes the
separate `rtl_tcp` process, the loopback TCP transport, and redundant buffering.
The ordinary TCP source remains the default and needs no CGO dependency.

The direct backend vendors the official Osmocom rtl-sdr source as a pinned Git
submodule:

- source: <https://gitea.osmocom.org/sdr/rtl-sdr.git>
- path: `third_party/rtl-sdr`

The four-slot direct ring transfers ownership of completed USB buffers to the
decoder and returns them after processing. This removed the hot USB-to-Go copy;
a bounded screen measured roughly **31.9% less process CPU** than the earlier
direct-copy control. More elaborate persistent-DMA and asynchronous pipeline
variants did not improve the complete boundary and were not retained.

## Adaptive DSP scheduling

The opt-in duty scheduler learns per-sender cadence while SDR ingestion remains
continuous. Its public implementation contains no site-specific sender IDs or
cadence seeds.

Modes are:

- `off`: decode every block, with no scheduler state;
- `shadow`: learn and score skip decisions while still decoding every block;
- `gated`: apply qualified skip decisions, periodically audit skipped regions,
  periodically audit skipped regions, return to continuous DSP for scheduled
  refresh windows, and fail open on obligation/count/recovery conditions.

Qualification is per sender. The default capture target is 99.5% at one-sided
95% confidence, which requires 598 clean eligible observations before a
zero-miss sender can qualify. The target is adjustable with
`-dutyschedulercapturetarget`. Checkpoints are atomic and versioned; compatible
state resumes exactly, while policy changes retain eligible evidence but force
cadence relearning. Compatible shadow checkpoints may be promoted into gated
mode without repeating the entire evidence run, but every promotion and gated
restart begins with continuous-DSP recovery.

Sender-specific watchdog values are operational data. The public default leaves
static count/deadline thresholds disabled rather than compiling private cadence
or endpoint identities into the binary. A protected JSON file selected with
`-dutyschedulerpolicy` can supply optional per-sender seeds and controller
limits. Unknown or duplicate sender IDs, unknown fields, malformed durations,
symlinks, group/world-accessible policy files, and invalid controller bounds
fail closed. Policy files must be private regular files (for example, mode
`0600`).

The generic controller learns a conservative overdue horizon and rolling-count
floor independently for each sender. Every accepted arrival opens the next
arrival obligation; an expired obligation, rolling-count deficit, audited
out-of-window arrival, protocol change, or clock discontinuity immediately
returns gated mode to continuous DSP. Protocol totals are telemetry only—a
healthy sender or protocol can never satisfy another sender's gate.

The raw-IQ collar has two wake paths. A complete short sleep still present in
the collar is replayed through the existing decoder state, preserving the same
message sequence as continuous DSP. A longer sleep creates a fresh decoder and
uses the collar as warmup. Any configured-sender message recovered from a
skipped block is explicitly reported as an escape, even though replay occurs
after the controller clock has entered an awake block, and therefore triggers
immediate fail-open recovery.

Cadence fitting retains up to 128 intervals while adapting its effective
history through 8, 16, 32, 64, and 128 observations. A residual change point
falls back to recent history, widens the wake envelope, raises the audit rate,
shortens the next refresh interval, and starts a fresh confidence epoch. A
candidate can rehabilitate after recovery and fresh evidence. Clean full-DSP
refreshes cautiously tighten a previously widened envelope, again requiring a
fresh evidence epoch before suppression resumes.

Default gated safeguards are:

- at least 10% randomized whole-quiet-interval audits;
- ten minutes of continuous-DSP recovery;
- ten minutes of continuous DSP every six hours;
- a stricter promotion bound plus a ten-minute stability period, with immediate
  demotion at the configured capture contract; and
- learned watchdogs after 16 intervals, using up to 128 recent gaps.

All defaults are generic and configurable through the protected policy file.
The capture contract, continuous SDR ingestion, owned raw-IQ collar, and
fail-open direction are invariant.

Example policy using a synthetic sender ID:

```json
{
  "schema": "rtlamr-duty-scheduler-policy-v1",
  "refresh_interval": "6h",
  "refresh_duration": "10m",
  "senders": [
    {
      "id": 12345678,
      "overdue": "5m",
      "count_window": "10m",
      "minimum_count": 2
    }
  ]
}
```

The scheduler remains opt-in. Qualification is evidence for a reversible gated
trial, not a claim that every RF environment is stationary or lossless.

## Collector write coalescing

`cmd/rtlamr-collect` includes an opt-in R900 change-or-heartbeat coalescer. When
enabled, a point is written immediately when any retained field changes;
otherwise one unchanged heartbeat is emitted after the configured interval.
Other protocols pass through unchanged. A zero interval disables coalescing.

This reduces redundant database writes without turning the stream into a
change-only feed, so downstream consumers still receive bounded liveness.

## Upstream and fork provenance

- Original project: [bemasher/rtlamr](https://github.com/bemasher/rtlamr)
- RTL-SDR implementation: [Osmocom rtl-sdr](https://gitea.osmocom.org/sdr/rtl-sdr)
- Collector lineage: [bemasher/rtlamr-collect](https://github.com/bemasher/rtlamr-collect)
- CSV error propagation and fail-fast validation were adapted from
  [evilgenius79/rtlamr](https://github.com/evilgenius79/rtlamr), then fitted to
  this fork's parser registration and tests.

Those correctness changes do not alter the DSP hot path.

## Rejected design families

Several ideas were measured and intentionally left out of the public runtime:

- extra asynchronous decoder queues and multi-core fan-out, where handoff,
  scheduling, and cache costs outweighed useful work;
- persistent-DMA layouts beyond the four-slot ownership ring;
- GPU/V3D offload for the current small streaming kernels, where submission and
  synchronization dominated;
- narrower receiver/channelization schemes that could not preserve the full RF
  visibility contract;
- early rejection heuristics that could not prove message preservation.

These are not promises that every future workload will behave identically.
They document why the current fork favors a short ownership pipeline, exact
native DSP leaves, and portable semantic fallbacks.

## Building

Portable build:

```bash
go build ./...
```

Direct RTL-SDR build on Linux with libusb development headers installed:

```bash
git submodule update --init --recursive
CGO_ENABLED=1 go build -tags=rtlsdr .
```

Run the normal test suite before publishing changes:

```bash
go test ./...
```

Architecture-sensitive changes should additionally be cross-built for
`linux/arm64` and verified on the target CPU before their benchmark results are
generalized.
