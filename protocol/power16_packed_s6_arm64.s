//go:build linux && arm64 && gc && !purego && !race

#include "textflag.h"

// Turn four deltas into an inclusive prefix, add the old scalar carry, retain
// the new scalar carry in NEW, and replace D with the four exclusive margins.
#define S6_PREFIX4(D, OLD, NEW)                 \
	VEXT $12, D.B16, V23.B16, V22.B16;       \
	VADD V22.S4, D.S4, D.S4;                 \
	VEXT $8, D.B16, V23.B16, V22.B16;        \
	VADD V22.S4, D.S4, D.S4;                 \
	VADD OLD.S4, D.S4, D.S4;                 \
	VDUP D.S[3], NEW.S4;                     \
	VEXT $12, D.B16, OLD.B16, D.B16

// fusedPowerManchesterPackedU8Mul32A72 is the accepted fixed-geometry fused
// Power16 leaf plus a second, exact packed-decision output. Each iteration
// stores 32 ordinary decision bytes and four packed bytes. The packed pointer
// advances exactly 1024 bytes over 256 iterations. No input or output is read
// or written outside the fixed ABI extents.
TEXT ·fusedPowerManchesterPackedU8Mul32A72(SB), NOSPLIT, $0-32
	MOVD decisions+0(FP), R0
	MOVD packed+8(FP), R8
	MOVD window+16(FP), R1
	MOVD iq+24(FP), R2

	// Initialize margin=sum(history[0:72])-sum(history[72:144]).
	MOVD R1, R3
	ADD $144, R1, R4
	VEOR V24.B16, V24.B16, V24.B16
	MOVD $9, R6
s6_power_manchester_init:
	VLD1.P 16(R3), [V0.H8]
	VLD1.P 16(R4), [V1.H8]
	VUADDLV V0.H8, V2
	VUADDLV V1.H8, V3
	VSUB V3.S4, V2.S4, V2.S4
	VADD V2.S4, V24.S4, V24.S4
	SUBS $1, R6, R6
	BNE s6_power_manchester_init
	VDUP V24.S[0], V24.S4

	MOVD R1, R3
	ADD $144, R1, R4
	ADD $288, R1, R5
	VEOR V23.B16, V23.B16, V23.B16
	VMOVI $1, V29.B16
	MOVD $·fusedPowerManchesterSignIndex(SB), R7
	VLD1 (R7), [V30.B16]
	MOVD $0x8040201008040201, R9
	MOVD $256, R6

	PCALIGN $16
s6_power_manchester_loop:
	VLD2.P 32(R2), [V0.B16, V1.B16]
	VLD2.P 32(R2), [V8.B16, V9.B16]
	WORD $0x6e217402 // UABD V0.B16, V1.B16, V2.B16
	WORD $0x6e29750a // UABD V8.B16, V9.B16, V10.B16
	WORD $0x6e205821 // MVN V1.B16, V1.B16
	WORD $0x6e205929 // MVN V9.B16, V9.B16
	WORD $0x6e217403 // UABD V0.B16, V1.B16, V3.B16
	WORD $0x6e29750b // UABD V8.B16, V9.B16, V11.B16
	WORD $0x2e22c044 // UMULL V2.B8, V2.B8, V4.H8
	WORD $0x6e22c045 // UMULL2 V2.B16, V2.B16, V5.H8
	WORD $0x2e2ac14c // UMULL V10.B8, V10.B8, V12.H8
	WORD $0x6e2ac14d // UMULL2 V10.B16, V10.B16, V13.H8
	WORD $0x2e238064 // UMLAL V3.B8, V3.B8, V4.H8
	WORD $0x6e238065 // UMLAL2 V3.B16, V3.B16, V5.H8
	WORD $0x2e2b816c // UMLAL V11.B8, V11.B8, V12.H8
	WORD $0x6e2b816d // UMLAL2 V11.B16, V11.B16, V13.H8

	WORD $0xacc10460 // LDP.P 32(R3), (V0.Q, V1.Q)
	WORD $0xacc12468 // LDP.P 32(R3), (V8.Q, V9.Q)
	WORD $0xacc10c82 // LDP.P 32(R4), (V2.Q, V3.Q)
	WORD $0xacc12c8a // LDP.P 32(R4), (V10.Q, V11.Q)

	WORD $0x2e60204e // USUBL V2.H4, V0.H4, V14.S4
	WORD $0x2e642056 // USUBL V2.H4, V4.H4, V22.S4
	VADD V22.S4, V14.S4, V14.S4
	WORD $0x6e60204f // USUBL2 V2.H8, V0.H8, V15.S4
	WORD $0x6e642056 // USUBL2 V2.H8, V4.H8, V22.S4
	VADD V22.S4, V15.S4, V15.S4
	WORD $0x2e612070 // USUBL V3.H4, V1.H4, V16.S4
	WORD $0x2e652076 // USUBL V3.H4, V5.H4, V22.S4
	VADD V22.S4, V16.S4, V16.S4
	WORD $0x6e612071 // USUBL2 V3.H8, V1.H8, V17.S4
	WORD $0x6e652076 // USUBL2 V3.H8, V5.H8, V22.S4
	VADD V22.S4, V17.S4, V17.S4
	WORD $0x2e682152 // USUBL V10.H4, V8.H4, V18.S4
	WORD $0x2e6c2156 // USUBL V10.H4, V12.H4, V22.S4
	VADD V22.S4, V18.S4, V18.S4
	WORD $0x6e682153 // USUBL2 V10.H8, V8.H8, V19.S4
	WORD $0x6e6c2156 // USUBL2 V10.H8, V12.H8, V22.S4
	VADD V22.S4, V19.S4, V19.S4
	WORD $0x2e692174 // USUBL V11.H4, V9.H4, V20.S4
	WORD $0x2e6d2176 // USUBL V11.H4, V13.H4, V22.S4
	VADD V22.S4, V20.S4, V20.S4
	WORD $0x6e692175 // USUBL2 V11.H8, V9.H8, V21.S4
	WORD $0x6e6d2176 // USUBL2 V11.H8, V13.H8, V22.S4
	VADD V22.S4, V21.S4, V21.S4

	VST1.P [V4.H8, V5.H8], 32(R5)
	VST1.P [V12.H8, V13.H8], 32(R5)

	S6_PREFIX4(V14, V24, V25)
	S6_PREFIX4(V15, V25, V24)
	S6_PREFIX4(V16, V24, V25)
	S6_PREFIX4(V17, V25, V24)
	S6_PREFIX4(V18, V24, V25)
	S6_PREFIX4(V19, V25, V24)
	S6_PREFIX4(V20, V24, V25)
	S6_PREFIX4(V21, V25, V24)

	VTBL V30.B16, [V14.B16, V15.B16, V16.B16, V17.B16], V28.B16
	VORR V29.B16, V29.B16, V31.B16
	WORD $0x4f09179f // SSRA V31.B16, V28.B16, #7
	VST1.P [V31.B16], 16(R0)
	VMOV V31.D[0], R10
	VMOV V31.D[1], R11

	// Start the second table gather before finishing the first scalar pack.
	// This hides the cross-domain moves and integer multiplies behind the
	// otherwise independent second decision-materialization chain.
	VTBL V30.B16, [V18.B16, V19.B16, V20.B16, V21.B16], V28.B16
	VORR V29.B16, V29.B16, V31.B16
	MUL R9, R10, R10
	MUL R9, R11, R11
	WORD $0x4f09179f // SSRA V31.B16, V28.B16, #7
	LSR $56, R10, R10
	LSR $56, R11, R11
	VST1.P [V31.B16], 16(R0)
	VMOV V31.D[0], R12
	VMOV V31.D[1], R13
	ORR R11<<8, R10, R10
	MOVH.P R10, 2(R8)
	MUL R9, R12, R12
	MUL R9, R13, R13
	LSR $56, R12, R12
	SUBS $1, R6, R6
	LSR $56, R13, R13
	ORR R13<<8, R12, R12
	MOVH.P R12, 2(R8)

	BNE s6_power_manchester_loop
	RET

// packDecision16S6A72 is a test seam for the exact four scalar operations used
// for each eight-decision half in the candidate leaf.
TEXT ·packDecision16S6A72(SB), NOSPLIT, $0-16
	MOVD output+0(FP), R8
	MOVD decisions+8(FP), R0
	VLD1 (R0), [V31.B16]
	MOVD $0x8040201008040201, R9
	VMOV V31.D[0], R10
	VMOV V31.D[1], R11
	MUL R9, R10, R10
	MUL R9, R11, R11
	LSR $56, R10, R10
	LSR $56, R11, R11
	ORR R11<<8, R10, R10
	MOVH R10, (R8)
	RET
