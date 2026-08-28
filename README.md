[![AGPLv3 License](https://img.shields.io/badge/license-AGPLv3-blue.svg?style=flat)](http://choosealicense.com/licenses/agpl-3.0/)

# rtlamr-mod-perf

This is a performance-focused rtlamr fork for 64-bit ARM systems. It preserves
the portable Go decoder while selecting verified Cortex-A72 SIMD paths and an
optional direct RTL-SDR input path when their runtime gates pass.

## Performance TL;DR

- On the reference Cortex-A72 combined-protocol workload, the integrated
  decoder and direct-SDR path used roughly **96% less process CPU** than the
  original two-process Go/`rtl_tcp` deployment. This is a workload-specific
  observation, not a portable hardware guarantee.
- Exact integer/NEON power plus Manchester fusion reduced the fixed complete
  decoder boundary **45.19%**. Packed search/layout added **12.54%**, and the
  fixed-L72 R900 SIMD leaf added another **8.93%** at its complete boundary.
  Returning the immutable R900 dispatch descriptor by reference subsequently
  removed a hot Go structure copy, cutting dispatch overhead **18.10%** and
  improving the fixed combined-protocol replay **0.93%**. Pointer receivers for
  parser dispatch, packet slicing, and IDM parsing subsequently removed the
  remaining large receiver copies, with sequential fixed-replay gains of
  **1.43%**, **0.36%**, and **0.68%**. These stage results are sequential
  checkpoints and must not be added together.
- Pure-Go packet extraction and parser work removed nearly all timed parser
  allocation (`298,512 B / 7,173 allocs` to `384 B / 17 allocs` over the
  fixed replay), while direct RTL-SDR input removes the separate `rtl_tcp`
  process and loopback transport.
- The deployed four-slot direct-SDR ring eliminates the hot USB-to-Go copy. A
  bounded screen measured **31.9% less process CPU** than the prior direct-copy
  control; more elaborate asynchronous and persistent-DMA variants lost and
  were rejected.
- An optional Linux kernel-owned ring goes one step further: USB DMA pages are
  mapped read-only into the decoder, and the duty scheduler borrows those same
  pages for recovery. This removes about **9.0 MiB/s** of payload-copy traffic
  at the reference sample rate. An anonymized five-minute live comparison used
  **7.98% less rtlamr CPU** than the already optimized direct-input + gated-DSP
  baseline. A later release-before-wait exchange removed one syscall and cgo
  transition per batch, reducing complete-process task-clock by another
  **4.59%**. Persistent DMA mappings plus a measured 36-block/576 KiB geometry
  then reduced task-clock **8.26%** and context switches **23.9%** relative to
  that immediate control. These are sequential stages, not additive claims.
  TCP remains the default, and the optional module fails open to the ordinary
  direct path.
- On the reference low-throughput direct-SDR pipeline, setting Go's standard
  `GOMAXPROCS=1` reduced median decoder-process CPU by **37.0%** in an
  eight-row counterbalanced screen. It cut scheduler wakeups and migrations
  without changing the decoder, USB ingestion, or DSP algorithms. This is an
  opt-in workload-specific tuning; multi-decoder or otherwise parallel
  workloads should measure their own setting.
- An opt-in self-learning DSP duty scheduler provides safe-seed, shadow,
  gated, audit, periodic full-refresh, and fail-open recovery behavior. It will
  not skip DSP until every configured sender independently clears the selected
  capture target at one-sided 95% confidence and passes promotion hysteresis.
  The default target is **99.5%** (598 clean observations per sender); stricter
  targets remain configurable. Learned watchdogs, per-arrival obligations,
  adaptive history, and recoverable evidence epochs track drift after initial
  qualification. Versioned checkpoints can promote compatible shadow evidence
  into gated mode while forcing continuous-DSP recovery first. Exact confidence
  results are cached by evidence tuple; canonical indexed traversal and
  change-driven qualification keep estimating-state processing cheap without
  weakening per-block wake, watchdog, audit, or fail-open decisions. Sender
  configuration is canonicalized so unchanged policies resume
  deterministically. A capture-target-only change preserves
  independently learned cadence, watchdogs, and evidence after proving every
  other normalized configuration field is identical; qualification and its
  stability timer always restart under the new target. Full-state resumes stay
  fail-open until one live arrival from every previously seen sender reanchors
  cadence phase; those arrivals are not scored as capture evidence, so process
  downtime cannot masquerade as RF drift. Optional site-specific seeds belong
  in a protected runtime policy file, never in the public binary.
- In one short, anonymized live validation at a configurable 99% contract,
  gated mode used **11.0% less process CPU** than the already optimized
  continuous-DSP control over the predeclared 120-second window; a five-minute
  confirmation measured **14.5% less**. No new selected-candidate escape was
  observed, but 99% is a statistical upper risk bound—not a measured 1% loss.
  Both measured windows used less CPU; deployment policy should restore
  continuous DSP automatically if a gated validation is not cheaper. A later
  five-minute production validation measured **17.9% less process CPU**. Its
  attached profile found the remaining work split across DSP, kernel USB
  completion, and the scheduler's raw-IQ collar copy rather than one dominant
  Go routine. See
  [CPU savings versus capture risk](PERFORMANCE.md#cpu-savings-versus-capture-risk).
- The bundled collector has an opt-in 60-second change-or-heartbeat policy for
  R900 data. It emits field changes immediately while bounding unchanged
  heartbeat writes.
- Removing roughly one continuously busy Cortex-A72 core from the reference
  Raspberry Pi 4 workflow corresponds to a modeled **0.8--1.1 W** reduction at
  the board, or about **18--27 Wh/day** and **7--10 kWh/year**. This is a
  data-backed capacity estimate, not a wall-power measurement; USB peripherals,
  storage, RF hardware, power-supply loss, and other services remain. See the
  [energy model and assumptions](PERFORMANCE.md#modeled-energy-implications).

See [PERFORMANCE.md](PERFORMANCE.md) for architecture, benchmark methodology,
accepted changes, rejected design families, and attribution. Operational
deployments, device identities, raw captures, and experiment evidence are
intentionally not stored in this public repository.

### Upstream fork-derived hardening

Reviewing public rtlamr forks identified a small correctness batch in
[`evilgenius79/rtlamr`](https://github.com/evilgenius79/rtlamr). This fork
adapts its CSV error propagation and fail-fast validation for invalid output
formats and message types.

That batch also adapts the source fork's deterministic parser setup and
synthetic R900 coverage to this fork's current architecture. Mixed-protocol
defaults now select the highest registered center frequency independent of Go
map iteration order, and a synthetic Reed-Solomon-valid R900 packet exercises
the Power16 candidate parser and corruption rejection. These changes do not
alter the per-block DSP, NEON, or assembly hot paths.

## Purpose

Utilities often use "smart meters" to optimize their residential meter reading infrastructure. Smart meters transmit consumption information in the various ISM bands allowing utilities to simply send readers driving through neighborhoods to collect commodity consumption information. One protocol in particular: Encoder Receiver Transmitter by Itron is fairly straight forward to decode and operates in the 900MHz ISM band, well within the tunable range of inexpensive rtl-sdr dongles.

This project is a software defined radio receiver for these messages. We make use of an inexpensive rtl-sdr dongle to allow users to non-invasively record and analyze the commodity consumption of their household.

There's now experimental support for data collection and aggregation with [rtlamr-collect](https://github.com/bemasher/rtlamr-collect)!
This fork includes the collector's upstream `v1.0.3` source under
[`cmd/rtlamr-collect`](cmd/rtlamr-collect), with an opt-in bounded R900
change-or-heartbeat coalescer. See that command's README for its exact behavior
and provenance.

## Requirements

- GoLang >=1.21 (Go build environment setup guide: http://golang.org/doc/code.html)
- rtl-sdr
  - Windows: [pre-built binaries](https://ftp.osmocom.org/binaries/windows/rtl-sdr/)
  - Linux: [source and build instructions](http://sdr.osmocom.org/trac/wiki/rtl-sdr)

## Install

Clone this fork to build the performance version:

```bash
git clone --recurse-submodules https://github.com/emrikol/rtlamr-mod-perf.git
cd rtlamr-mod-perf
go install .
```

The command above will add the binary to `$HOME/go/bin/`, or if `$GOPATH` is set, `$GOPATH/bin/`.

To run the rtlamr binary from any directory, ensure the directory containing the binary is in your `PATH` ([more info](https://superuser.com/questions/284342/what-are-path-and-other-environment-variables-and-how-can-i-set-or-use-them)).

## Usage

See the wiki page [Configuration](https://github.com/bemasher/rtlamr/wiki/Configuration) for details on configuring rtlamr.

Running the receiver is as simple as starting an [`rtl_tcp`](https://osmocom.org/projects/rtl-sdr/wiki/Rtl-sdr) instance and then starting the receiver:

```bash
# Terminal A
$ rtl_tcp

# Terminal B
$ rtlamr
```

The animation below shows an example of starting rtlamr along with the successful capture of an ERT message.
![Animation of output when starting rtlamr](assets/run_rtlamr.gif)  

---

If you want to run the spectrum server on a different machine than the receiver you'll need to specify an address to listen on with the `-a` flag for `rtl_tcp`, and the `-server` flag for `rtlamr`.

On Linux, rtlamr can also read the RTL-SDR device directly, without a
separate `rtl_tcp` process or loopback TCP connection. Clone the repository
with its pinned rtl-sdr submodule, install the libusb development headers,
and build the optional direct backend:

```bash
git clone --recurse-submodules https://github.com/emrikol/rtlamr-mod-perf.git
cd rtlamr-mod-perf
CGO_ENABLED=1 go build -tags=rtlsdr
sudo ./rtlamr -source=direct -device=0
```

The ordinary build and the default `-source=tcp` behavior remain unchanged.
`-device` accepts either an RTL-SDR device index or USB serial number.

Linux users may additionally build the optional
[`rtlamr_usb_ring`](kernel/rtlamr_usb_ring) module and pass
`-directkernelring`. It maps kernel-owned USB payload pages directly into the
decoder and fails open to the ordinary direct source when the optional path is
unavailable. The module must match the running kernel; see its README for build
and device-permission details. `-directkernelbatchblocks` selects the kernel
batch geometry and must match the module's `slot_bytes`; its default preserves
the 16-block layout. Ring cancellation and repeated usbfs/kernel handoffs are
deterministic: shutdown stops blocked ring reads, and each START re-establishes
the SDR interface before submitting URBs.

## Message Types

The following message types are supported by rtlamr:

- **scm**: Standard Consumption Message. Simple packet that reports total consumption.
- **scm+**: Similar to SCM, allows greater precision and longer meter ID's.
- **idm**: Interval Data Message. Provides differential consumption data for previous 47 intervals at 5 minutes per interval.
- **netidm**: Similar to IDM, except net meters (type 8) have different internal packet structure, number of intervals and precision. Also reports total power production.
- **r900**: Message type used by Neptune R900 transmitters, provides total consumption and leak flags.
- **r900bcd**: Some Neptune R900 meters report consumption as a binary-coded digits.

## Compatibility

Currently the only tested meter is the Itron C1SR and Itron 40G. However, the protocol is designed to be useful for several different commodities and should be capable of receiving messages from any ERT capable smart meter.

Check out the table of meters I've been compiling from various internet sources: [ERT Compatible Meters](meters.md)

User provided, but otherwise unverified compatible meters: [Google Sheets](https://docs.google.com/spreadsheets/d/1lTeHkk7rwFfq0joMWngrhnJA2nXAk4m82eApVaAKfhw/edit?usp=sharing)

Look for an FCC ID label on your meter, it should identify the two-digit commodity or endpoint type and the eight- or ten-digit endpoint ID of your meter: `## ########[##]`. Below are a few examples:

![Example FCC Label (1)](assets/fcc_label_01.png)
![Example FCC Label (2)](assets/fcc_label_02.png)
![Example FCC Label (3)](assets/fcc_label_03.png)

## Sensitivity

Using a NooElec NESDR Nano R820T with the provided antenna, I can reliably receive standard consumption messages from ~300 different meters and intermittently from another ~600 meters. These figures are calculated from the number of messages received during a 25 minute window. Reliably in this case means receiving at least 10 of the expected 12 messages and intermittently means 3-9 messages.

## Ethics

_Do not use this for malicious purposes._ If you do, I don't want to know about it, I am not and will not be responsible for your actions. However, if you find a clever non-evil use for this, by all means, share.

## Use Cases

These are a few examples of ways this tool could be used:

**Ethical**

- Track down stray appliances.
- Track power generated vs. power consumed.
- Find a water leak with rtlamr rather than from your bill.
- Optimize your thermostat to reduce energy consumption.
- Mass collection for research purposes. (_Please_ anonymize your data.)

**Unethical**

- Using data collected to determine living patterns of specific persons with the intent to act on this data, particularly without express permission to do so.

## License

The source of this project is licensed under Affero GPL v3.0. According to [http://choosealicense.com/licenses/agpl-3.0/](http://choosealicense.com/licenses/agpl-3.0/) you may:

### Required:

- **Disclose Source:** Source code must be made available when distributing the software. In the case of LGPL, the source for the library (and not the entire program) must be made available.
- **License and copyright notice:** Include a copy of the license and copyright notice with the code.
- **Network Use is Distribution:** Users who interact with the software via network are given the right to receive a copy of the corresponding source code.
- **State Changes:** Indicate significant changes made to the code.

### Permitted:

- **Commercial Use:** This software and derivatives may be used for commercial purposes.
- **Distribution:** You may distribute this software.
- **Modification:** This software may be modified.
- **Patent Grant:** This license provides an express grant of patent rights from the contributor to the recipient.
- **Private Use:** You may use and modify the software without distributing it.

### Forbidden:

- **Hold Liable:** Software is provided without warranty and the software author/license owner cannot be held liable for damages.
- **Sublicensing:** You may not grant a sublicense to modify and distribute this software to third parties not included in the license.

## Feedback

If you have any questions, comments, feedback or bugs, please submit an issue.
