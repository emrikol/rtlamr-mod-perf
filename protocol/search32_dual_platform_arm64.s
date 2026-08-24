//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

#include "textflag.h"

// After the fixed eight-symbol prefix, V4..V7 retain the immutable IDM
// identity tag and V0..V3 hold the disjoint union of IDM and R900 candidates.
// Each remaining row therefore filters one union mask. Go's assembler does
// not name AdvSIMD BIC; the raw encodings below subtract V8..V11 from V0..V3.
#define UNION_FILTER_00 \
	WORD $0x4e681c00 \
	WORD $0x4e691c21 \
	WORD $0x4e6a1c42 \
	WORD $0x4e6b1c63

#define UNION_FILTER_10 \
	VEOR V4.B16, V8.B16, V8.B16 \
	VEOR V5.B16, V9.B16, V9.B16 \
	VEOR V6.B16, V10.B16, V10.B16 \
	VEOR V7.B16, V11.B16, V11.B16 \
	UNION_FILTER_00

#define UNION_FILTER_01 \
	VEOR V4.B16, V8.B16, V8.B16 \
	VEOR V5.B16, V9.B16, V9.B16 \
	VEOR V6.B16, V10.B16, V10.B16 \
	VEOR V7.B16, V11.B16, V11.B16 \
	VAND V8.B16, V0.B16, V0.B16 \
	VAND V9.B16, V1.B16, V1.B16 \
	VAND V10.B16, V2.B16, V2.B16 \
	VAND V11.B16, V3.B16, V3.B16

#define UNION_FILTER_11 \
	VAND V8.B16, V0.B16, V0.B16 \
	VAND V9.B16, V1.B16, V1.B16 \
	VAND V10.B16, V2.B16, V2.B16 \
	VAND V11.B16, V3.B16, V3.B16

#define UNION_ROW_00 \
	VLD1.P (R7)(R3), [V8.B16, V9.B16, V10.B16, V11.B16] \
	UNION_FILTER_00

#define UNION_ROW_10 \
	VLD1.P (R7)(R3), [V8.B16, V9.B16, V10.B16, V11.B16] \
	UNION_FILTER_10

#define UNION_ROW_01 \
	VLD1.P (R7)(R3), [V8.B16, V9.B16, V10.B16, V11.B16] \
	UNION_FILTER_01

#define UNION_ROW_11 \
	VLD1.P (R7)(R3), [V8.B16, V9.B16, V10.B16, V11.B16] \
	UNION_FILTER_11

#define UNION_NONZERO_OR_BRANCH(ZERO_LABEL) \
	VORR V1.B16, V0.B16, V12.B16 \
	VORR V3.B16, V2.B16, V13.B16 \
	VORR V13.B16, V12.B16, V12.B16 \
	VMOV V12.D[0], R9 \
	VMOV V12.D[1], R10 \
	ORR R10, R9 \
	CBZ R9, ZERO_LABEL

#define DUAL_EXPAND_DWORD(SOURCE, OUTPUT, COUNT, BIT_LABEL, DONE_LABEL) \
	REV SOURCE, R13 \
	CBZ R13, DONE_LABEL \
BIT_LABEL: \
	CLZ R13, R14 \
	LSR R14, R19, R22 \
	ADD R14, R15, R16 \
	BIC R22, R13 \
	MOVD.P R16, 8(OUTPUT) \
	ADD $1, COUNT \
	CBNZ R13, BIT_LABEL \
DONE_LABEL: \
	ADD $64, R15

// searchAlignedCandidates32IDMR900A72 searches both fixed 32-symbol
// preambles in one traversal of each 64-byte candidate group. The first eight
// rows are loaded/extracted once. Remaining rows update both disjoint masks
// and stop as soon as neither protocol has a survivor. Inputs are the exact
// production geometry: stride 18, positive count divisible by 64.
TEXT ·searchAlignedCandidates32IDMR900A72(SB), NOSPLIT, $0-48
	MOVD packed+0(FP), R1
	MOVD idmIndices+8(FP), R8
	MOVD r900Indices+16(FP), R23
	MOVD count+24(FP), R5
	MOVD $18, R3
	MOVD ZR, R17
	MOVD ZR, R20
	MOVD ZR, R24
	MOVD R1, R12
	PCALIGN $16
search32_dual_quad_loop:
	VLD1.P 64(R12), [V16.B16, V17.B16, V18.B16, V19.B16]
	VLD1.P 64(R12), [V20.B16, V21.B16, V22.B16, V23.B16]
	VLD1.P 64(R12), [V24.B16, V25.B16, V26.B16, V27.B16]

	// IDM is 01010101 and R900 is 00000000. Both require zero at
	// positions 0/2/4/6; positions 1/3/5/7 must all agree. The agreed
	// odd bit is the immutable protocol identity tag.
	VEXT $2, V18.B16, V17.B16, V4.B16
	VEXT $2, V19.B16, V18.B16, V5.B16
	VEXT $2, V20.B16, V19.B16, V6.B16
	VEXT $2, V21.B16, V20.B16, V7.B16

	VEXT $4, V19.B16, V18.B16, V0.B16
	VEXT $4, V20.B16, V19.B16, V1.B16
	VEXT $4, V21.B16, V20.B16, V2.B16
	VEXT $4, V22.B16, V21.B16, V3.B16
	VEXT $6, V20.B16, V19.B16, V12.B16
	VEXT $6, V21.B16, V20.B16, V13.B16
	VEXT $6, V22.B16, V21.B16, V14.B16
	VEXT $6, V23.B16, V22.B16, V15.B16
	VORR V16.B16, V0.B16, V0.B16
	VORR V17.B16, V1.B16, V1.B16
	VORR V18.B16, V2.B16, V2.B16
	VORR V19.B16, V3.B16, V3.B16
	VEOR V4.B16, V12.B16, V12.B16
	VEOR V5.B16, V13.B16, V13.B16
	VEOR V6.B16, V14.B16, V14.B16
	VEOR V7.B16, V15.B16, V15.B16

	VEXT $8, V21.B16, V20.B16, V8.B16
	VEXT $8, V22.B16, V21.B16, V9.B16
	VEXT $8, V23.B16, V22.B16, V10.B16
	VEXT $8, V24.B16, V23.B16, V11.B16
	VEXT $10, V22.B16, V21.B16, V28.B16
	VEXT $10, V23.B16, V22.B16, V29.B16
	VEXT $10, V24.B16, V23.B16, V30.B16
	VEXT $10, V25.B16, V24.B16, V31.B16
	VORR V8.B16, V0.B16, V0.B16
	VORR V9.B16, V1.B16, V1.B16
	VORR V10.B16, V2.B16, V2.B16
	VORR V11.B16, V3.B16, V3.B16
	VEOR V4.B16, V28.B16, V28.B16
	VEOR V5.B16, V29.B16, V29.B16
	VEOR V6.B16, V30.B16, V30.B16
	VEOR V7.B16, V31.B16, V31.B16
	VORR V28.B16, V12.B16, V12.B16
	VORR V29.B16, V13.B16, V13.B16
	VORR V30.B16, V14.B16, V14.B16
	VORR V31.B16, V15.B16, V15.B16

	VEXT $12, V23.B16, V22.B16, V8.B16
	VEXT $12, V24.B16, V23.B16, V9.B16
	VEXT $12, V25.B16, V24.B16, V10.B16
	VEXT $12, V26.B16, V25.B16, V11.B16
	VEXT $14, V24.B16, V23.B16, V28.B16
	VEXT $14, V25.B16, V24.B16, V29.B16
	VEXT $14, V26.B16, V25.B16, V30.B16
	VEXT $14, V27.B16, V26.B16, V31.B16
	VORR V8.B16, V0.B16, V0.B16
	VORR V9.B16, V1.B16, V1.B16
	VORR V10.B16, V2.B16, V2.B16
	VORR V11.B16, V3.B16, V3.B16
	VEOR V4.B16, V28.B16, V28.B16
	VEOR V5.B16, V29.B16, V29.B16
	VEOR V6.B16, V30.B16, V30.B16
	VEOR V7.B16, V31.B16, V31.B16
	VORR V28.B16, V12.B16, V12.B16
	VORR V29.B16, V13.B16, V13.B16
	VORR V30.B16, V14.B16, V14.B16
	VORR V31.B16, V15.B16, V15.B16

	// A candidate belongs to either prefix exactly when every even bit is
	// zero and every odd bit agrees. Keep the agreed odd bit as the tag.
	VORR V12.B16, V0.B16, V0.B16
	VORR V13.B16, V1.B16, V1.B16
	VORR V14.B16, V2.B16, V2.B16
	VORR V15.B16, V3.B16, V3.B16
	// MVN V0..V3.16B: Go's ARM64 assembler has no AdvSIMD NOT mnemonic.
	WORD $0x6e205800
	WORD $0x6e205821
	WORD $0x6e205842
	WORD $0x6e205863

	SUB $48, R12, R7
	// Symbols 8..31. The suffix pair is IDM bit then R900 bit.
	UNION_ROW_00
	UNION_ROW_10
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_00
	UNION_ROW_10
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_00
	UNION_ROW_10
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_00
	UNION_ROW_10
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_01
	UNION_ROW_01
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_01
	UNION_ROW_10
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_00
	UNION_ROW_11
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_10
	UNION_ROW_01
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_10
	UNION_ROW_01
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_11
	UNION_ROW_00
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_00
	UNION_ROW_01
	UNION_NONZERO_OR_BRANCH(search32_dual_quad_next)
	UNION_ROW_10
	UNION_ROW_10

	// Split the surviving union back into the two exact protocol sets.
	VAND V4.B16, V0.B16, V8.B16
	VAND V5.B16, V1.B16, V9.B16
	VAND V6.B16, V2.B16, V10.B16
	VAND V7.B16, V3.B16, V11.B16
	VEOR V8.B16, V0.B16, V4.B16
	VEOR V9.B16, V1.B16, V5.B16
	VEOR V10.B16, V2.B16, V6.B16
	VEOR V11.B16, V3.B16, V7.B16

	// Expand IDM survivors in exact MSB-first sample order.
	MOVD R17, R15
	VMOV V8.D[0], R2
	VMOV V8.D[1], R4
	VMOV V9.D[0], R6
	VMOV V9.D[1], R7
	VMOV V10.D[0], R9
	VMOV V10.D[1], R10
	VMOV V11.D[0], R11
	VMOV V11.D[1], R21
	MOVD $0x8000000000000000, R19
	DUAL_EXPAND_DWORD(R2, R8, R20, search32_dual_idm_v0d0_bit, search32_dual_idm_v0d0_done)
	DUAL_EXPAND_DWORD(R4, R8, R20, search32_dual_idm_v0d1_bit, search32_dual_idm_v0d1_done)
	DUAL_EXPAND_DWORD(R6, R8, R20, search32_dual_idm_v1d0_bit, search32_dual_idm_v1d0_done)
	DUAL_EXPAND_DWORD(R7, R8, R20, search32_dual_idm_v1d1_bit, search32_dual_idm_v1d1_done)
	DUAL_EXPAND_DWORD(R9, R8, R20, search32_dual_idm_v2d0_bit, search32_dual_idm_v2d0_done)
	DUAL_EXPAND_DWORD(R10, R8, R20, search32_dual_idm_v2d1_bit, search32_dual_idm_v2d1_done)
	DUAL_EXPAND_DWORD(R11, R8, R20, search32_dual_idm_v3d0_bit, search32_dual_idm_v3d0_done)
	DUAL_EXPAND_DWORD(R21, R8, R20, search32_dual_idm_v3d1_bit, search32_dual_idm_v3d1_done)

	// Expand R900 survivors independently, retaining the same order.
	MOVD R17, R15
	VMOV V4.D[0], R2
	VMOV V4.D[1], R4
	VMOV V5.D[0], R6
	VMOV V5.D[1], R7
	VMOV V6.D[0], R9
	VMOV V6.D[1], R10
	VMOV V7.D[0], R11
	VMOV V7.D[1], R21
	DUAL_EXPAND_DWORD(R2, R23, R24, search32_dual_r900_v0d0_bit, search32_dual_r900_v0d0_done)
	DUAL_EXPAND_DWORD(R4, R23, R24, search32_dual_r900_v0d1_bit, search32_dual_r900_v0d1_done)
	DUAL_EXPAND_DWORD(R6, R23, R24, search32_dual_r900_v1d0_bit, search32_dual_r900_v1d0_done)
	DUAL_EXPAND_DWORD(R7, R23, R24, search32_dual_r900_v1d1_bit, search32_dual_r900_v1d1_done)
	DUAL_EXPAND_DWORD(R9, R23, R24, search32_dual_r900_v2d0_bit, search32_dual_r900_v2d0_done)
	DUAL_EXPAND_DWORD(R10, R23, R24, search32_dual_r900_v2d1_bit, search32_dual_r900_v2d1_done)
	DUAL_EXPAND_DWORD(R11, R23, R24, search32_dual_r900_v3d0_bit, search32_dual_r900_v3d0_done)
	DUAL_EXPAND_DWORD(R21, R23, R24, search32_dual_r900_v3d1_bit, search32_dual_r900_v3d1_done)

search32_dual_quad_next:
	SUB $128, R12
	ADD $512, R17
	SUB $64, R5
	CBNZ R5, search32_dual_quad_loop
	MOVD R20, idmN+32(FP)
	MOVD R24, r900N+40(FP)
	RET

