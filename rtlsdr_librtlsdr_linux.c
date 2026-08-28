//go:build linux && cgo && rtlsdr

#include "third_party/rtl-sdr/src/librtlsdr.c"

/* These two handoff helpers intentionally live in the parent project rather
 * than carrying a private rtl-sdr submodule commit. Including librtlsdr.c in
 * this translation unit gives the helpers access to its opaque device handle
 * while the pinned official source remains unchanged. */
int rtlsdr_attach_kernel_driver(rtlsdr_dev_t *dev)
{
	int result;

	if (!dev || !dev->devh)
		return -EINVAL;
	result = libusb_release_interface(dev->devh, 0);
	if (result < 0)
		return result;
	result = libusb_attach_kernel_driver(dev->devh, 0);
	if (result < 0) {
		int claim_result = libusb_claim_interface(dev->devh, 0);
		if (claim_result < 0)
			return claim_result;
	}
	return result;
}

int rtlsdr_detach_kernel_driver(rtlsdr_dev_t *dev)
{
	int result;

	if (!dev || !dev->devh)
		return -EINVAL;
	result = libusb_detach_kernel_driver(dev->devh, 0);
	if (result < 0 && result != LIBUSB_ERROR_NOT_FOUND)
		return result;
	result = libusb_claim_interface(dev->devh, 0);
	if (result == 0)
		dev->driver_active = 0;
	return result;
}
