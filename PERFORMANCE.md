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

### Kernel-owned mapped ring

An additional opt-in Linux module makes its fixed USB ring the only raw-IQ
payload store. The controller DMA-writes ordinary cacheable pages, and rtlamr
maps them read-only. The decoder reads each mapped block directly. When gated
DSP is active, the 29-block recovery collar borrows those same pages and holds
two complete 16-block USB batches before recycling the oldest slot.

This removes both the kernel-to-userspace IQ copy and the userspace collar
copy. At the reference 2,359,296 complex samples/second, those two eliminated
copies represent about **9.0 MiB/s** of memory-copy traffic. The mandatory USB
DMA write remains. The 16 x 262,144-byte ring reserves 4 MiB to preserve input
headroom; that small capacity increase is intentional.

An anonymized five-minute live sample measured 3.179941% of one Cortex-A72 core
versus the retained 3.455864% gated direct-input baseline: **7.98% less rtlamr
CPU**. A separate 60-second interval measured 3.049675% and supported the same
direction. All configured test streams remained fresh, and the module path had
no restart, lost-profile-sample, or throttle event.

The matching profile no longer contained the former bulk `copy_to_user` or
duty-collar `memmove`. Fused DSP was the largest leaf at 36.01% of sampled
cycles. The kernel-ring descriptor read was 0.12%; ARM64 DMA cache invalidation
was 0.87%. The remaining 0.27% `runtime.memmove` belonged to decoder result
assembly rather than IQ transport. Persistent DMA mapping would still require
the same CPU/device cache-ownership synchronization, so no speculative kernel
SIMD or custom copy loop is retained.

The feature is selected with `-directkernelring`. Missing modules, device
nodes, ABI/geometry mismatches, and unsupported builds continue through the
ordinary direct-SDR path. See
[`kernel/rtlamr_usb_ring`](kernel/rtlamr_usb_ring) for the generic build and
ownership contract.

## Modeled energy implications

Reducing CPU time does not reduce total device power by the same percentage.
The reference end-to-end measurements nevertheless remove approximately one
continuously busy Cortex-A72 core: the original workflow used about 105% of one
core, while a bounded post-integration profile used 2.34%. That is a 97.8%
reduction in decoder-process CPU, equivalent to 25.7% of the four-core Pi 4's
total CPU capacity. The README uses the more conservative rounded claim of
roughly 96% because complete-workflow observations vary with RF traffic and
host activity.

Raspberry Pi's [power documentation](https://www.raspberrypi.com/documentation/computers/raspberry-pi.html#power-supply)
reports approximately 0.6 A average at idle and 1.2 A under stress for a Pi 4,
or about 3 W of idle-to-stress headroom at 5 V. Raspberry Pi's separate
[firmware and thermal testing](https://www.raspberrypi.com/news/thermal-testing-raspberry-pi-4/)
measured 2.36 W at idle and 6.67 W under its worst-case synthetic workload, a
4.31 W range. Scaling those two published dynamic ranges by the removed 25.7%
CPU-capacity equivalent gives 0.77 W and 1.11 W respectively. Rounded to avoid
false precision, the modeled board-level saving is therefore **0.8--1.1 W**.

For a continuously running receiver, that corresponds to approximately:

| Period | Modeled board energy avoided |
| --- | ---: |
| Day | 18--27 Wh |
| 30-day month | 0.55--0.80 kWh |
| Year | 6.7--9.7 kWh |

For battery systems, runtime scales with the complete system draw, not just the
decoder. As an illustration, reducing a measured 5 W installation by 0.9 W
would increase ideal runtime by about 22% (`5 / 4.1 - 1`). For solar sizing,
18--27 Wh/day is equivalent to roughly 7.5--11.3 W of panel capacity at three
peak-sun-hours and 80% end-to-end charging efficiency.

This is a capacity model, not a measured wall-power claim. CPU power is not
perfectly linear, the four cores share voltage and frequency domains, and the
SDR, USB controller, RAM, networking, storage, collector, and other services
continue to draw power. Raspberry Pi also notes that its published figures
exclude additional USB devices. A specific installation needs a counterbalanced
inline USB-C meter or smart-plug A/B run to establish actual input watts and
power-supply losses.

## Adaptive DSP scheduling

The opt-in duty scheduler learns per-sender cadence while SDR ingestion remains
continuous. Its public implementation contains no site-specific sender IDs or
cadence seeds.

Modes are:

- `off`: decode every block, with no scheduler state;
- `shadow`: learn and score skip decisions while still decoding every block;
- `gated`: apply qualified skip decisions, periodically audit skipped regions,
  return to continuous DSP for scheduled refresh windows, and fail open on
  obligation/count/recovery conditions.

Qualification is per sender. The default capture target is 99.5% at one-sided
95% confidence, which requires 598 clean eligible observations before a
zero-miss sender can qualify. The target is adjustable with
`-dutyschedulercapturetarget`. Checkpoints are atomic and versioned; compatible
state resumes exactly. A capture-target-only migration verifies the prior full
configuration fingerprint after substituting only the checkpoint target, then
preserves cadence histories, learned watchdogs, evidence, adaptive state, and
safety deficits. It clears qualification and promotion deadlines and recomputes
them under the new target. Any additional policy difference retains eligible
evidence but forces cadence relearning. Compatible shadow checkpoints may be
promoted into gated mode without repeating the entire evidence run, but every
promotion and gated restart begins with continuous-DSP recovery.

After any complete-state resume, the scheduler also waits fail-open for one
live arrival from every previously seen sender. Each first arrival shifts that
sender's cadence anchor to the new sample stream without adding a watchdog gap,
capture event, miss, or change point. Normal scoring resumes afterward. This is
necessary because the decoder's sample clock counts ingested IQ rather than
wall time; treating process downtime as a sampled interval would manufacture a
phase discontinuity and unnecessarily discard a valid evidence epoch.

The exact Clopper-Pearson bound is cached by each sender's evidence tuple
(`events`, `misses`, and confidence alpha). Qualification timers and fail-open
checks still run for every block, but unchanged evidence does not repeat the
80-step numerical solver. A regression benchmark covering learned senders in
the estimating state fell from about 8.9 microseconds to 0.29 microseconds per
block on the development ARM64 system, with identical bounds and zero
allocations. The cached path measured 1.70--1.72 microseconds per block on the
reference Cortex-A72, or about 0.05% of one core at 288 blocks per second.

Sender inventories are sorted before configuration is fingerprinted. Sender
order has no policy meaning, so this prevents Go map iteration order from
turning an unchanged restart into a false policy migration and unnecessary
cadence relearning.

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

### CPU savings versus capture risk

A short anonymized live trial tested a qualified conservative policy at a
configurable 99% capture target. It used the same optimized decoder and
continuous SDR ingestion for both conditions. After the required ten-minute
continuous-DSP recovery, the scheduler entered gated mode automatically.

The continuous-DSP control used 4.211% of one CPU core. The predeclared
120-second gated window used 3.748%, an **11.01% relative reduction**. A longer
five-minute confirmation used 3.602%, a **14.46% relative reduction**. The
gated report recorded roughly 86,000 skipped DSP blocks, its selected candidate
added no new projected escape, all configured output streams remained fresh,
and no gated-window restart, checkpoint failure, or hardware-throttle event was
observed.

The 99% target does **not** mean the trial measured a 1% accuracy loss. It means
each sender had enough prior shadow evidence for the one-sided 95% confidence
upper bound on its *scheduler-induced* miss rate to be at most 1%. The short
gated window added no candidate miss, but it cannot prove that an unaudited
skipped transmission never occurred. Ordinary RF loss and decoder limitations
are also outside that scheduler-only contract. Random audits, per-sender
obligations, count watchdogs, periodic continuous-DSP refresh, and immediate
fail-open recovery therefore remain part of the safety model.

The operational deployment rule is the configured per-sender capture SLA plus
a measured CPU improvement over continuous DSP; it does not require an
arbitrary minimum percentage. Both windows cleared that rule. A reversible
deployment controller should automatically restore continuous DSP when its
gated validation is equal to or more expensive than the control. In plain
terms, this trial found roughly 11--14% less decoder CPU in exchange for a
statistically bounded scheduler risk below 1%, not a measured 1% reduction in
decoded-message accuracy.

A later five-minute production validation used 3.456% of one core, **17.94%
less** than the 4.211% continuous-DSP control. An attached, restart-free
120-second profile captured 787 cycle samples with none lost. Its largest flat
costs were the fused ARM64 power/Manchester path (15.76%), kernel USB completion
copy-to-user (12.39%), the scheduler's raw-IQ collar `memmove` (10.19%), USB
submit locking (4.93%), and dual-protocol search (2.36%). These percentages are
fractions of the remaining 3.456%-of-one-core workload, not percentages of a
whole core.

The collar currently owns a copy of every input block so skipped IQ can be
replayed safely when decoding wakes. The rebuild algorithm only consumes
skipped entries on a short wake; after a long sleep every retained collar entry
is also skipped. Copying only skipped blocks is therefore the clearest next Go
optimization to prove with wrap, rebuild, replay, and message-equivalence tests.
Kernel USB completion copying is a harder architectural ceiling and cannot be
removed by ordinary Go allocation cleanup.

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
