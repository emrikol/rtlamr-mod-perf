//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

#include "textflag.h"

#define MANCHESTER_INIT(OFFSET)                         \
	FMOVD OFFSET(R1), F2                             \
	FADDD F0, F2, F0                                 \
	FMOVD (576+OFFSET)(R1), F2                       \
	FADDD F1, F2, F1

// The balanced schedule holds one result's ready lower and upper deltas in
// F20/F21. It overlaps the following result's three loads and two independent
// delta subtractions with the current recurrence updates while preserving the
// literal scalar FSUBD/FADDD order.
#define MANCHESTER_BALANCED_PREPARE(OFFSET)               \
	FMOVD OFFSET(R1), F22                               \
	FMOVD (576+OFFSET)(R1), F23                         \
	FMOVD (1152+OFFSET)(R1), F24                        \
	FSUBD F22, F23, F20                                 \
	FSUBD F23, F24, F21

#define MANCHESTER_BALANCED_RESULT(NEXT_OFFSET, RESULT)   \
	FSUBD F1, F0, RESULT                               \
	FMOVD NEXT_OFFSET(R1), F22                         \
	FADDD F0, F20, F0                                  \
	FMOVD (576+NEXT_OFFSET)(R1), F23                   \
	FADDD F1, F21, F1                                  \
	FMOVD (1152+NEXT_OFFSET)(R1), F24                  \
	FSUBD F22, F23, F20                                \
	FSUBD F23, F24, F21

#define MANCHESTER_BALANCED_FINAL(RESULT)                 \
	FSUBD F1, F0, RESULT                               \
	FADDD F0, F20, F0                                  \
	FADDD F1, F21, F1

// filterManchesterA72TBXSSRABalanced implements the fixed production
// geometry: a 72-sample chip, 8,192 decisions, and exactly 8,336 input
// float64 values. Initialization and every rolling update preserve the Go
// recurrence's operand order, including signed-zero, infinity, and NaN
// behavior. TBL/TBX gathers the sign bytes of eight decision values; SSRA
// maps each sign bit to the exact byte expression 1-signbit(lower-upper).
//
// The startup delta and last block are peeled. The steady loop prepares only
// the first delta of the next legal result. Result 8,190 reaches the final
// input samples 8,191/8,263/8,335, and result 8,191 performs no following
// load, so neither input nor output is accessed outside its exact range.
TEXT ·filterManchesterA72TBXSSRABalanced(SB), NOSPLIT, $0-16
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	FMOVD ZR, F0
	FMOVD ZR, F1
	MOVD $72, R2

	PCALIGN $16
manchester_tbx_ssra_balanced_init:
	MANCHESTER_INIT(0)
	MANCHESTER_INIT(8)
	MANCHESTER_INIT(16)
	MANCHESTER_INIT(24)
	MANCHESTER_INIT(32)
	MANCHESTER_INIT(40)
	MANCHESTER_INIT(48)
	MANCHESTER_INIT(56)
	ADD $64, R1
	SUB $8, R2
	CBNZ R2, manchester_tbx_ssra_balanced_init

	SUB $576, R1
	MOVD $1, R14
	VDUP R14, V31.B16
	MOVD $0x4040404037271707, R3
	VDUP R3, V30.D2
	MOVD $0x3727170740404040, R4
	VDUP R4, V29.D2

	// Seed delta zero before its decision. The steady loop handles
	// results [0,8184), eight at a time.
	MANCHESTER_BALANCED_PREPARE(0)
	MOVD $8184, R2

	PCALIGN $16
manchester_tbx_ssra_balanced_loop:
	MANCHESTER_BALANCED_RESULT(8, F4)
	MANCHESTER_BALANCED_RESULT(16, F5)
	MANCHESTER_BALANCED_RESULT(24, F6)
	MANCHESTER_BALANCED_RESULT(32, F7)
	VTBL V30.B8, [V4.B16, V5.B16, V6.B16, V7.B16], V18.B8

	MANCHESTER_BALANCED_RESULT(40, F8)
	MANCHESTER_BALANCED_RESULT(48, F9)
	MANCHESTER_BALANCED_RESULT(56, F10)
	MANCHESTER_BALANCED_RESULT(64, F11)
	VTBX V29.B8, [V8.B16, V9.B16, V10.B16, V11.B16], V18.B8

	WORD $0x0f09165f // SSRA V31.8B, V18.8B, #7
	VST1 V31.D[0], (R0)
	VMOVI $1, V31.B16

	ADD $64, R1
	ADD $8, R0
	SUB $8, R2
	CBNZ R2, manchester_tbx_ssra_balanced_loop

	// Result 8,190 prepares the last legal delta from offsets 56, 632,
	// and 1,208 relative to input[8,184]. Result 8,191 consumes it without
	// preparing a nonexistent result 8,192.
	MANCHESTER_BALANCED_RESULT(8, F4)
	MANCHESTER_BALANCED_RESULT(16, F5)
	MANCHESTER_BALANCED_RESULT(24, F6)
	MANCHESTER_BALANCED_RESULT(32, F7)
	VTBL V30.B8, [V4.B16, V5.B16, V6.B16, V7.B16], V18.B8

	MANCHESTER_BALANCED_RESULT(40, F8)
	MANCHESTER_BALANCED_RESULT(48, F9)
	MANCHESTER_BALANCED_RESULT(56, F10)
	MANCHESTER_BALANCED_FINAL(F11)
	VTBX V29.B8, [V8.B16, V9.B16, V10.B16, V11.B16], V18.B8

	WORD $0x0f09165f // SSRA V31.8B, V18.8B, #7
	VST1 V31.D[0], (R0)
	VMOVI $1, V31.B16
	RET
