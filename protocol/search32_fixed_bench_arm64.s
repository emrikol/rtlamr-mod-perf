//go:build search32bench && linux && arm64 && gc && !purego && !race
// +build search32bench,linux,arm64,gc,!purego,!race

#include "textflag.h"

// Isolated drivers keep the Go scheduler and benchmark loop outside the leaf.
// R25 is reserved for the repetition counter; all three leaves leave it
// untouched. The 56-byte callee argument/result area starts at 8(RSP).
#define SEARCH32_FIXED_DRIVER(NAME, LEAF, LOOP) \
TEXT NAME(SB), NOSPLIT, $64-56               \
	MOVD dst+0(FP), R0                         \
	MOVD packed+8(FP), R1                      \
	MOVD masks+16(FP), R2                      \
	MOVD indices+24(FP), R8                    \
	MOVD symLenByte+32(FP), R3                 \
	MOVD count+40(FP), R5                      \
	MOVD calls+48(FP), R25                     \
	STP (R0, R1), 8(RSP)                       \
	STP (R2, R8), 24(RSP)                      \
	STP (R3, R5), 40(RSP)                      \
LOOP:                                         \
	CALL LEAF(SB)                              \
	SUB $1, R25                                \
	CBNZ R25, LOOP                             \
	RET

SEARCH32_FIXED_DRIVER(·search32FixedRunCurrent, ·searchAlignedCandidates32A72, search32_fixed_run_current_loop)
SEARCH32_FIXED_DRIVER(·search32FixedRunIDM, ·searchAlignedCandidates32IDMA72, search32_fixed_run_idm_loop)
SEARCH32_FIXED_DRIVER(·search32FixedRunR900, ·searchAlignedCandidates32R900A72, search32_fixed_run_r900_loop)
