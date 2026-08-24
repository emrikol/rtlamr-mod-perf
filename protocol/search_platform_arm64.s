//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

#include "textflag.h"

#define SEARCH4_TAIL_ONE(OFFSET) \
	ADD $OFFSET, R1, R7           \
	VLD1 (R7), [V4.B16, V5.B16, V6.B16, V7.B16] \
	VAND V4.B16, V0.B16, V0.B16  \
	VAND V5.B16, V1.B16, V1.B16  \
	VAND V6.B16, V2.B16, V2.B16  \
	VAND V7.B16, V3.B16, V3.B16

#define SEARCH4_TAIL_ZERO(OFFSET) \
	ADD $OFFSET, R1, R7            \
	VLD1 (R7), [V4.B16, V5.B16, V6.B16, V7.B16] \
	WORD $0x4e641c00               \
	WORD $0x4e651c21               \
	WORD $0x4e661c42               \
	WORD $0x4e671c63


#define PREAMBLE_STEP(MASK, LANE) \
	VDUP MASK.B[LANE], V1.B16        \
	VLD1.P (R7)(R3), [V2.B16]        \
	VEOR V1.B16, V2.B16, V2.B16     \
	VAND V2.B16, V0.B16, V0.B16

// Fixed 32-symbol production preambles operate on four independent 16-byte
// candidate groups. Go's assembler does not name AdvSIMD BIC, so the raw
// encodings below are pinned by native exactness and linked-disassembly tests.
#define SEARCH32_QUAD_ONE \
	VLD1.P (R7)(R3), [V4.B16, V5.B16, V6.B16, V7.B16] \
	VAND V4.B16, V0.B16, V0.B16                       \
	VAND V5.B16, V1.B16, V1.B16                       \
	VAND V6.B16, V2.B16, V2.B16                       \
	VAND V7.B16, V3.B16, V3.B16

#define SEARCH32_QUAD_ZERO \
	VLD1.P (R7)(R3), [V4.B16, V5.B16, V6.B16, V7.B16] \
	WORD $0x4e641c00                                   \
	WORD $0x4e651c21                                   \
	WORD $0x4e661c42                                   \
	WORD $0x4e671c63

#define SEARCH32_QUAD_NONZERO_OR_BRANCH(ZERO_LABEL) \
	VORR V1.B16, V0.B16, V8.B16                 \
	VORR V3.B16, V2.B16, V9.B16                 \
	VORR V9.B16, V8.B16, V8.B16                 \
	VMOV V8.D[0], R9                             \
	VMOV V8.D[1], R10                            \
	ORR R10, R9                                  \
	CBZ R9, ZERO_LABEL

#define SEARCH32_EXPAND_DWORD(SOURCE, BIT_LABEL, DONE_LABEL) \
	REV SOURCE, R13                                            \
	CBZ R13, DONE_LABEL                                        \
BIT_LABEL:                                                   \
	CLZ R13, R14                                               \
	LSR R14, R19, R22                                          \
	ADD R14, R15, R16                                          \
	BIC R22, R13                                               \
	MOVD.P R16, 8(R8)                                          \
	ADD $1, R20                                                \
	CBNZ R13, BIT_LABEL                                        \
DONE_LABEL:                                                  \
	ADD $64, R15

#define SEARCH4_BALANCED_SINGLE \
	VLD1.P 16(R1), [V0.B16]       \
	VLD1.P 16(R5), [V1.B16]       \
	VEOR V16.B16, V0.B16, V0.B16 \
	VLD1.P 16(R6), [V2.B16]       \
	VEOR V17.B16, V1.B16, V1.B16 \
	VAND V1.B16, V0.B16, V0.B16  \
	VLD1.P 16(R7), [V3.B16]       \
	VEOR V18.B16, V2.B16, V2.B16 \
	VEOR V19.B16, V3.B16, V3.B16 \
	VAND V3.B16, V2.B16, V2.B16  \
	VAND V2.B16, V0.B16, V0.B16  \
	VST1.P [V0.B16], 16(R0)

#define SEARCH4_INTERLEAVE2 \
	VLD1.P 16(R1), [V0.B16]       \
	VLD1.P 16(R1), [V4.B16]       \
	VEOR V16.B16, V0.B16, V0.B16 \
	VEOR V16.B16, V4.B16, V4.B16 \
	VLD1.P 16(R5), [V1.B16]       \
	VLD1.P 16(R5), [V5.B16]       \
	VEOR V17.B16, V1.B16, V1.B16 \
	VAND V1.B16, V0.B16, V0.B16  \
	VEOR V17.B16, V5.B16, V5.B16 \
	VAND V5.B16, V4.B16, V4.B16  \
	VLD1.P 16(R6), [V2.B16]       \
	VLD1.P 16(R6), [V6.B16]       \
	VEOR V18.B16, V2.B16, V2.B16 \
	VEOR V18.B16, V6.B16, V6.B16 \
	VLD1.P 16(R7), [V3.B16]       \
	VLD1.P 16(R7), [V7.B16]       \
	VEOR V19.B16, V3.B16, V3.B16 \
	VAND V3.B16, V2.B16, V2.B16  \
	VEOR V19.B16, V7.B16, V7.B16 \
	VAND V7.B16, V6.B16, V6.B16  \
	VAND V2.B16, V0.B16, V0.B16  \
	VAND V6.B16, V4.B16, V4.B16  \
	VST1.P [V0.B16], 16(R0)      \
	VST1.P [V4.B16], 16(R0)

#define SEARCH4_ROLL18_PREP(C0, C1, C2, C3, C4, NEXT, S0, S1, S2, S3) \
	VLD1.P 16(R1), [NEXT.B16]                                      \
	VEOR V16.B16, C0.B16, S0.B16                                  \
	VEXT $2, C2.B16, C1.B16, S1.B16                               \
	VEOR V17.B16, S1.B16, S1.B16                                  \
	VEXT $4, C3.B16, C2.B16, S2.B16                               \
	VEOR V18.B16, S2.B16, S2.B16                                  \
	VEXT $6, C4.B16, C3.B16, S3.B16                               \
	VEOR V19.B16, S3.B16, S3.B16

#define SEARCH4_ROLL18_REDUCE(S0, S1, S2, S3) \
	VAND S1.B16, S0.B16, S0.B16               \
	VAND S3.B16, S2.B16, S2.B16               \
	VAND S2.B16, S0.B16, S0.B16

#define SEARCH4_ROLL18_QUAD(A0, A1, A2, A3, A4, NA, B0, B1, B2, B3, B4, NB, C0, C1, C2, C3, C4, NC, D0, D1, D2, D3, D4, ND) \
	SEARCH4_ROLL18_PREP(A0, A1, A2, A3, A4, NA, V0, V4, V8, V12)                                                                    \
	SEARCH4_ROLL18_PREP(B0, B1, B2, B3, B4, NB, V1, V5, V9, V13)                                                                    \
	SEARCH4_ROLL18_PREP(C0, C1, C2, C3, C4, NC, V2, V6, V10, V14)                                                                   \
	SEARCH4_ROLL18_PREP(D0, D1, D2, D3, D4, ND, V3, V7, V11, V15)                                                                   \
	SEARCH4_ROLL18_REDUCE(V0, V4, V8, V12)                                                                                           \
	SEARCH4_ROLL18_REDUCE(V1, V5, V9, V13)                                                                                           \
	SEARCH4_ROLL18_REDUCE(V2, V6, V10, V14)                                                                                          \
	SEARCH4_ROLL18_REDUCE(V3, V7, V11, V15)                                                                                          \
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R0)

#define SEARCH4_ROLL18_PAIR(A0, A1, A2, A3, A4, NA, B0, B1, B2, B3, B4, NB) \
	SEARCH4_ROLL18_PREP(A0, A1, A2, A3, A4, NA, V0, V1, V2, V3)            \
	SEARCH4_ROLL18_PREP(B0, B1, B2, B3, B4, NB, V4, V5, V6, V7)            \
	SEARCH4_ROLL18_REDUCE(V0, V1, V2, V3)                                   \
	SEARCH4_ROLL18_REDUCE(V4, V5, V6, V7)                                   \
	VST1.P [V0.B16], 16(R0)                                                 \
	VST1.P [V4.B16], 16(R0)

#define SEARCH4_ROLL18_FINAL(C0, C1, C2, C3, C4) \
	VEOR V16.B16, C0.B16, V0.B16                  \
	VEXT $2, C2.B16, C1.B16, V1.B16               \
	VEOR V17.B16, V1.B16, V1.B16                  \
	VEXT $4, C3.B16, C2.B16, V2.B16               \
	VEOR V18.B16, V2.B16, V2.B16                  \
	VEXT $6, C4.B16, C3.B16, V3.B16               \
	VEOR V19.B16, V3.B16, V3.B16                  \
	VAND V1.B16, V0.B16, V0.B16                   \
	VAND V3.B16, V2.B16, V2.B16                   \
	VAND V2.B16, V0.B16, V0.B16                   \
	VST1.P [V0.B16], 16(R0)

#define SEARCH4_MASK0_PREP(C1, C2, C3, C4, S1, S2, S3) \
	VEXT $2, C2.B16, C1.B16, S1.B16                    \
	VEXT $4, C3.B16, C2.B16, S2.B16                    \
	VEXT $6, C4.B16, C3.B16, S3.B16

#define SEARCH4_MASK0_REDUCE(C0, OUT, S1, S2, S3) \
	VAND S1.B16, C0.B16, OUT.B16                   \
	VAND S3.B16, S2.B16, S2.B16                    \
	VAND S2.B16, OUT.B16, OUT.B16

#define SEARCH4_MASK0_OFFSET_EVEN(OA, OB, OC, OD) \
	FMOVQ OA(R1), F25                               \
	SEARCH4_MASK0_PREP(V21, V22, V23, V24, V4, V8, V12) \
	FMOVQ OB(R1), F26                               \
	SEARCH4_MASK0_PREP(V22, V23, V24, V25, V5, V9, V13) \
	FMOVQ OC(R1), F27                               \
	SEARCH4_MASK0_PREP(V23, V24, V25, V26, V6, V10, V14) \
	SEARCH4_MASK0_REDUCE(V20, V0, V4, V8, V12)     \
	FMOVQ OD(R1), F20                               \
	SEARCH4_MASK0_PREP(V24, V25, V26, V27, V7, V11, V15) \
	SEARCH4_MASK0_REDUCE(V21, V1, V5, V9, V13)     \
	SEARCH4_MASK0_REDUCE(V22, V2, V6, V10, V14)    \
	SEARCH4_MASK0_REDUCE(V23, V3, V7, V11, V15)    \
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R0)

#define SEARCH4_MASK0_OFFSET_ODD(OA, OB, OC, OD) \
	FMOVQ OA(R1), F21                              \
	SEARCH4_MASK0_PREP(V25, V26, V27, V20, V4, V8, V12) \
	FMOVQ OB(R1), F22                              \
	SEARCH4_MASK0_PREP(V26, V27, V20, V21, V5, V9, V13) \
	FMOVQ OC(R1), F23                              \
	SEARCH4_MASK0_PREP(V27, V20, V21, V22, V6, V10, V14) \
	SEARCH4_MASK0_REDUCE(V24, V0, V4, V8, V12)    \
	FMOVQ OD(R1), F24                              \
	SEARCH4_MASK0_PREP(V20, V21, V22, V23, V7, V11, V15) \
	SEARCH4_MASK0_REDUCE(V25, V1, V5, V9, V13)    \
	SEARCH4_MASK0_REDUCE(V26, V2, V6, V10, V14)   \
	SEARCH4_MASK0_REDUCE(V27, V3, V7, V11, V15)   \
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R0)

// Go's ARM64 assembler does not name AdvSIMD BIC. These fixed encodings form
// (d & ~a) & ~(b | c) for four independent result vectors. Native oracle,
// guard-page, and linked-disassembly tests pin the encodings.
#define SEARCH4_MASK7_OFFSET_EVEN(OA, OB, OC, OD) \
	FMOVQ OA(R1), F25                               \
	SEARCH4_MASK0_PREP(V21, V22, V23, V24, V4, V8, V12) \
	FMOVQ OB(R1), F26                               \
	SEARCH4_MASK0_PREP(V22, V23, V24, V25, V5, V9, V13) \
	FMOVQ OC(R1), F27                               \
	SEARCH4_MASK0_PREP(V23, V24, V25, V26, V6, V10, V14) \
	VORR V8.B16, V4.B16, V0.B16                    \
	WORD $0x4e741d84                                \
	WORD $0x4e601c80                                \
	FMOVQ OD(R1), F20                               \
	SEARCH4_MASK0_PREP(V24, V25, V26, V27, V7, V11, V15) \
	VORR V9.B16, V5.B16, V1.B16                    \
	WORD $0x4e751da5                                \
	WORD $0x4e611ca1                                \
	VORR V10.B16, V6.B16, V2.B16                   \
	WORD $0x4e761dc6                                \
	WORD $0x4e621cc2                                \
	VORR V11.B16, V7.B16, V3.B16                   \
	WORD $0x4e771de7                                \
	WORD $0x4e631ce3                                \
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R0)

#define SEARCH4_MASK7_OFFSET_ODD(OA, OB, OC, OD) \
	FMOVQ OA(R1), F21                              \
	SEARCH4_MASK0_PREP(V25, V26, V27, V20, V4, V8, V12) \
	FMOVQ OB(R1), F22                              \
	SEARCH4_MASK0_PREP(V26, V27, V20, V21, V5, V9, V13) \
	FMOVQ OC(R1), F23                              \
	SEARCH4_MASK0_PREP(V27, V20, V21, V22, V6, V10, V14) \
	VORR V8.B16, V4.B16, V0.B16                   \
	WORD $0x4e781d84                               \
	WORD $0x4e601c80                               \
	FMOVQ OD(R1), F24                              \
	SEARCH4_MASK0_PREP(V20, V21, V22, V23, V7, V11, V15) \
	VORR V9.B16, V5.B16, V1.B16                   \
	WORD $0x4e791da5                               \
	WORD $0x4e611ca1                               \
	VORR V10.B16, V6.B16, V2.B16                  \
	WORD $0x4e7a1dc6                               \
	WORD $0x4e621cc2                               \
	VORR V11.B16, V7.B16, V3.B16                  \
	WORD $0x4e7b1de7                               \
	WORD $0x4e631ce3                               \
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R0)

// searchAlignedCandidates4A72 computes the first-four-symbol candidate mask
// for 16 packed signal bytes per iteration. The Go wrapper proves all lengths
// and gates this routine to a verified Cortex-A72 r0p3 implementation.
TEXT ·searchAlignedCandidates4A72(SB), NOSPLIT, $0-40
	MOVD dst+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD masks+16(FP), R2
	MOVD symLenByte+24(FP), R3
	MOVD count+32(FP), R4
	CMP $18, R3
	BNE search4_generic
	CMP $1024, R4
	BNE search4_generic
	MOVWU (R2), R10
	CBZ R10, search4_mask0
	MOVD $0x00ffffff, R11
	CMP R11, R10
	BEQ search4_mask7

	VLD1R.P 1(R2), [V16.B16]
	VLD1R.P 1(R2), [V17.B16]
	VLD1R.P 1(R2), [V18.B16]
	VLD1R (R2), [V19.B16]
	MOVD R1, R9
	VLD1.P 16(R1), [V20.B16]
	VLD1.P 16(R1), [V21.B16]
	VLD1.P 16(R1), [V22.B16]
	VLD1.P 16(R1), [V23.B16]
	VLD1.P 16(R1), [V24.B16]
	MOVD $7, R8

search4_rolling18_loop:
	SEARCH4_ROLL18_QUAD(V20, V21, V22, V23, V24, V25, V21, V22, V23, V24, V25, V26, V22, V23, V24, V25, V26, V27, V23, V24, V25, V26, V27, V20)
	SEARCH4_ROLL18_QUAD(V24, V25, V26, V27, V20, V21, V25, V26, V27, V20, V21, V22, V26, V27, V20, V21, V22, V23, V27, V20, V21, V22, V23, V24)
	SUB $1, R8
	CBNZ R8, search4_rolling18_loop

	SEARCH4_ROLL18_QUAD(V20, V21, V22, V23, V24, V25, V21, V22, V23, V24, V25, V26, V22, V23, V24, V25, V26, V27, V23, V24, V25, V26, V27, V20)
	SEARCH4_ROLL18_PAIR(V24, V25, V26, V27, V20, V21, V25, V26, V27, V20, V21, V22)
	SEARCH4_ROLL18_FINAL(V26, V27, V20, V21, V22)

	ADD $1008, R9, R1
	ADD $18, R1, R5
	ADD $18, R5, R6
	ADD $18, R6, R7
	SEARCH4_BALANCED_SINGLE
	RET

search4_generic:
	ADD R3, R1, R5
	ADD R3, R5, R6
	ADD R3, R6, R7
	VLD1R.P 1(R2), [V16.B16]
	VLD1R.P 1(R2), [V17.B16]
	VLD1R.P 1(R2), [V18.B16]
	VLD1R (R2), [V19.B16]
	AND $16, R4, R8
	CBZ R8, search4_generic_peel32
	SEARCH4_BALANCED_SINGLE
	SUB $16, R4
	CBZ R4, search4_done

search4_generic_peel32:
	AND $32, R4, R8
	CBZ R8, search4_generic_peel64
	SEARCH4_INTERLEAVE2
	SUB $32, R4
	CBZ R4, search4_done

search4_generic_peel64:
	AND $64, R4, R8
	CBZ R8, search4_generic_loop
	SEARCH4_INTERLEAVE2
	SEARCH4_INTERLEAVE2
	SUB $64, R4
	CBZ R4, search4_done

search4_generic_loop:
	SEARCH4_INTERLEAVE2
	SEARCH4_INTERLEAVE2
	SEARCH4_INTERLEAVE2
	SEARCH4_INTERLEAVE2
	SUB $128, R4
	CBNZ R4, search4_generic_loop

search4_done:
	RET

// Production SCM uses four expected-one symbols, represented by four zero
// inversion masks. Fixed-offset loads remove writeback dependencies while the
// stride-18 window reuses each packed byte. The last eight-byte load ends at
// the caller-proved buffer boundary.
search4_mask0:
	FMOVQ 0(R1), F20
	FMOVQ 16(R1), F21
	FMOVQ 32(R1), F22
	FMOVQ 48(R1), F23
	FMOVQ 64(R1), F24
	SEARCH4_MASK0_OFFSET_EVEN(80, 96, 112, 128)
	SEARCH4_MASK0_OFFSET_ODD(144, 160, 176, 192)
	SEARCH4_MASK0_OFFSET_EVEN(208, 224, 240, 256)
	SEARCH4_MASK0_OFFSET_ODD(272, 288, 304, 320)
	SEARCH4_MASK0_OFFSET_EVEN(336, 352, 368, 384)
	SEARCH4_MASK0_OFFSET_ODD(400, 416, 432, 448)
	SEARCH4_MASK0_OFFSET_EVEN(464, 480, 496, 512)
	SEARCH4_MASK0_OFFSET_ODD(528, 544, 560, 576)
	SEARCH4_MASK0_OFFSET_EVEN(592, 608, 624, 640)
	SEARCH4_MASK0_OFFSET_ODD(656, 672, 688, 704)
	SEARCH4_MASK0_OFFSET_EVEN(720, 736, 752, 768)
	SEARCH4_MASK0_OFFSET_ODD(784, 800, 816, 832)
	SEARCH4_MASK0_OFFSET_EVEN(848, 864, 880, 896)
	SEARCH4_MASK0_OFFSET_ODD(912, 928, 944, 960)
	SEARCH4_MASK0_OFFSET_EVEN(976, 992, 1008, 1024)

	FMOVQ 1040(R1), F21
	SEARCH4_MASK0_PREP(V25, V26, V27, V20, V4, V8, V12)
	FMOVQ 1056(R1), F22
	SEARCH4_MASK0_PREP(V26, V27, V20, V21, V5, V9, V13)
	SEARCH4_MASK0_REDUCE(V24, V0, V4, V8, V12)
	SEARCH4_MASK0_REDUCE(V25, V1, V5, V9, V13)
	VST1.P [V0.B16], 16(R0)
	VST1.P [V1.B16], 16(R0)
	SEARCH4_MASK0_PREP(V27, V20, V21, V22, V4, V8, V12)
	SEARCH4_MASK0_REDUCE(V26, V0, V4, V8, V12)
	VST1.P [V0.B16], 16(R0)

	ADD $1070, R1, R12
	VLD1 (R12), [V23.D1]
	VEXT $2, V23.B16, V23.B16, V23.B16
	SEARCH4_MASK0_PREP(V20, V21, V22, V23, V4, V8, V12)
	SEARCH4_MASK0_REDUCE(V27, V0, V4, V8, V12)
	VST1.P [V0.B16], 16(R0)
	RET

// Production SCM+ begins 0001, represented as ff,ff,ff,00 inversion masks.
// The balanced BIC form shortens four dependent logical operations to two
// levels while retaining the same fixed-load, exact-boundary geometry as SCM.
search4_mask7:
	FMOVQ 0(R1), F20
	FMOVQ 16(R1), F21
	FMOVQ 32(R1), F22
	FMOVQ 48(R1), F23
	FMOVQ 64(R1), F24
	SEARCH4_MASK7_OFFSET_EVEN(80, 96, 112, 128)
	SEARCH4_MASK7_OFFSET_ODD(144, 160, 176, 192)
	SEARCH4_MASK7_OFFSET_EVEN(208, 224, 240, 256)
	SEARCH4_MASK7_OFFSET_ODD(272, 288, 304, 320)
	SEARCH4_MASK7_OFFSET_EVEN(336, 352, 368, 384)
	SEARCH4_MASK7_OFFSET_ODD(400, 416, 432, 448)
	SEARCH4_MASK7_OFFSET_EVEN(464, 480, 496, 512)
	SEARCH4_MASK7_OFFSET_ODD(528, 544, 560, 576)
	SEARCH4_MASK7_OFFSET_EVEN(592, 608, 624, 640)
	SEARCH4_MASK7_OFFSET_ODD(656, 672, 688, 704)
	SEARCH4_MASK7_OFFSET_EVEN(720, 736, 752, 768)
	SEARCH4_MASK7_OFFSET_ODD(784, 800, 816, 832)
	SEARCH4_MASK7_OFFSET_EVEN(848, 864, 880, 896)
	SEARCH4_MASK7_OFFSET_ODD(912, 928, 944, 960)
	SEARCH4_MASK7_OFFSET_EVEN(976, 992, 1008, 1024)

	FMOVQ 1040(R1), F21
	SEARCH4_MASK0_PREP(V25, V26, V27, V20, V4, V8, V12)
	FMOVQ 1056(R1), F22
	SEARCH4_MASK0_PREP(V26, V27, V20, V21, V5, V9, V13)
	VORR V8.B16, V4.B16, V0.B16
	WORD $0x4e781d84
	WORD $0x4e601c80
	VORR V9.B16, V5.B16, V1.B16
	WORD $0x4e791da5
	WORD $0x4e611ca1
	VST1.P [V0.B16], 16(R0)
	VST1.P [V1.B16], 16(R0)
	SEARCH4_MASK0_PREP(V27, V20, V21, V22, V4, V8, V12)
	VORR V8.B16, V4.B16, V0.B16
	WORD $0x4e7a1d84
	WORD $0x4e601c80
	VST1.P [V0.B16], 16(R0)

	ADD $1070, R1, R12
	VLD1 (R12), [V23.D1]
	VEXT $2, V23.B16, V23.B16, V23.B16
	SEARCH4_MASK0_PREP(V20, V21, V22, V23, V4, V8, V12)
	VORR V8.B16, V4.B16, V0.B16
	WORD $0x4e7b1d84
	WORD $0x4e601c80
	VST1.P [V0.B16], 16(R0)
	RET


// searchAlignedCandidatesSCMTailA72 filters a precomputed first-four-symbol mask against the
// remaining fixed preamble and returns exact MSB-first sample indices.
TEXT ·searchAlignedCandidatesSCMTailA72(SB), NOSPLIT, $0-40
	MOVD masks+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD indices+16(FP), R8
	MOVD count+24(FP), R5
	MOVD ZR, R15
	MOVD ZR, R20
	MOVD $0x8000000000000000, R19
	PCALIGN $16
search4_scm_tail_loop:
	VLD1 (R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	SEARCH4_TAIL_ONE(72)
	SEARCH4_TAIL_ZERO(90)
	SEARCH4_TAIL_ZERO(108)
	SEARCH4_TAIL_ONE(126)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scm_tail_next)
	SEARCH4_TAIL_ZERO(144)
	SEARCH4_TAIL_ONE(162)
	SEARCH4_TAIL_ZERO(180)
	SEARCH4_TAIL_ONE(198)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scm_tail_next)
	SEARCH4_TAIL_ZERO(216)
	SEARCH4_TAIL_ZERO(234)
	SEARCH4_TAIL_ONE(252)
	SEARCH4_TAIL_ONE(270)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scm_tail_next)
	SEARCH4_TAIL_ZERO(288)
	SEARCH4_TAIL_ZERO(306)
	SEARCH4_TAIL_ZERO(324)
	SEARCH4_TAIL_ZERO(342)
	SEARCH4_TAIL_ZERO(360)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scm_tail_next)
	VMOV V0.D[0], R2
	VMOV V0.D[1], R4
	VMOV V1.D[0], R6
	VMOV V1.D[1], R7
	VMOV V2.D[0], R9
	VMOV V2.D[1], R10
	VMOV V3.D[0], R11
	VMOV V3.D[1], R21
	SEARCH32_EXPAND_DWORD(R2, search4_scm_tail_w0_bit, search4_scm_tail_w0_done)
	SEARCH32_EXPAND_DWORD(R4, search4_scm_tail_w1_bit, search4_scm_tail_w1_done)
	SEARCH32_EXPAND_DWORD(R6, search4_scm_tail_w2_bit, search4_scm_tail_w2_done)
	SEARCH32_EXPAND_DWORD(R7, search4_scm_tail_w3_bit, search4_scm_tail_w3_done)
	SEARCH32_EXPAND_DWORD(R9, search4_scm_tail_w4_bit, search4_scm_tail_w4_done)
	SEARCH32_EXPAND_DWORD(R10, search4_scm_tail_w5_bit, search4_scm_tail_w5_done)
	SEARCH32_EXPAND_DWORD(R11, search4_scm_tail_w6_bit, search4_scm_tail_w6_done)
	SEARCH32_EXPAND_DWORD(R21, search4_scm_tail_w7_bit, search4_scm_tail_w7_done)
	B search4_scm_tail_advance
search4_scm_tail_next:
	ADD $512, R15
search4_scm_tail_advance:
	ADD $64, R0
	ADD $64, R1
	SUB $64, R5
	CBNZ R5, search4_scm_tail_loop
	MOVD R20, ret+32(FP)
	RET

// searchAlignedCandidatesSCMPlusTailA72 filters a precomputed first-four-symbol mask against the
// remaining fixed preamble and returns exact MSB-first sample indices.
TEXT ·searchAlignedCandidatesSCMPlusTailA72(SB), NOSPLIT, $0-40
	MOVD masks+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD indices+16(FP), R8
	MOVD count+24(FP), R5
	MOVD ZR, R15
	MOVD ZR, R20
	MOVD $0x8000000000000000, R19
	PCALIGN $16
search4_scmplus_tail_loop:
	VLD1 (R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	SEARCH4_TAIL_ZERO(72)
	SEARCH4_TAIL_ONE(90)
	SEARCH4_TAIL_ONE(108)
	SEARCH4_TAIL_ZERO(126)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scmplus_tail_next)
	SEARCH4_TAIL_ONE(144)
	SEARCH4_TAIL_ZERO(162)
	SEARCH4_TAIL_ONE(180)
	SEARCH4_TAIL_ZERO(198)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scmplus_tail_next)
	SEARCH4_TAIL_ZERO(216)
	SEARCH4_TAIL_ZERO(234)
	SEARCH4_TAIL_ONE(252)
	SEARCH4_TAIL_ONE(270)
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search4_scmplus_tail_next)
	VMOV V0.D[0], R2
	VMOV V0.D[1], R4
	VMOV V1.D[0], R6
	VMOV V1.D[1], R7
	VMOV V2.D[0], R9
	VMOV V2.D[1], R10
	VMOV V3.D[0], R11
	VMOV V3.D[1], R21
	SEARCH32_EXPAND_DWORD(R2, search4_scmplus_tail_w0_bit, search4_scmplus_tail_w0_done)
	SEARCH32_EXPAND_DWORD(R4, search4_scmplus_tail_w1_bit, search4_scmplus_tail_w1_done)
	SEARCH32_EXPAND_DWORD(R6, search4_scmplus_tail_w2_bit, search4_scmplus_tail_w2_done)
	SEARCH32_EXPAND_DWORD(R7, search4_scmplus_tail_w3_bit, search4_scmplus_tail_w3_done)
	SEARCH32_EXPAND_DWORD(R9, search4_scmplus_tail_w4_bit, search4_scmplus_tail_w4_done)
	SEARCH32_EXPAND_DWORD(R10, search4_scmplus_tail_w5_bit, search4_scmplus_tail_w5_done)
	SEARCH32_EXPAND_DWORD(R11, search4_scmplus_tail_w6_bit, search4_scmplus_tail_w6_done)
	SEARCH32_EXPAND_DWORD(R21, search4_scmplus_tail_w7_bit, search4_scmplus_tail_w7_done)
	B search4_scmplus_tail_advance
search4_scmplus_tail_next:
	ADD $512, R15
search4_scmplus_tail_advance:
	ADD $64, R0
	ADD $64, R1
	SUB $64, R5
	CBNZ R5, search4_scmplus_tail_loop
	MOVD R20, ret+32(FP)
	RET

// searchAlignedCandidates32A72 checks an entire 32-symbol preamble for 16
// packed signal bytes per iteration and writes surviving sample indices in
// their existing MSB-first order. Masks are pre-expanded to 0x00/0xff by Go.
TEXT ·searchAlignedCandidates32A72(SB), NOSPLIT, $0-56
	MOVD dst+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD masks+16(FP), R6
	MOVD indices+24(FP), R8
	MOVD symLenByte+32(FP), R3
	MOVD count+40(FP), R5
	MOVD ZR, R17
	MOVD ZR, R20
	VLD1.P 16(R6), [V16.B16]
	VLD1 (R6), [V17.B16]

search32_loop:
	MOVD R1, R7
	VMOVI $255, V0.B16
	PREAMBLE_STEP(V16, 0)
	PREAMBLE_STEP(V16, 1)
	PREAMBLE_STEP(V16, 2)
	PREAMBLE_STEP(V16, 3)
	PREAMBLE_STEP(V16, 4)
	PREAMBLE_STEP(V16, 5)
	PREAMBLE_STEP(V16, 6)
	PREAMBLE_STEP(V16, 7)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_store_zero
	PREAMBLE_STEP(V16, 8)
	PREAMBLE_STEP(V16, 9)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_store_zero
	PREAMBLE_STEP(V16, 10)
	PREAMBLE_STEP(V16, 11)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_store_zero
	PREAMBLE_STEP(V16, 12)
	PREAMBLE_STEP(V16, 13)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_store_zero
	PREAMBLE_STEP(V16, 14)
	PREAMBLE_STEP(V16, 15)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_store_zero
	PREAMBLE_STEP(V17, 0)
	PREAMBLE_STEP(V17, 1)
	PREAMBLE_STEP(V17, 2)
	PREAMBLE_STEP(V17, 3)
	PREAMBLE_STEP(V17, 4)
	PREAMBLE_STEP(V17, 5)
	PREAMBLE_STEP(V17, 6)
	PREAMBLE_STEP(V17, 7)
	PREAMBLE_STEP(V17, 8)
	PREAMBLE_STEP(V17, 9)
	PREAMBLE_STEP(V17, 10)
	PREAMBLE_STEP(V17, 11)
	PREAMBLE_STEP(V17, 12)
	PREAMBLE_STEP(V17, 13)
	PREAMBLE_STEP(V17, 14)
	PREAMBLE_STEP(V17, 15)
	VST1 [V0.B16], (R0)
	VUADDLV V0.B16, V3
	VMOV V3.H[0], R9
	CBZ R9, search32_next

	MOVD R0, R10
	MOVD R17, R15
	MOVD $16, R11
search32_byte:
	MOVBU (R10), R13
	ADD $1, R10
	CBZ R13, search32_byte_next
search32_bit:
	CLZ R13, R14
	SUB $56, R14
	ADD R14, R15, R16
	MOVD R16, (R8)
	ADD $8, R8
	ADD $1, R20
	MOVD $128, R19
	LSR R14, R19
	BIC R19, R13
	CBNZ R13, search32_bit
search32_byte_next:
	ADD $8, R15
	SUB $1, R11
	CBNZ R11, search32_byte
	B search32_next

search32_store_zero:
	VST1 [V0.B16], (R0)

search32_next:
	ADD $16, R0
	ADD $16, R1
	ADD $128, R17
	SUB $16, R5
	CBNZ R5, search32_loop
	MOVD R20, ret+48(FP)
	RET
// Fixed production-preamble kernels. The Go wrapper requires stride 18 and a
// positive count divisible by 64. Empty groups deliberately skip scratch
// stores; ordered survivor indices are the only caller-visible output.
TEXT ·searchAlignedCandidates32IDMA72(SB), NOSPLIT, $0-56
	MOVD dst+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD indices+24(FP), R8
	MOVD symLenByte+32(FP), R3
	MOVD count+40(FP), R5
	MOVD ZR, R17
	MOVD ZR, R20
	PCALIGN $16
search32_idm_quad_loop:
	MOVD R1, R12
	VLD1.P 64(R12), [V16.B16, V17.B16, V18.B16, V19.B16]
	VLD1.P 64(R12), [V20.B16, V21.B16, V22.B16, V23.B16]
	VLD1.P 64(R12), [V24.B16, V25.B16, V26.B16, V27.B16]
	VEXT $2, V18.B16, V17.B16, V28.B16
	VEXT $2, V19.B16, V18.B16, V29.B16
	VEXT $2, V20.B16, V19.B16, V30.B16
	VEXT $2, V21.B16, V20.B16, V31.B16
	WORD $0x4e701f80
	WORD $0x4e711fa1
	WORD $0x4e721fc2
	WORD $0x4e731fe3
	VEXT $4, V19.B16, V18.B16, V4.B16
	VEXT $4, V20.B16, V19.B16, V5.B16
	VEXT $4, V21.B16, V20.B16, V6.B16
	VEXT $4, V22.B16, V21.B16, V7.B16
	VEXT $6, V20.B16, V19.B16, V28.B16
	VEXT $6, V21.B16, V20.B16, V29.B16
	VEXT $6, V22.B16, V21.B16, V30.B16
	VEXT $6, V23.B16, V22.B16, V31.B16
	WORD $0x4e641f84
	WORD $0x4e651fa5
	WORD $0x4e661fc6
	WORD $0x4e671fe7
	VEXT $8, V21.B16, V20.B16, V8.B16
	VEXT $8, V22.B16, V21.B16, V9.B16
	VEXT $8, V23.B16, V22.B16, V10.B16
	VEXT $8, V24.B16, V23.B16, V11.B16
	VEXT $10, V22.B16, V21.B16, V28.B16
	VEXT $10, V23.B16, V22.B16, V29.B16
	VEXT $10, V24.B16, V23.B16, V30.B16
	VEXT $10, V25.B16, V24.B16, V31.B16
	WORD $0x4e681f88
	WORD $0x4e691fa9
	WORD $0x4e6a1fca
	WORD $0x4e6b1feb
	VEXT $12, V23.B16, V22.B16, V12.B16
	VEXT $12, V24.B16, V23.B16, V13.B16
	VEXT $12, V25.B16, V24.B16, V14.B16
	VEXT $12, V26.B16, V25.B16, V15.B16
	VEXT $14, V24.B16, V23.B16, V28.B16
	VEXT $14, V25.B16, V24.B16, V29.B16
	VEXT $14, V26.B16, V25.B16, V30.B16
	VEXT $14, V27.B16, V26.B16, V31.B16
	WORD $0x4e6c1f8c
	WORD $0x4e6d1fad
	WORD $0x4e6e1fce
	WORD $0x4e6f1fef
	VAND V4.B16, V0.B16, V0.B16
	VAND V5.B16, V1.B16, V1.B16
	VAND V6.B16, V2.B16, V2.B16
	VAND V7.B16, V3.B16, V3.B16
	VAND V12.B16, V8.B16, V8.B16
	VAND V13.B16, V9.B16, V9.B16
	VAND V14.B16, V10.B16, V10.B16
	VAND V15.B16, V11.B16, V11.B16
	VAND V8.B16, V0.B16, V0.B16
	VAND V9.B16, V1.B16, V1.B16
	VAND V10.B16, V2.B16, V2.B16
	VAND V11.B16, V3.B16, V3.B16
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	VLD1.P 64(R12), [V16.B16, V17.B16, V18.B16, V19.B16]
	VLD1.P 64(R12), [V20.B16, V21.B16, V22.B16, V23.B16]
	WORD $0x4e791c00
	WORD $0x4e7a1c21
	WORD $0x4e7b1c42
	WORD $0x4e701c63
	VEXT $2, V27.B16, V26.B16, V4.B16
	VEXT $2, V16.B16, V27.B16, V5.B16
	VEXT $2, V17.B16, V16.B16, V6.B16
	VEXT $2, V18.B16, V17.B16, V7.B16
	VAND V4.B16, V0.B16, V0.B16
	VAND V5.B16, V1.B16, V1.B16
	VAND V6.B16, V2.B16, V2.B16
	VAND V7.B16, V3.B16, V3.B16
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	VEXT $4, V16.B16, V27.B16, V4.B16
	VEXT $4, V17.B16, V16.B16, V5.B16
	VEXT $4, V18.B16, V17.B16, V6.B16
	VEXT $4, V19.B16, V18.B16, V7.B16
	WORD $0x4e641c00
	WORD $0x4e651c21
	WORD $0x4e661c42
	WORD $0x4e671c63
	VEXT $6, V17.B16, V16.B16, V4.B16
	VEXT $6, V18.B16, V17.B16, V5.B16
	VEXT $6, V19.B16, V18.B16, V6.B16
	VEXT $6, V20.B16, V19.B16, V7.B16
	VAND V4.B16, V0.B16, V0.B16
	VAND V5.B16, V1.B16, V1.B16
	VAND V6.B16, V2.B16, V2.B16
	VAND V7.B16, V3.B16, V3.B16
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	VEXT $8, V18.B16, V17.B16, V4.B16
	VEXT $8, V19.B16, V18.B16, V5.B16
	VEXT $8, V20.B16, V19.B16, V6.B16
	VEXT $8, V21.B16, V20.B16, V7.B16
	WORD $0x4e641c00
	WORD $0x4e651c21
	WORD $0x4e661c42
	WORD $0x4e671c63
	VEXT $10, V19.B16, V18.B16, V4.B16
	VEXT $10, V20.B16, V19.B16, V5.B16
	VEXT $10, V21.B16, V20.B16, V6.B16
	VEXT $10, V22.B16, V21.B16, V7.B16
	VAND V4.B16, V0.B16, V0.B16
	VAND V5.B16, V1.B16, V1.B16
	VAND V6.B16, V2.B16, V2.B16
	VAND V7.B16, V3.B16, V3.B16
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	VEXT $12, V20.B16, V19.B16, V4.B16
	VEXT $12, V21.B16, V20.B16, V5.B16
	VEXT $12, V22.B16, V21.B16, V6.B16
	VEXT $12, V23.B16, V22.B16, V7.B16
	WORD $0x4e641c00
	WORD $0x4e651c21
	WORD $0x4e661c42
	WORD $0x4e671c63
	VLD1.P 16(R12), [V24.B16]
	VEXT $14, V21.B16, V20.B16, V4.B16
	VEXT $14, V22.B16, V21.B16, V5.B16
	VEXT $14, V23.B16, V22.B16, V6.B16
	VEXT $14, V24.B16, V23.B16, V7.B16
	VAND V4.B16, V0.B16, V0.B16
	VAND V5.B16, V1.B16, V1.B16
	VAND V6.B16, V2.B16, V2.B16
	VAND V7.B16, V3.B16, V3.B16
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	ADD $288, R1, R7
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_idm_quad_next)
	MOVD R17, R15
	VMOV V0.D[0], R2
	VMOV V0.D[1], R4
	VMOV V1.D[0], R6
	VMOV V1.D[1], R7
	VMOV V2.D[0], R9
	VMOV V2.D[1], R10
	VMOV V3.D[0], R11
	VMOV V3.D[1], R21
	MOVD $0x8000000000000000, R19
	SEARCH32_EXPAND_DWORD(R2, search32_idm_v0d0_bit, search32_idm_v0d0_done)
	SEARCH32_EXPAND_DWORD(R4, search32_idm_v0d1_bit, search32_idm_v0d1_done)
	SEARCH32_EXPAND_DWORD(R6, search32_idm_v1d0_bit, search32_idm_v1d0_done)
	SEARCH32_EXPAND_DWORD(R7, search32_idm_v1d1_bit, search32_idm_v1d1_done)
	SEARCH32_EXPAND_DWORD(R9, search32_idm_v2d0_bit, search32_idm_v2d0_done)
	SEARCH32_EXPAND_DWORD(R10, search32_idm_v2d1_bit, search32_idm_v2d1_done)
	SEARCH32_EXPAND_DWORD(R11, search32_idm_v3d0_bit, search32_idm_v3d0_done)
	SEARCH32_EXPAND_DWORD(R21, search32_idm_v3d1_bit, search32_idm_v3d1_done)
	B search32_idm_quad_next
search32_idm_quad_next:
	ADD $64, R0
	ADD $64, R1
	ADD $512, R17
	SUB $64, R5
	CBNZ R5, search32_idm_quad_loop
	MOVD R20, ret+48(FP)
	RET

TEXT ·searchAlignedCandidates32R900A72(SB), NOSPLIT, $0-56
	MOVD dst+0(FP), R0
	MOVD packed+8(FP), R1
	MOVD indices+24(FP), R8
	MOVD symLenByte+32(FP), R3
	MOVD count+40(FP), R5
	MOVD ZR, R17
	MOVD ZR, R20
	MOVD R1, R12
	PCALIGN $16
search32_r900_quad_loop:
	VLD1.P 64(R12), [V16.B16, V17.B16, V18.B16, V19.B16]
	VLD1.P 64(R12), [V20.B16, V21.B16, V22.B16, V23.B16]
	VLD1.P 64(R12), [V24.B16, V25.B16, V26.B16, V27.B16]
	VEXT $2, V18.B16, V17.B16, V28.B16
	VEXT $2, V19.B16, V18.B16, V29.B16
	VEXT $2, V20.B16, V19.B16, V30.B16
	VEXT $2, V21.B16, V20.B16, V31.B16
	VORR V28.B16, V16.B16, V0.B16
	VORR V29.B16, V17.B16, V1.B16
	VORR V30.B16, V18.B16, V2.B16
	VORR V31.B16, V19.B16, V3.B16
	VEXT $4, V19.B16, V18.B16, V4.B16
	VEXT $4, V20.B16, V19.B16, V5.B16
	VEXT $4, V21.B16, V20.B16, V6.B16
	VEXT $4, V22.B16, V21.B16, V7.B16
	VEXT $6, V20.B16, V19.B16, V28.B16
	VEXT $6, V21.B16, V20.B16, V29.B16
	VEXT $6, V22.B16, V21.B16, V30.B16
	VEXT $6, V23.B16, V22.B16, V31.B16
	VORR V28.B16, V4.B16, V4.B16
	VORR V29.B16, V5.B16, V5.B16
	VORR V30.B16, V6.B16, V6.B16
	VORR V31.B16, V7.B16, V7.B16
	VEXT $8, V21.B16, V20.B16, V8.B16
	VEXT $8, V22.B16, V21.B16, V9.B16
	VEXT $8, V23.B16, V22.B16, V10.B16
	VEXT $8, V24.B16, V23.B16, V11.B16
	VEXT $10, V22.B16, V21.B16, V28.B16
	VEXT $10, V23.B16, V22.B16, V29.B16
	VEXT $10, V24.B16, V23.B16, V30.B16
	VEXT $10, V25.B16, V24.B16, V31.B16
	VORR V28.B16, V8.B16, V8.B16
	VORR V29.B16, V9.B16, V9.B16
	VORR V30.B16, V10.B16, V10.B16
	VORR V31.B16, V11.B16, V11.B16
	VEXT $12, V23.B16, V22.B16, V12.B16
	VEXT $12, V24.B16, V23.B16, V13.B16
	VEXT $12, V25.B16, V24.B16, V14.B16
	VEXT $12, V26.B16, V25.B16, V15.B16
	VEXT $14, V24.B16, V23.B16, V28.B16
	VEXT $14, V25.B16, V24.B16, V29.B16
	VEXT $14, V26.B16, V25.B16, V30.B16
	VEXT $14, V27.B16, V26.B16, V31.B16
	VORR V28.B16, V12.B16, V12.B16
	VORR V29.B16, V13.B16, V13.B16
	VORR V30.B16, V14.B16, V14.B16
	VORR V31.B16, V15.B16, V15.B16
	VORR V4.B16, V0.B16, V0.B16
	VORR V5.B16, V1.B16, V1.B16
	VORR V6.B16, V2.B16, V2.B16
	VORR V7.B16, V3.B16, V3.B16
	VORR V12.B16, V8.B16, V8.B16
	VORR V13.B16, V9.B16, V9.B16
	VORR V14.B16, V10.B16, V10.B16
	VORR V15.B16, V11.B16, V11.B16
	VORR V8.B16, V0.B16, V0.B16
	VORR V9.B16, V1.B16, V1.B16
	VORR V10.B16, V2.B16, V2.B16
	VORR V11.B16, V3.B16, V3.B16
	VMOVI $255, V28.B16
	WORD $0x4e601f80
	WORD $0x4e611f81
	WORD $0x4e621f82
	WORD $0x4e631f83
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next_after8)
	SUB $192, R12, R1
	ADD $144, R1, R7
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next)
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next)
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next)
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next)
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ONE
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_ZERO
	SEARCH32_QUAD_NONZERO_OR_BRANCH(search32_r900_quad_next)
	MOVD R17, R15
	VMOV V0.D[0], R2
	VMOV V0.D[1], R4
	VMOV V1.D[0], R6
	VMOV V1.D[1], R7
	VMOV V2.D[0], R9
	VMOV V2.D[1], R10
	VMOV V3.D[0], R11
	VMOV V3.D[1], R21
	MOVD $0x8000000000000000, R19
	SEARCH32_EXPAND_DWORD(R2, search32_r900_v0d0_bit, search32_r900_v0d0_done)
	SEARCH32_EXPAND_DWORD(R4, search32_r900_v0d1_bit, search32_r900_v0d1_done)
	SEARCH32_EXPAND_DWORD(R6, search32_r900_v1d0_bit, search32_r900_v1d0_done)
	SEARCH32_EXPAND_DWORD(R7, search32_r900_v1d1_bit, search32_r900_v1d1_done)
	SEARCH32_EXPAND_DWORD(R9, search32_r900_v2d0_bit, search32_r900_v2d0_done)
	SEARCH32_EXPAND_DWORD(R10, search32_r900_v2d1_bit, search32_r900_v2d1_done)
	SEARCH32_EXPAND_DWORD(R11, search32_r900_v3d0_bit, search32_r900_v3d0_done)
	SEARCH32_EXPAND_DWORD(R21, search32_r900_v3d1_bit, search32_r900_v3d1_done)
	B search32_r900_quad_next
search32_r900_quad_next_after8:
	SUB $128, R12
	B search32_r900_quad_advance
search32_r900_quad_next:
	ADD $64, R1, R12
search32_r900_quad_advance:
	ADD $64, R0
	ADD $512, R17
	SUB $64, R5
	CBNZ R5, search32_r900_quad_loop
	MOVD R20, ret+48(FP)
	RET
