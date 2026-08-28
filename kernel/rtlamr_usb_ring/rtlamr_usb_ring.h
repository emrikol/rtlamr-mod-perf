/* SPDX-License-Identifier: GPL-2.0 WITH Linux-syscall-note */
#ifndef RTLAMR_USB_RING_H
#define RTLAMR_USB_RING_H

#include <linux/ioctl.h>
#include <linux/types.h>

#define RTLAMR_USB_RING_ABI 1U
#define RTLAMR_USB_RING_DEVICE "/dev/rtlamr_usb_ring0"

struct rtlamr_usb_ring_info {
	__u32 abi;
	__u32 slot_count;
	__u32 slot_bytes;
	__u32 endpoint;
};

struct rtlamr_usb_ring_completion {
	__u64 sequence;
	__u32 slot;
	__u32 length;
	__s32 status;
	__u32 reserved;
};

struct rtlamr_usb_ring_release {
	__u64 sequence;
	__u32 slot;
	__u32 reserved;
};

/*
 * Atomically return the previously consumed slot and claim the next completed
 * slot.  This additive ioctl keeps ABI-1 read/release clients compatible while
 * allowing new clients to remove one syscall and one io_mutex acquisition per
 * ring turn.
 */
struct rtlamr_usb_ring_exchange {
	__u32 release_valid;
	__u32 reserved;
	struct rtlamr_usb_ring_release release;
	struct rtlamr_usb_ring_completion completion;
};

struct rtlamr_usb_ring_stats {
	__u64 completions;
	__u64 releases;
	__u64 submit_errors;
	__u64 transfer_errors;
	__u64 short_transfers;
};

#define RTLAMR_USB_RING_IOC_MAGIC 'R'
#define RTLAMR_USB_RING_IOC_INFO \
	_IOR(RTLAMR_USB_RING_IOC_MAGIC, 0x00, struct rtlamr_usb_ring_info)
#define RTLAMR_USB_RING_IOC_START \
	_IO(RTLAMR_USB_RING_IOC_MAGIC, 0x01)
#define RTLAMR_USB_RING_IOC_RELEASE \
	_IOW(RTLAMR_USB_RING_IOC_MAGIC, 0x02, struct rtlamr_usb_ring_release)
#define RTLAMR_USB_RING_IOC_STOP \
	_IO(RTLAMR_USB_RING_IOC_MAGIC, 0x03)
#define RTLAMR_USB_RING_IOC_STATS \
	_IOR(RTLAMR_USB_RING_IOC_MAGIC, 0x04, struct rtlamr_usb_ring_stats)
#define RTLAMR_USB_RING_IOC_EXCHANGE \
	_IOWR(RTLAMR_USB_RING_IOC_MAGIC, 0x05, struct rtlamr_usb_ring_exchange)

#endif
