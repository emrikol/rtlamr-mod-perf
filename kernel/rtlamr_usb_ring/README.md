# Optional Linux USB ring

`rtlamr_usb_ring` is an opt-in Linux driver for the common RTL2838 USB device
(`0bda:2838`). It keeps SDR ingestion in a fixed kernel-owned ring and maps the
completed payload pages read-only into rtlamr. The decoder consumes those pages
directly; a gated duty-scheduler collar may retain references to older slots
instead of copying raw IQ into another ring.

The module is deliberately a streaming transport, not a tuner driver. The
vendored librtlsdr configures the demodulator and tuner, releases interface
zero, and asks Linux to bind this driver for bulk-IN streaming. On shutdown it
reclaims the interface so librtlsdr can deinitialize the hardware normally.
Before each ring START, the driver re-selects the interface's current alternate
setting and then submits the initial URBs. This is required to establish a
fresh endpoint context after the usbfs-to-kernel ownership handoff; successful
URB submission alone does not prove that the endpoint is advancing.

## Build

Build against the headers for the running kernel:

```sh
make -C kernel/rtlamr_usb_ring
sudo insmod kernel/rtlamr_usb_ring/rtlamr_usb_ring.ko
```

The defaults are 16 slots of 262,144 bytes. Both are load-time parameters:

```sh
sudo insmod kernel/rtlamr_usb_ring/rtlamr_usb_ring.ko \
  ring_slots=16 slot_bytes=262144
```

`slot_bytes` must equal the decoder block size multiplied by rtlamr's
`-directkernelbatchblocks` value. The default is 16 blocks. Larger batches
reduce ownership crossings but increase cache-synchronization volume and
latency, so they should be selected from whole-process measurements rather
than assumed to be universally faster.

Grant the rtlamr process read access to `/dev/rtlamr_usb_ring0`, then build the
direct backend and opt in:

```sh
CGO_ENABLED=1 go build -tags=rtlsdr
./rtlamr -source=direct -directkernelring -device=0
```

If the optional module, device node, ABI, or geometry is unavailable, rtlamr
continues through its ordinary direct-SDR ring. TCP input and builds without
the `rtlsdr` tag are unchanged.

The module must be rebuilt for each kernel ABI. A production service should
load it before rtlamr and use an ordinary udev rule to assign the device node to
the service account.

## Data path

- Ordinary cacheable pages are allocated and DMA-mapped once during USB probe.
- URBs and slot metadata are allocated once; the steady path allocates no IQ
  payload storage.
- The driver performs the architecture-correct CPU/device ownership sync at
  each handoff without unmapping and remapping the payload.
- ABI v2 combines the previous slot's release with the next blocking claim in
  one ioctl. Release and resubmission happen before the wait, preserving queue
  depth and avoiding an empty-ring dependency.
- Userspace exchanges only fixed-size ownership descriptors; payload bytes
  remain in the mapped ring.
- Slots are released in sequence and cannot be submitted again while the
  decoder or recovery collar may still reference their bytes.
- Cancellation sends STOP before waiting for the blocked reader. STOP wakes
  descriptor reads and cancels anchored URBs, so normal shutdown does not rely
  on a watchdog or forced process termination.

The cacheable mapping is intentional. Coherent USB allocations avoid explicit
cache maintenance but can make sustained CPU reads substantially slower on
ARM systems. Handwritten SIMD is not used because no payload-copy loop remains.
