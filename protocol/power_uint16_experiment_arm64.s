//go:build d3_power_neon && linux && arm64 && gc && !purego && !race

#include "textflag.h"

// powerIQUint16NEONLeaf converts a positive multiple of sixteen interleaved
// IQ pairs. Rotate the two centered odd coordinates by 45 degrees:
//
//	a = 2*I - 255, b = 2*Q - 255
//	u = (a+b)/2 = I+Q-255, v = (a-b)/2 = I-Q
//	P = (a*a+b*b)/2 = u*u+v*v
//
// LD2 deinterleaves sixteen I and Q bytes. UADDL forms I+Q, while UABDL forms
// abs(I-Q), whose square equals v*v. Both coordinates are in [-255,255], each
// square is at most 65025, and the correlated sum P is globally at most 65025.
// Consequently the low 16-bit VMUL products and wrapping H-lane VADD are the
// exact uint16 result; no 32-bit expansion, pair reduction, shift, or narrow is
// needed.
//
// Raw words are used for AdvSIMD operations not named by Go's ARM64 assembler.
// Each word and lane mapping is fixed by linked-disassembly inspection.
TEXT ·powerIQUint16NEONLeaf(SB), NOSPLIT, $0-24
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	MOVD count+16(FP), R2
	WORD $0x4f0787ff // MOVI V31.H8, $255

	PCALIGN $16
power_uint16_loop:
	VLD2.P 32(R1), [V0.B16, V1.B16]
	WORD $0x2e210002 // UADDL  V0.B8, V1.B8, V2.H8
	WORD $0x6e210003 // UADDL2 V0.B16, V1.B16, V3.H8
	WORD $0x2e217004 // UABDL  V0.B8, V1.B8, V4.H8
	WORD $0x6e217005 // UABDL2 V0.B16, V1.B16, V5.H8
	VSUB V31.H8, V2.H8, V2.H8
	VSUB V31.H8, V3.H8, V3.H8
	WORD $0x4e629c42 // MUL V2.H8, V2.H8, V2.H8
	WORD $0x4e639c63 // MUL V3.H8, V3.H8, V3.H8
	WORD $0x4e649c84 // MUL V4.H8, V4.H8, V4.H8
	WORD $0x4e659ca5 // MUL V5.H8, V5.H8, V5.H8
	VADD V4.H8, V2.H8, V2.H8
	VADD V5.H8, V3.H8, V3.H8
	VST1.P [V2.H8, V3.H8], 32(R0)

	SUBS $16, R2, R2
	BNE power_uint16_loop
	RET

// powerIQUint16NEONUZPLeaf is the exact arithmetic control with a different
// input-layout mechanism. LDP loads the same 32 bytes and UZP1/UZP2 explicitly
// deinterleave them. This adds two vector permutes but reveals whether LD2's
// structure-load expansion is unexpectedly more costly on a measured core.
TEXT ·powerIQUint16NEONUZPLeaf(SB), NOSPLIT, $0-24
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	MOVD count+16(FP), R2
	WORD $0x4f0787ff // MOVI V31.H8, $255

	PCALIGN $16
power_uint16_uzp_loop:
	WORD $0xacc10420 // LDP.P 32(R1), (V0.Q, V1.Q)
	VUZP1 V1.B16, V0.B16, V6.B16
	VUZP2 V1.B16, V0.B16, V7.B16
	WORD $0x2e2700c2 // UADDL  V6.B8, V7.B8, V2.H8
	WORD $0x6e2700c3 // UADDL2 V6.B16, V7.B16, V3.H8
	WORD $0x2e2770c4 // UABDL  V6.B8, V7.B8, V4.H8
	WORD $0x6e2770c5 // UABDL2 V6.B16, V7.B16, V5.H8
	VSUB V31.H8, V2.H8, V2.H8
	VSUB V31.H8, V3.H8, V3.H8
	WORD $0x4e629c42 // MUL V2.H8, V2.H8, V2.H8
	WORD $0x4e639c63 // MUL V3.H8, V3.H8, V3.H8
	WORD $0x4e649c84 // MUL V4.H8, V4.H8, V4.H8
	WORD $0x4e659ca5 // MUL V5.H8, V5.H8, V5.H8
	VADD V4.H8, V2.H8, V2.H8
	VADD V5.H8, V3.H8, V3.H8
	VST1.P [V2.H8, V3.H8], 32(R0)

	SUBS $16, R2, R2
	BNE power_uint16_uzp_loop
	RET

// powerIQUint16NEONU8MulLeaf computes the same rotated coordinates without
// widening the coordinates before multiplication:
//
//	dv   = abs(I-Q)             = abs(v)
//	qnot = 255-Q
//	du   = abs(I-qnot)          = abs(I+Q-255) = abs(u)
//	P    = dv*dv + du*du
//
// Both absolute differences fit uint8. UMULL/UMULL2 create exact uint16
// squares and UMLAL/UMLAL2 add the second exact square. The exhaustive domain
// proof establishes that the final accumulator never exceeds 65025.
TEXT ·powerIQUint16NEONU8MulLeaf(SB), NOSPLIT, $0-24
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	MOVD count+16(FP), R2

	PCALIGN $16
power_uint16_u8mul_loop:
	VLD2.P 32(R1), [V0.B16, V1.B16]
	WORD $0x6e217402 // UABD V0.B16, V1.B16, V2.B16: dv
	WORD $0x6e205821 // MVN V1.B16, V1.B16: qnot
	WORD $0x6e217403 // UABD V0.B16, V1.B16, V3.B16: du
	WORD $0x2e22c044 // UMULL  V2.B8, V2.B8, V4.H8
	WORD $0x6e22c045 // UMULL2 V2.B16, V2.B16, V5.H8
	WORD $0x2e238064 // UMLAL   V3.B8, V3.B8, V4.H8
	WORD $0x6e238065 // UMLAL2  V3.B16, V3.B16, V5.H8
	VST1.P [V4.H8, V5.H8], 32(R0)

	SUBS $16, R2, R2
	BNE power_uint16_u8mul_loop
	RET

// powerIQUint16NEONU8Mul32Leaf is a genuinely distinct two-bank schedule over
// 32 outputs per backedge. It keeps independent A/B input, difference, and
// accumulator registers. All four initial widening multiplies issue before
// their four accumulator-dependent UMLAL operations, giving each initial
// product the documented four-instruction latency window. No early lookahead
// occurs: each iteration consumes exactly its current 64 input bytes and emits
// exactly 64 output bytes.
TEXT ·powerIQUint16NEONU8Mul32Leaf(SB), NOSPLIT, $0-24
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	MOVD count+16(FP), R2

	PCALIGN $16
power_uint16_u8mul32_loop:
	VLD2.P 32(R1), [V0.B16, V1.B16]
	VLD2.P 32(R1), [V8.B16, V9.B16]
	WORD $0x6e217402 // UABD V0.B16, V1.B16, V2.B16: A dv
	WORD $0x6e29750a // UABD V8.B16, V9.B16, V10.B16: B dv
	WORD $0x6e205821 // MVN V1.B16, V1.B16: A qnot
	WORD $0x6e205929 // MVN V9.B16, V9.B16: B qnot
	WORD $0x6e217403 // UABD V0.B16, V1.B16, V3.B16: A du
	WORD $0x6e29750b // UABD V8.B16, V9.B16, V11.B16: B du
	WORD $0x2e22c044 // UMULL  V2.B8, V2.B8, V4.H8
	WORD $0x6e22c045 // UMULL2 V2.B16, V2.B16, V5.H8
	WORD $0x2e2ac14c // UMULL  V10.B8, V10.B8, V12.H8
	WORD $0x6e2ac14d // UMULL2 V10.B16, V10.B16, V13.H8
	WORD $0x2e238064 // UMLAL   V3.B8, V3.B8, V4.H8
	WORD $0x6e238065 // UMLAL2  V3.B16, V3.B16, V5.H8
	WORD $0x2e2b816c // UMLAL   V11.B8, V11.B8, V12.H8
	WORD $0x6e2b816d // UMLAL2  V11.B16, V11.B16, V13.H8
	VST1.P [V4.H8, V5.H8], 32(R0)
	VST1.P [V12.H8, V13.H8], 32(R0)

	SUBS $32, R2, R2
	BNE power_uint16_u8mul32_loop
	RET
