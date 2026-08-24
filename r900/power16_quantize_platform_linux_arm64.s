//go:build linux && arm64 && gc && !purego && !race

#include "textflag.h"

// quantizePower16L72A72 computes the fixed L=72 R900 correlations over one
// contiguous four-chip Power16 window and returns the exact symbol in [0,5].
// Each chip sum is at most 72*65025=4,681,800.  Each correlation is therefore
// in [-9,363,600,9,363,600], so every vector accumulator, scalar expression,
// negation, absolute value, and comparison is exact in int32.
//
// Nine groups cover each 72-element chip. UADALP widens adjacent uint16 pairs
// into four uint32 lanes and accumulates them. The raw words are needed because
// Go's ARM64 assembler does not name UADALP; linked disassembly is a mandatory
// gate. The four independent chains balance A72's one/cycle load and F1 UADALP
// throughput. VADDV then reduces each exact chip sum once.
TEXT ·quantizePower16L72A72(SB), NOSPLIT, $0-12
	MOVD signal+0(FP), R0
	ADD $144, R0, R1
	ADD $288, R0, R2
	ADD $432, R0, R3

	VEOR V4.B16, V4.B16, V4.B16
	VEOR V5.B16, V5.B16, V5.B16
	VEOR V6.B16, V6.B16, V6.B16
	VEOR V7.B16, V7.B16, V7.B16
	MOVD $9, R4

	PCALIGN $16
r900_power16_l72_sum:
	VLD1.P 16(R0), [V0.H8]
	VLD1.P 16(R1), [V1.H8]
	VLD1.P 16(R2), [V2.H8]
	VLD1.P 16(R3), [V3.H8]
	WORD $0x6e606804 // UADALP V0.H8, V4.S4
	WORD $0x6e606825 // UADALP V1.H8, V5.S4
	WORD $0x6e606846 // UADALP V2.H8, V6.S4
	WORD $0x6e606867 // UADALP V3.H8, V7.S4
	SUBS $1, R4, R4
	BNE r900_power16_l72_sum

	VADDV V4.S4, V4
	VADDV V5.S4, V5
	VADDV V6.S4, V6
	VADDV V7.S4, V7
	VMOV V4.S[0], R4
	VMOV V5.S[0], R5
	VMOV V6.S[0], R6
	VMOV V7.S[0], R7

	// v0=(a+b)-(c+d), v1=(a-b)+(c-d), v2=(a-b)-(c-d).
	ADDW R5, R4, R8
	SUBW R6, R8, R8
	SUBW R7, R8, R8
	SUBW R5, R4, R9
	ADDW R6, R9, R9
	SUBW R7, R9, R9
	SUBW R5, R4, R10
	SUBW R6, R10, R10
	ADDW R7, R10, R10

	// Preserve production's strict-greater v0, then v1, then v2 precedence.
	// Equal absolute values never replace the earlier candidate; selected==0
	// is non-positive, exactly as in quantizePower16Symbol.
	CMPW $0, R8
	CNEGW LT, R8, R11
	MOVW R8, R14
	MOVW ZR, R15

	CMPW $0, R9
	CNEGW LT, R9, R12
	CMPW R11, R12
	BLE r900_power16_l72_keep_v0
	MOVW R12, R11
	MOVW R9, R14
	MOVW $1, R15
r900_power16_l72_keep_v0:

	CMPW $0, R10
	CNEGW LT, R10, R12
	CMPW R11, R12
	BLE r900_power16_l72_selected
	MOVW R10, R14
	MOVW $2, R15
r900_power16_l72_selected:
	CMPW $0, R14
	BLE r900_power16_l72_return
	ADDW $3, R15, R15
r900_power16_l72_return:
	MOVW R15, ret+8(FP)
	RET
