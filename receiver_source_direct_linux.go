//go:build linux && cgo && rtlsdr

package main

/*
#cgo pkg-config: libusb-1.0
#cgo CFLAGS: -I${SRCDIR}/third_party/rtl-sdr/include -I${SRCDIR}/third_party/rtl-sdr/src -DDETACH_KERNEL_DRIVER=1 -Drtlsdr_STATIC
#cgo LDFLAGS: -lpthread

#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include "third_party/rtl-sdr/include/rtl-sdr.h"

#ifndef RTLAMR_DIRECT_RING_COUNT
#define RTLAMR_DIRECT_RING_COUNT 4
#endif

typedef struct rtlamr_direct_slot {
	unsigned char *data;
	uint32_t length;
	int state;
} rtlamr_direct_slot;

enum {
	RTLAMR_SLOT_FREE = 0,
	RTLAMR_SLOT_FILLING = 1,
	RTLAMR_SLOT_READY = 2,
	RTLAMR_SLOT_CLAIMED = 3,
};

typedef struct rtlamr_direct {
	rtlsdr_dev_t *dev;
	pthread_t thread;
	pthread_mutex_t mutex;
	pthread_cond_t condition;
	rtlamr_direct_slot *slots;
	uint32_t producer;
	uint32_t consumer;
	int stopping;
	int started;
	int read_done;
	int read_result;
	uint32_t buffer_count;
	uint32_t buffer_length;
} rtlamr_direct;

static void *rtlamr_direct_read_thread(void *opaque) {
	rtlamr_direct *source = (rtlamr_direct *)opaque;
	for (;;) {
		pthread_mutex_lock(&source->mutex);
		rtlamr_direct_slot *slot = &source->slots[source->producer];
		while (!source->stopping && slot->state != RTLAMR_SLOT_FREE) {
			pthread_cond_wait(&source->condition, &source->mutex);
			slot = &source->slots[source->producer];
		}
		if (source->stopping) {
			pthread_mutex_unlock(&source->mutex);
			break;
		}
		slot->state = RTLAMR_SLOT_FILLING;
		pthread_mutex_unlock(&source->mutex);

		int length = 0;
		int result = rtlsdr_read_sync(source->dev, slot->data,
				(int)source->buffer_length, &length);

		pthread_mutex_lock(&source->mutex);
		if (source->stopping) {
			slot->state = RTLAMR_SLOT_FREE;
			pthread_mutex_unlock(&source->mutex);
			break;
		}
		if (result < 0 || length <= 0) {
			slot->state = RTLAMR_SLOT_FREE;
			source->read_result = result < 0 ? result : -EIO;
			source->read_done = 1;
			pthread_cond_broadcast(&source->condition);
			pthread_mutex_unlock(&source->mutex);
			return NULL;
		}
		slot->length = (uint32_t)length;
		slot->state = RTLAMR_SLOT_READY;
		source->producer = (source->producer + 1) % source->buffer_count;
		pthread_cond_broadcast(&source->condition);
		pthread_mutex_unlock(&source->mutex);
	}

	pthread_mutex_lock(&source->mutex);
	source->read_done = 1;
	pthread_cond_broadcast(&source->condition);
	pthread_mutex_unlock(&source->mutex);
	return NULL;
}

static int rtlamr_direct_open(uint32_t index, rtlamr_direct **output) {
	rtlamr_direct *source = (rtlamr_direct *)calloc(1, sizeof(*source));
	if (source == NULL) {
		return -ENOMEM;
	}
	int result = pthread_mutex_init(&source->mutex, NULL);
	if (result != 0) {
		free(source);
		return -result;
	}
	result = pthread_cond_init(&source->condition, NULL);
	if (result != 0) {
		pthread_mutex_destroy(&source->mutex);
		free(source);
		return -result;
	}
	result = rtlsdr_open(&source->dev, index);
	if (result < 0) {
		pthread_cond_destroy(&source->condition);
		pthread_mutex_destroy(&source->mutex);
		free(source);
		return result;
	}
	*output = source;
	return 0;
}

static int rtlamr_direct_start(rtlamr_direct *source, uint32_t buffer_count, uint32_t buffer_length) {
	if (source == NULL || source->started || buffer_count < 2 || buffer_length == 0) {
		return -EINVAL;
	}
	int result = rtlsdr_reset_buffer(source->dev);
	if (result < 0) {
		return result;
	}
	source->buffer_count = buffer_count;
	source->buffer_length = buffer_length;
	source->slots = (rtlamr_direct_slot *)calloc(buffer_count, sizeof(*source->slots));
	if (source->slots == NULL) {
		return -ENOMEM;
	}
	for (uint32_t index = 0; index < buffer_count; index++) {
		if (posix_memalign((void **)&source->slots[index].data, 64, buffer_length) != 0) {
			for (uint32_t prior = 0; prior < index; prior++) {
				free(source->slots[prior].data);
			}
			free(source->slots);
			source->slots = NULL;
			return -ENOMEM;
		}
	}
	result = pthread_create(&source->thread, NULL, rtlamr_direct_read_thread, source);
	if (result != 0) {
		for (uint32_t index = 0; index < buffer_count; index++) {
			free(source->slots[index].data);
		}
		free(source->slots);
		source->slots = NULL;
		return -result;
	}
	source->started = 1;
	return 0;
}

static int rtlamr_direct_next(rtlamr_direct *source, unsigned char **data, uint32_t *length) {
	if (source == NULL) {
		return -EINVAL;
	}
	pthread_mutex_lock(&source->mutex);
	rtlamr_direct_slot *slot = &source->slots[source->consumer];
	while (slot->state != RTLAMR_SLOT_READY && !source->read_done && !source->stopping) {
		pthread_cond_wait(&source->condition, &source->mutex);
		slot = &source->slots[source->consumer];
	}
	if (slot->state == RTLAMR_SLOT_READY) {
		slot->state = RTLAMR_SLOT_CLAIMED;
		*data = slot->data;
		*length = slot->length;
		pthread_mutex_unlock(&source->mutex);
		return 0;
	}
	int result = source->read_result;
	if (source->stopping || result == 0) {
		result = -ECANCELED;
	}
	pthread_mutex_unlock(&source->mutex);
	return result;
}

static int rtlamr_direct_release(rtlamr_direct *source) {
	if (source == NULL) {
		return -EINVAL;
	}
	pthread_mutex_lock(&source->mutex);
	rtlamr_direct_slot *slot = &source->slots[source->consumer];
	if (slot->state != RTLAMR_SLOT_CLAIMED) {
		pthread_mutex_unlock(&source->mutex);
		return -EINVAL;
	}
	slot->length = 0;
	slot->state = RTLAMR_SLOT_FREE;
	source->consumer = (source->consumer + 1) % source->buffer_count;
	pthread_cond_broadcast(&source->condition);
	pthread_mutex_unlock(&source->mutex);
	return 0;
}

static int rtlamr_direct_cancel(rtlamr_direct *source) {
	if (source == NULL) {
		return 0;
	}
	pthread_mutex_lock(&source->mutex);
	source->stopping = 1;
	pthread_cond_broadcast(&source->condition);
	pthread_mutex_unlock(&source->mutex);
	return 0;
}

static int rtlamr_direct_close(rtlamr_direct *source) {
	if (source == NULL) {
		return 0;
	}
	rtlamr_direct_cancel(source);
	if (source->started) {
		pthread_join(source->thread, NULL);
		source->started = 0;
	}
	int result = rtlsdr_close(source->dev);
	for (uint32_t index = 0; index < source->buffer_count; index++) {
		free(source->slots[index].data);
	}
	free(source->slots);
	pthread_cond_destroy(&source->condition);
	pthread_mutex_destroy(&source->mutex);
	free(source);
	return result;
}

static int rtlamr_direct_set_gain_by_index(rtlamr_direct *source, uint32_t index) {
	int count = rtlsdr_get_tuner_gains(source->dev, NULL);
	if (count < 0) {
		return count;
	}
	if (index >= (uint32_t)count) {
		return -EINVAL;
	}
	int *gains = (int *)malloc(sizeof(int) * (size_t)count);
	if (gains == NULL) {
		return -ENOMEM;
	}
	int result = rtlsdr_get_tuner_gains(source->dev, gains);
	if (result >= 0) {
		result = rtlsdr_set_tuner_gain(source->dev, gains[index]);
	}
	free(gains);
	return result;
}

static int rtlamr_direct_device_count(void) { return (int)rtlsdr_get_device_count(); }
static uint32_t rtlamr_direct_ring_count(void) { return RTLAMR_DIRECT_RING_COUNT; }
static int rtlamr_direct_index_by_serial(const char *serial) { return rtlsdr_get_index_by_serial(serial); }
static const char *rtlamr_direct_device_name(uint32_t index) { return rtlsdr_get_device_name(index); }
static int rtlamr_direct_tuner_type(rtlamr_direct *source) { return (int)rtlsdr_get_tuner_type(source->dev); }
static int rtlamr_direct_gain_count(rtlamr_direct *source) { return rtlsdr_get_tuner_gains(source->dev, NULL); }
static int rtlamr_direct_set_center_freq(rtlamr_direct *source, uint32_t value) { return rtlsdr_set_center_freq(source->dev, value); }
static int rtlamr_direct_set_sample_rate(rtlamr_direct *source, uint32_t value) { return rtlsdr_set_sample_rate(source->dev, value); }
static int rtlamr_direct_set_tuner_gain_mode(rtlamr_direct *source, int value) { return rtlsdr_set_tuner_gain_mode(source->dev, value); }
static int rtlamr_direct_set_tuner_gain(rtlamr_direct *source, int value) { return rtlsdr_set_tuner_gain(source->dev, value); }
static int rtlamr_direct_set_freq_correction(rtlamr_direct *source, int value) { return rtlsdr_set_freq_correction(source->dev, value); }
static int rtlamr_direct_set_test_mode(rtlamr_direct *source, int value) { return rtlsdr_set_testmode(source->dev, value); }
static int rtlamr_direct_set_agc_mode(rtlamr_direct *source, int value) { return rtlsdr_set_agc_mode(source->dev, value); }
static int rtlamr_direct_set_direct_sampling(rtlamr_direct *source, int value) { return rtlsdr_set_direct_sampling(source->dev, value); }
static int rtlamr_direct_set_offset_tuning(rtlamr_direct *source, int value) { return rtlsdr_set_offset_tuning(source->dev, value); }
static int rtlamr_direct_set_xtal_freq(rtlamr_direct *source, uint32_t rtl, uint32_t tuner) { return rtlsdr_set_xtal_freq(source->dev, rtl, tuner); }
*/
import "C"

import (
	"fmt"
	"strconv"
	"sync"
	"unsafe"
)

type directRTLSource struct {
	state       *C.rtlamr_direct
	blockBytes  int
	batchActive bool
	deviceName  string
	cancelOnce  sync.Once
	closeOnce   sync.Once
	cancelErr   error
	closeErr    error
}

func directRTLSourceAvailable() bool { return true }

func directRTLError(operation string, result C.int) error {
	if result >= 0 {
		return nil
	}
	return fmt.Errorf("%s failed: librtlsdr code %d", operation, int(result))
}

func directRTLBool(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func directRTLDeviceIndex(device string) (uint32, error) {
	if index, err := strconv.ParseUint(device, 10, 32); err == nil {
		if index >= uint64(C.rtlamr_direct_device_count()) {
			return 0, fmt.Errorf("RTL-SDR device index %d is not present", index)
		}
		return uint32(index), nil
	}
	serial := C.CString(device)
	defer C.free(unsafe.Pointer(serial))
	index := C.rtlamr_direct_index_by_serial(serial)
	if index < 0 {
		return 0, fmt.Errorf("RTL-SDR device serial %q was not found", device)
	}
	return uint32(index), nil
}

func newDirectRTLSource(config directRTLConfig) (receiverSource, uint32, uint32, error) {
	if config.BlockBytes == 0 || config.BatchBytes == 0 || config.BatchBytes%config.BlockBytes != 0 || config.BatchBytes%512 != 0 {
		return nil, 0, 0, fmt.Errorf("direct RTL-SDR batch bytes must be a positive multiple of 512")
	}
	index, err := directRTLDeviceIndex(config.Device)
	if err != nil {
		return nil, 0, 0, err
	}
	var state *C.rtlamr_direct
	if result := C.rtlamr_direct_open(C.uint32_t(index), &state); result < 0 {
		return nil, 0, 0, directRTLError("open RTL-SDR device", result)
	}
	source := &directRTLSource{
		state:      state,
		blockBytes: int(config.BlockBytes),
	}
	if name := C.rtlamr_direct_device_name(C.uint32_t(index)); name != nil {
		source.deviceName = C.GoString(name)
	}
	fail := func(operation string, result C.int) (receiverSource, uint32, uint32, error) {
		_ = source.Close()
		return nil, 0, 0, directRTLError(operation, result)
	}

	if config.RTLXtalFreqSet || config.TunerXtalFreqSet {
		if result := C.rtlamr_direct_set_xtal_freq(state, C.uint32_t(config.RTLXtalFreq), C.uint32_t(config.TunerXtalFreq)); result < 0 {
			return fail("set RTL-SDR crystal frequencies", result)
		}
	}
	if config.FreqCorrectionSet {
		if result := C.rtlamr_direct_set_freq_correction(state, C.int(config.FreqCorrectionPPM)); result < 0 {
			return fail("set RTL-SDR frequency correction", result)
		}
	}
	if result := C.rtlamr_direct_set_sample_rate(state, C.uint32_t(config.SampleRate)); result < 0 {
		return fail("set RTL-SDR sample rate", result)
	}
	if result := C.rtlamr_direct_set_center_freq(state, C.uint32_t(config.CenterFreq)); result < 0 {
		return fail("set RTL-SDR center frequency", result)
	}
	if config.TunerGainModeSet {
		// rtltcp's flag is phrased as tuner AGC: true sends manual=0.
		if result := C.rtlamr_direct_set_tuner_gain_mode(state, directRTLBool(!config.TunerGainMode)); result < 0 {
			return fail("set RTL-SDR tuner gain mode", result)
		}
	}
	if config.TunerGainSet {
		if result := C.rtlamr_direct_set_tuner_gain(state, C.int(config.TunerGainTenthsDB)); result < 0 {
			return fail("set RTL-SDR tuner gain", result)
		}
	}
	if config.GainByIndexSet {
		if result := C.rtlamr_direct_set_gain_by_index(state, C.uint32_t(config.GainByIndex)); result < 0 {
			return fail("set RTL-SDR tuner gain by index", result)
		}
	}
	if config.TestModeSet {
		if result := C.rtlamr_direct_set_test_mode(state, directRTLBool(config.TestMode)); result < 0 {
			return fail("set RTL-SDR test mode", result)
		}
	}
	if config.AGCModeSet {
		if result := C.rtlamr_direct_set_agc_mode(state, directRTLBool(config.AGCMode)); result < 0 {
			return fail("set RTL-SDR AGC mode", result)
		}
	}
	if config.DirectSamplingSet {
		if result := C.rtlamr_direct_set_direct_sampling(state, directRTLBool(config.DirectSampling)); result < 0 {
			return fail("set RTL-SDR direct sampling", result)
		}
	}
	if config.OffsetTuningSet {
		if result := C.rtlamr_direct_set_offset_tuning(state, directRTLBool(config.OffsetTuning)); result < 0 {
			return fail("set RTL-SDR offset tuning", result)
		}
	}
	tunerType := C.rtlamr_direct_tuner_type(state)
	gainCount := C.rtlamr_direct_gain_count(state)
	if tunerType < 0 || gainCount < 0 {
		_ = source.Close()
		return nil, 0, 0, fmt.Errorf("query RTL-SDR tuner identity failed")
	}
	if result := C.rtlamr_direct_start(state, C.rtlamr_direct_ring_count(), C.uint32_t(config.BatchBytes)); result < 0 {
		return fail("start RTL-SDR asynchronous reader", result)
	}
	return source, uint32(tunerType), uint32(gainCount), nil
}

func (source *directRTLSource) Name() string {
	if source.deviceName == "" {
		return "rtlsdr-direct"
	}
	return "rtlsdr-direct:" + source.deviceName
}

func (source *directRTLSource) Next() ([]byte, error) {
	if source.batchActive {
		return nil, fmt.Errorf("direct RTL-SDR batch was not released")
	}
	var data *C.uchar
	var length C.uint32_t
	result := C.rtlamr_direct_next(source.state, &data, &length)
	if result < 0 {
		return nil, directRTLError("read RTL-SDR batch", result)
	}
	batchBytes := int(length)
	if data == nil || batchBytes == 0 || batchBytes%source.blockBytes != 0 {
		_ = C.rtlamr_direct_release(source.state)
		return nil, fmt.Errorf("invalid RTL-SDR batch length %d", uint32(length))
	}
	source.batchActive = true
	return (*[1 << 30]byte)(unsafe.Pointer(data))[:batchBytes:batchBytes], nil
}

func (source *directRTLSource) Release() error {
	if !source.batchActive {
		return fmt.Errorf("no active direct RTL-SDR batch")
	}
	if result := C.rtlamr_direct_release(source.state); result < 0 {
		return directRTLError("release RTL-SDR ring batch", result)
	}
	source.batchActive = false
	return nil
}

func (source *directRTLSource) Cancel() error {
	source.cancelOnce.Do(func() {
		source.cancelErr = directRTLError("cancel RTL-SDR reader", C.rtlamr_direct_cancel(source.state))
	})
	return source.cancelErr
}

func (source *directRTLSource) Close() error {
	source.closeOnce.Do(func() {
		_ = source.Cancel()
		source.closeErr = directRTLError("close RTL-SDR device", C.rtlamr_direct_close(source.state))
	})
	return source.closeErr
}
