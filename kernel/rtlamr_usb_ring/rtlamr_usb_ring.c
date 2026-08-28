// SPDX-License-Identifier: GPL-2.0-only
/*
 * Persistent, mmap-backed bulk-IN ring for RTLAMR's direct RTL-SDR source.
 *
 * Payload pages use the ordinary USB-core DMA mapping path so that CPU reads
 * remain cacheable after completion.  The steady path allocates nothing and
 * never copies payload bytes across the kernel/userspace boundary.
 */

#include <linux/atomic.h>
#include <linux/fs.h>
#include <linux/kref.h>
#include <linux/mm.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/mutex.h>
#include <linux/poll.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/uaccess.h>
#include <linux/usb.h>
#include <linux/wait.h>

#include "rtlamr_usb_ring.h"

#define RTLAMR_USB_VENDOR 0x0bda
#define RTLAMR_USB_PRODUCT 0x2838
#define RTLAMR_DEFAULT_SLOTS 16U
#define RTLAMR_DEFAULT_SLOT_BYTES (16U * 16384U)
#define RTLAMR_MAX_SLOTS 64U

static unsigned int ring_slots = RTLAMR_DEFAULT_SLOTS;
module_param(ring_slots, uint, 0444);
MODULE_PARM_DESC(ring_slots, "number of persistent USB ring slots");

static unsigned int slot_bytes = RTLAMR_DEFAULT_SLOT_BYTES;
module_param(slot_bytes, uint, 0444);
MODULE_PARM_DESC(slot_bytes, "bytes in each persistent USB ring slot");

enum rtlamr_slot_state {
	RTLAMR_SLOT_FREE = 0,
	RTLAMR_SLOT_INFLIGHT,
	RTLAMR_SLOT_READY,
	RTLAMR_SLOT_CLAIMED,
};

struct rtlamr_usb_ring;

struct rtlamr_usb_slot {
	struct rtlamr_usb_ring *ring;
	struct urb *urb;
	void *data;
	u64 sequence;
	u32 length;
	int status;
	enum rtlamr_slot_state state;
};

struct rtlamr_usb_ring {
	struct usb_device *udev;
	struct usb_interface *interface;
	struct miscdevice misc;
	struct usb_anchor submitted;
	struct rtlamr_usb_slot *slots;
	wait_queue_head_t wait;
	spinlock_t lock;
	struct mutex io_mutex;
	struct kref refs;
	atomic_t opened;
	bool disconnected;
	bool stopping;
	bool started;
	int fatal_error;
	u8 endpoint;
	u32 slot_count;
	u32 slot_bytes;
	u64 next_claim;
	u64 next_release;
	u64 next_submit;
	atomic64_t completions;
	atomic64_t releases;
	atomic64_t submit_errors;
	atomic64_t transfer_errors;
	atomic64_t short_transfers;
};

static void rtlamr_ring_stop(struct rtlamr_usb_ring *ring);

static void rtlamr_ring_free(struct kref *ref)
{
	struct rtlamr_usb_ring *ring = container_of(ref, struct rtlamr_usb_ring,
						    refs);
	u32 index;

	for (index = 0; index < ring->slot_count; index++) {
		usb_free_urb(ring->slots[index].urb);
		if (ring->slots[index].data)
			free_pages_exact(ring->slots[index].data, ring->slot_bytes);
	}
	kfree(ring->slots);
	usb_put_dev(ring->udev);
	kfree(ring);
}

static void rtlamr_complete(struct urb *urb)
{
	struct rtlamr_usb_slot *slot = urb->context;
	struct rtlamr_usb_ring *ring = slot->ring;
	unsigned long flags;

	spin_lock_irqsave(&ring->lock, flags);
	if (ring->stopping || ring->disconnected) {
		slot->state = RTLAMR_SLOT_FREE;
	} else if (urb->status) {
		slot->status = urb->status;
		slot->state = RTLAMR_SLOT_READY;
		ring->fatal_error = urb->status;
		atomic64_inc(&ring->transfer_errors);
	} else if (urb->actual_length != ring->slot_bytes) {
		slot->length = urb->actual_length;
		slot->status = -EMSGSIZE;
		slot->state = RTLAMR_SLOT_READY;
		ring->fatal_error = -EMSGSIZE;
		atomic64_inc(&ring->short_transfers);
	} else {
		slot->length = urb->actual_length;
		slot->status = 0;
		slot->state = RTLAMR_SLOT_READY;
		atomic64_inc(&ring->completions);
	}
	spin_unlock_irqrestore(&ring->lock, flags);
	wake_up_interruptible(&ring->wait);
}

static int rtlamr_submit_slot(struct rtlamr_usb_ring *ring,
			      struct rtlamr_usb_slot *slot)
{
	int result;

	usb_anchor_urb(slot->urb, &ring->submitted);
	result = usb_submit_urb(slot->urb, GFP_KERNEL);
	if (result) {
		usb_unanchor_urb(slot->urb);
		atomic64_inc(&ring->submit_errors);
	}
	return result;
}

static int rtlamr_ring_start(struct rtlamr_usb_ring *ring)
{
	unsigned long flags;
	u32 index;
	int result = 0;

	mutex_lock(&ring->io_mutex);
	spin_lock_irqsave(&ring->lock, flags);
	if (ring->disconnected) {
		result = -ENODEV;
		goto unlock_state;
	}
	if (ring->started) {
		result = -EBUSY;
		goto unlock_state;
	}
	ring->stopping = false;
	ring->fatal_error = 0;
	ring->next_claim = 0;
	ring->next_release = 0;
	ring->next_submit = ring->slot_count;
	for (index = 0; index < ring->slot_count; index++) {
		ring->slots[index].sequence = index;
		ring->slots[index].length = 0;
		ring->slots[index].status = 0;
		ring->slots[index].state = RTLAMR_SLOT_INFLIGHT;
	}
	ring->started = true;
	spin_unlock_irqrestore(&ring->lock, flags);

	for (index = 0; index < ring->slot_count; index++) {
		result = rtlamr_submit_slot(ring, &ring->slots[index]);
		if (result)
			break;
	}
	if (result) {
		spin_lock_irqsave(&ring->lock, flags);
		ring->fatal_error = result;
		ring->stopping = true;
		spin_unlock_irqrestore(&ring->lock, flags);
		usb_kill_anchored_urbs(&ring->submitted);
		wake_up_interruptible(&ring->wait);
	}
	mutex_unlock(&ring->io_mutex);
	return result;

unlock_state:
	spin_unlock_irqrestore(&ring->lock, flags);
	mutex_unlock(&ring->io_mutex);
	return result;
}

static void rtlamr_ring_stop(struct rtlamr_usb_ring *ring)
{
	unsigned long flags;

	spin_lock_irqsave(&ring->lock, flags);
	if (!ring->started) {
		spin_unlock_irqrestore(&ring->lock, flags);
		return;
	}
	ring->stopping = true;
	spin_unlock_irqrestore(&ring->lock, flags);
	wake_up_interruptible(&ring->wait);
	usb_kill_anchored_urbs(&ring->submitted);

	spin_lock_irqsave(&ring->lock, flags);
	ring->started = false;
	spin_unlock_irqrestore(&ring->lock, flags);
	wake_up_interruptible(&ring->wait);
}

static int rtlamr_open(struct inode *inode, struct file *file)
{
	struct miscdevice *misc = file->private_data;
	struct rtlamr_usb_ring *ring = container_of(misc,
						    struct rtlamr_usb_ring, misc);
	unsigned long flags;
	int result = 0;

	if (atomic_cmpxchg(&ring->opened, 0, 1) != 0)
		return -EBUSY;
	spin_lock_irqsave(&ring->lock, flags);
	if (ring->disconnected)
		result = -ENODEV;
	else
		kref_get(&ring->refs);
	spin_unlock_irqrestore(&ring->lock, flags);
	if (result) {
		atomic_set(&ring->opened, 0);
		return result;
	}
	file->private_data = ring;
	return nonseekable_open(inode, file);
}

static int rtlamr_release_file(struct inode *inode, struct file *file)
{
	struct rtlamr_usb_ring *ring = file->private_data;

	rtlamr_ring_stop(ring);
	atomic_set(&ring->opened, 0);
	kref_put(&ring->refs, rtlamr_ring_free);
	return 0;
}

static ssize_t rtlamr_read(struct file *file, char __user *buffer, size_t length,
			   loff_t *offset)
{
	struct rtlamr_usb_ring *ring = file->private_data;
	struct rtlamr_usb_ring_completion completion;
	struct rtlamr_usb_slot *slot;
	unsigned long flags;
	int result;

	if (length < sizeof(completion))
		return -EINVAL;
	if (mutex_lock_interruptible(&ring->io_mutex))
		return -ERESTARTSYS;

	for (;;) {
		u32 index = ring->next_claim % ring->slot_count;

		result = wait_event_interruptible(ring->wait,
			READ_ONCE(ring->disconnected) || READ_ONCE(ring->stopping) ||
			READ_ONCE(ring->fatal_error) ||
			READ_ONCE(ring->slots[index].state) == RTLAMR_SLOT_READY);
		if (result)
			goto out;

		spin_lock_irqsave(&ring->lock, flags);
		if (ring->disconnected) {
			result = -ENODEV;
		} else if (ring->stopping) {
			result = -ECANCELED;
		} else {
			slot = &ring->slots[index];
			if (ring->fatal_error && slot->state != RTLAMR_SLOT_READY) {
				result = ring->fatal_error;
				spin_unlock_irqrestore(&ring->lock, flags);
				break;
			}
			if (slot->state != RTLAMR_SLOT_READY ||
			    slot->sequence != ring->next_claim) {
				spin_unlock_irqrestore(&ring->lock, flags);
				continue;
			}
			completion.sequence = slot->sequence;
			completion.slot = index;
			completion.length = slot->length;
			completion.status = slot->status;
			completion.reserved = 0;
			slot->state = RTLAMR_SLOT_CLAIMED;
			ring->next_claim++;
			result = 0;
		}
		spin_unlock_irqrestore(&ring->lock, flags);
		break;
	}
	if (!result && copy_to_user(buffer, &completion, sizeof(completion)))
		result = -EFAULT;
	if (!result)
		result = sizeof(completion);
out:
	mutex_unlock(&ring->io_mutex);
	return result;
}

static long rtlamr_release_slot(struct rtlamr_usb_ring *ring,
				unsigned long argument)
{
	struct rtlamr_usb_ring_release release;
	struct rtlamr_usb_slot *slot;
	unsigned long flags;
	u64 submit_sequence;
	int result = 0;

	if (copy_from_user(&release, (void __user *)argument, sizeof(release)))
		return -EFAULT;
	if (release.slot >= ring->slot_count)
		return -EINVAL;
	if (mutex_lock_interruptible(&ring->io_mutex))
		return -ERESTARTSYS;

	spin_lock_irqsave(&ring->lock, flags);
	slot = &ring->slots[release.slot];
	if (release.sequence != ring->next_release ||
	    slot->sequence != release.sequence ||
	    slot->state != RTLAMR_SLOT_CLAIMED) {
		result = -EINVAL;
		goto unlock;
	}
	ring->next_release++;
	atomic64_inc(&ring->releases);
	if (ring->stopping || ring->disconnected || ring->fatal_error) {
		slot->state = RTLAMR_SLOT_FREE;
		goto unlock;
	}
	submit_sequence = ring->next_submit++;
	slot->sequence = submit_sequence;
	slot->length = 0;
	slot->status = 0;
	slot->state = RTLAMR_SLOT_INFLIGHT;
	spin_unlock_irqrestore(&ring->lock, flags);

	result = rtlamr_submit_slot(ring, slot);
	if (result) {
		spin_lock_irqsave(&ring->lock, flags);
		slot->state = RTLAMR_SLOT_FREE;
		ring->fatal_error = result;
		spin_unlock_irqrestore(&ring->lock, flags);
		wake_up_interruptible(&ring->wait);
	}
	mutex_unlock(&ring->io_mutex);
	return result;

unlock:
	spin_unlock_irqrestore(&ring->lock, flags);
	mutex_unlock(&ring->io_mutex);
	return result;
}

static long rtlamr_ioctl(struct file *file, unsigned int command,
			 unsigned long argument)
{
	struct rtlamr_usb_ring *ring = file->private_data;
	struct rtlamr_usb_ring_info info;
	struct rtlamr_usb_ring_stats stats;

	switch (command) {
	case RTLAMR_USB_RING_IOC_INFO:
		info.abi = RTLAMR_USB_RING_ABI;
		info.slot_count = ring->slot_count;
		info.slot_bytes = ring->slot_bytes;
		info.endpoint = ring->endpoint;
		return copy_to_user((void __user *)argument, &info, sizeof(info)) ?
			-EFAULT : 0;
	case RTLAMR_USB_RING_IOC_START:
		return rtlamr_ring_start(ring);
	case RTLAMR_USB_RING_IOC_RELEASE:
		return rtlamr_release_slot(ring, argument);
	case RTLAMR_USB_RING_IOC_STOP:
		rtlamr_ring_stop(ring);
		return 0;
	case RTLAMR_USB_RING_IOC_STATS:
		stats.completions = atomic64_read(&ring->completions);
		stats.releases = atomic64_read(&ring->releases);
		stats.submit_errors = atomic64_read(&ring->submit_errors);
		stats.transfer_errors = atomic64_read(&ring->transfer_errors);
		stats.short_transfers = atomic64_read(&ring->short_transfers);
		return copy_to_user((void __user *)argument, &stats,
				    sizeof(stats)) ? -EFAULT : 0;
	default:
		return -ENOTTY;
	}
}

static void rtlamr_vma_open(struct vm_area_struct *vma)
{
	struct rtlamr_usb_ring *ring = vma->vm_private_data;

	kref_get(&ring->refs);
	__module_get(THIS_MODULE);
}

static void rtlamr_vma_close(struct vm_area_struct *vma)
{
	struct rtlamr_usb_ring *ring = vma->vm_private_data;

	kref_put(&ring->refs, rtlamr_ring_free);
	module_put(THIS_MODULE);
}

static const struct vm_operations_struct rtlamr_vm_ops = {
	.open = rtlamr_vma_open,
	.close = rtlamr_vma_close,
};

static int rtlamr_mmap(struct file *file, struct vm_area_struct *vma)
{
	struct rtlamr_usb_ring *ring = file->private_data;
	unsigned long size = vma->vm_end - vma->vm_start;
	unsigned long byte_offset = vma->vm_pgoff << PAGE_SHIFT;
	u32 index;
	int result;

	if (vma->vm_flags & VM_WRITE)
		return -EPERM;
	if (size != ring->slot_bytes || byte_offset % ring->slot_bytes)
		return -EINVAL;
	index = byte_offset / ring->slot_bytes;
	if (index >= ring->slot_count || READ_ONCE(ring->disconnected))
		return -ENODEV;

	vm_flags_set(vma, VM_DONTEXPAND | VM_DONTDUMP | VM_DONTCOPY);
	result = remap_pfn_range(vma, vma->vm_start,
				 virt_to_phys(ring->slots[index].data) >> PAGE_SHIFT,
				 ring->slot_bytes, vma->vm_page_prot);
	if (result)
		return result;
	vma->vm_ops = &rtlamr_vm_ops;
	vma->vm_private_data = ring;
	rtlamr_vma_open(vma);
	return 0;
}

static const struct file_operations rtlamr_fops = {
	.owner = THIS_MODULE,
	.open = rtlamr_open,
	.release = rtlamr_release_file,
	.read = rtlamr_read,
	.unlocked_ioctl = rtlamr_ioctl,
	.compat_ioctl = rtlamr_ioctl,
	.mmap = rtlamr_mmap,
};

static int rtlamr_probe(struct usb_interface *interface,
			const struct usb_device_id *id)
{
	struct usb_host_interface *setting = interface->cur_altsetting;
	struct usb_endpoint_descriptor *endpoint = NULL;
	struct rtlamr_usb_ring *ring;
	u32 index;
	int result;

	for (index = 0; index < setting->desc.bNumEndpoints; index++) {
		if (usb_endpoint_is_bulk_in(&setting->endpoint[index].desc)) {
			endpoint = &setting->endpoint[index].desc;
			break;
		}
	}
	if (!endpoint)
		return -ENODEV;
	if (ring_slots < 4 || ring_slots > RTLAMR_MAX_SLOTS ||
	    slot_bytes == 0 || !PAGE_ALIGNED(slot_bytes) || slot_bytes > INT_MAX)
		return -EINVAL;

	ring = kzalloc(sizeof(*ring), GFP_KERNEL);
	if (!ring)
		return -ENOMEM;
	ring->slots = kcalloc(ring_slots, sizeof(*ring->slots), GFP_KERNEL);
	if (!ring->slots) {
		kfree(ring);
		return -ENOMEM;
	}
	ring->udev = usb_get_dev(interface_to_usbdev(interface));
	ring->interface = interface;
	ring->endpoint = endpoint->bEndpointAddress;
	ring->slot_count = ring_slots;
	ring->slot_bytes = slot_bytes;
	init_waitqueue_head(&ring->wait);
	spin_lock_init(&ring->lock);
	mutex_init(&ring->io_mutex);
	kref_init(&ring->refs);
	atomic_set(&ring->opened, 0);
	init_usb_anchor(&ring->submitted);
	atomic64_set(&ring->completions, 0);
	atomic64_set(&ring->releases, 0);
	atomic64_set(&ring->submit_errors, 0);
	atomic64_set(&ring->transfer_errors, 0);
	atomic64_set(&ring->short_transfers, 0);

	for (index = 0; index < ring->slot_count; index++) {
		struct rtlamr_usb_slot *slot = &ring->slots[index];

		slot->ring = ring;
		slot->data = alloc_pages_exact(ring->slot_bytes,
					       GFP_KERNEL | __GFP_ZERO);
		if (!slot->data) {
			result = -ENOMEM;
			goto fail;
		}
		slot->urb = usb_alloc_urb(0, GFP_KERNEL);
		if (!slot->urb) {
			result = -ENOMEM;
			goto fail;
		}
		usb_fill_bulk_urb(slot->urb, ring->udev,
				  usb_rcvbulkpipe(ring->udev, ring->endpoint),
				  slot->data, ring->slot_bytes, rtlamr_complete,
				  slot);
	}

	ring->misc.minor = MISC_DYNAMIC_MINOR;
	ring->misc.name = "rtlamr_usb_ring0";
	ring->misc.fops = &rtlamr_fops;
	ring->misc.parent = &interface->dev;
	result = misc_register(&ring->misc);
	if (result)
		goto fail;
	usb_set_intfdata(interface, ring);
	dev_info(&interface->dev,
		 "RTLMAR USB ring ready: slots=%u bytes=%u endpoint=0x%02x\n",
		 ring->slot_count, ring->slot_bytes, ring->endpoint);
	return 0;

fail:
	kref_put(&ring->refs, rtlamr_ring_free);
	return result;
}

static void rtlamr_disconnect(struct usb_interface *interface)
{
	struct rtlamr_usb_ring *ring = usb_get_intfdata(interface);
	unsigned long flags;

	if (!ring)
		return;
	usb_set_intfdata(interface, NULL);
	spin_lock_irqsave(&ring->lock, flags);
	ring->disconnected = true;
	ring->stopping = true;
	spin_unlock_irqrestore(&ring->lock, flags);
	wake_up_interruptible(&ring->wait);
	usb_kill_anchored_urbs(&ring->submitted);
	misc_deregister(&ring->misc);
	kref_put(&ring->refs, rtlamr_ring_free);
}

static const struct usb_device_id rtlamr_ids[] = {
	{ USB_DEVICE(RTLAMR_USB_VENDOR, RTLAMR_USB_PRODUCT) },
	{ }
};
MODULE_DEVICE_TABLE(usb, rtlamr_ids);

static struct usb_driver rtlamr_usb_driver = {
	.name = "rtlamr_usb_ring",
	.probe = rtlamr_probe,
	.disconnect = rtlamr_disconnect,
	.id_table = rtlamr_ids,
	.supports_autosuspend = 0,
};

module_usb_driver(rtlamr_usb_driver);

MODULE_AUTHOR("RTLAMR contributors");
MODULE_DESCRIPTION("Persistent cacheable USB bulk ring for RTLMAR");
MODULE_LICENSE("GPL");
