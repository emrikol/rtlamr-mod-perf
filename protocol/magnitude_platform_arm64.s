//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

#include "textflag.h"

#define MAGNITUDE_LOAD_RAW(RAW, BIT, INDEX, VALUE) \
	UBFX $BIT, RAW, $8, INDEX                   \
	FMOVD (R2)(INDEX<<3), VALUE

#define MAGNITUDE_EARLY_LOAD(NEXT)            \
	UBFX $0, R4, $8, R5                       \
	FMOVD (R2)(R5<<3), F0                     \
	UBFX $0, R13, $8, R5                      \
	UBFX $8, R4, $8, R6                       \
	FMOVD (R2)(R6<<3), F1                     \
	UBFX $8, R13, $8, R6                      \
	UBFX $16, R4, $8, R7                      \
	FMOVD (R2)(R7<<3), F2                     \
	UBFX $16, R13, $8, R7                     \
	UBFX $24, R4, $8, R8                      \
	FMOVD (R2)(R8<<3), F3                     \
	UBFX $24, R13, $8, R8                     \
	UBFX $32, R4, $8, R9                      \
	FMOVD (R2)(R9<<3), F4                     \
	UBFX $32, R13, $8, R9                     \
	UBFX $40, R4, $8, R10                     \
	FMOVD (R2)(R10<<3), F5                    \
	UBFX $40, R13, $8, R10                    \
	UBFX $48, R4, $8, R11                     \
	FMOVD (R2)(R11<<3), F6                    \
	UBFX $48, R13, $8, R11                    \
	LSR $56, R4, R12                          \
	FMOVD (R2)(R12<<3), F7                    \
	LSR $56, R13, R12                         \
	LDP NEXT(R1), (R4, R13)

#define MAGNITUDE_EARLY_FINISH(O0, O1, O2, O3, O4, O5, O6, O7) \
	FMOVD (R2)(R5<<3), F8                                        \
	FMOVD (R2)(R6<<3), F9                                        \
	FADDD F1, F0, F0                                             \
	FMOVD F0, O0(R0)                                             \
	FMOVD (R2)(R7<<3), F10                                       \
	FMOVD (R2)(R8<<3), F11                                       \
	FADDD F3, F2, F2                                             \
	FMOVD F2, O1(R0)                                             \
	FMOVD (R2)(R9<<3), F12                                       \
	FMOVD (R2)(R10<<3), F13                                      \
	FADDD F5, F4, F4                                             \
	FMOVD F4, O2(R0)                                             \
	FMOVD (R2)(R11<<3), F14                                      \
	FMOVD (R2)(R12<<3), F15                                      \
	FADDD F7, F6, F6                                             \
	FMOVD F6, O3(R0)                                             \
	FADDD F9, F8, F8                                             \
	FMOVD F8, O4(R0)                                             \
	FADDD F11, F10, F10                                          \
	FMOVD F10, O5(R0)                                            \
	FADDD F13, F12, F12                                          \
	FMOVD F12, O6(R0)                                            \
	FADDD F15, F14, F14                                          \
	FMOVD F14, O7(R0)

#define MAGNITUDE_FINAL_FINISH(O0, O1, O2, O3, O4, O5, O6, O7) \
	MAGNITUDE_LOAD_RAW(R4, 0, R5, F0)                            \
	MAGNITUDE_LOAD_RAW(R4, 8, R6, F1)                            \
	MAGNITUDE_LOAD_RAW(R4, 16, R7, F2)                           \
	MAGNITUDE_LOAD_RAW(R4, 24, R8, F3)                           \
	MAGNITUDE_LOAD_RAW(R4, 32, R9, F4)                           \
	MAGNITUDE_LOAD_RAW(R4, 40, R10, F5)                          \
	MAGNITUDE_LOAD_RAW(R4, 48, R11, F6)                          \
	MAGNITUDE_LOAD_RAW(R4, 56, R12, F7)                          \
	MAGNITUDE_LOAD_RAW(R13, 0, R5, F8)                           \
	MAGNITUDE_LOAD_RAW(R13, 8, R6, F9)                           \
	MAGNITUDE_LOAD_RAW(R13, 16, R7, F10)                         \
	MAGNITUDE_LOAD_RAW(R13, 24, R8, F11)                         \
	MAGNITUDE_LOAD_RAW(R13, 32, R9, F12)                         \
	MAGNITUDE_LOAD_RAW(R13, 40, R10, F13)                        \
	MAGNITUDE_LOAD_RAW(R13, 48, R11, F14)                        \
	MAGNITUDE_LOAD_RAW(R13, 56, R12, F15)                        \
	FADDD F1, F0, F0                                             \
	FADDD F3, F2, F2                                             \
	FADDD F5, F4, F4                                             \
	FADDD F7, F6, F6                                             \
	FADDD F9, F8, F8                                             \
	FADDD F11, F10, F10                                          \
	FADDD F13, F12, F12                                          \
	FADDD F15, F14, F14                                          \
	FMOVD F0, O0(R0)                                             \
	FMOVD F2, O1(R0)                                             \
	FMOVD F4, O2(R0)                                             \
	FMOVD F6, O3(R0)                                             \
	FMOVD F8, O4(R0)                                             \
	FMOVD F10, O5(R0)                                            \
	FMOVD F12, O6(R0)                                            \
	FMOVD F14, O7(R0)

// magnitudeLUTFloat64A72 computes exact lut[I]+lut[Q] results. Thirty-two groups per
// hot-loop branch retain the early safe preload schedule while reducing pointer
// and loop-control work. Cold paths preserve every positive multiple-of-eight
// count and never read beyond the input.
TEXT ·magnitudeLUTFloat64A72(SB), NOSPLIT, $0-32
	MOVD output+0(FP), R0
	MOVD input+8(FP), R1
	MOVD lut+16(FP), R2
	MOVD count+24(FP), R3
	LDP (R1), (R4, R13)
	AND $255, R3, R15
	CBZ R15, magnitude_aligned

magnitude_head:
	CMP $8, R3
	BEQ magnitude_final_single
	MAGNITUDE_EARLY_LOAD(16)
	MAGNITUDE_EARLY_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	ADD $16, R1
	ADD $64, R0
	SUB $8, R3
	SUB $8, R15
	CBNZ R15, magnitude_head

magnitude_aligned:
	SUB $256, R3
	CBZ R3, magnitude_final_32
	PCALIGN $64
magnitude_loop:
	MAGNITUDE_EARLY_LOAD(16)
	MAGNITUDE_EARLY_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	MAGNITUDE_EARLY_LOAD(32)
	MAGNITUDE_EARLY_FINISH(64, 72, 80, 88, 96, 104, 112, 120)
	MAGNITUDE_EARLY_LOAD(48)
	MAGNITUDE_EARLY_FINISH(128, 136, 144, 152, 160, 168, 176, 184)
	MAGNITUDE_EARLY_LOAD(64)
	MAGNITUDE_EARLY_FINISH(192, 200, 208, 216, 224, 232, 240, 248)
	MAGNITUDE_EARLY_LOAD(80)
	MAGNITUDE_EARLY_FINISH(256, 264, 272, 280, 288, 296, 304, 312)
	MAGNITUDE_EARLY_LOAD(96)
	MAGNITUDE_EARLY_FINISH(320, 328, 336, 344, 352, 360, 368, 376)
	MAGNITUDE_EARLY_LOAD(112)
	MAGNITUDE_EARLY_FINISH(384, 392, 400, 408, 416, 424, 432, 440)
	MAGNITUDE_EARLY_LOAD(128)
	MAGNITUDE_EARLY_FINISH(448, 456, 464, 472, 480, 488, 496, 504)
	MAGNITUDE_EARLY_LOAD(144)
	MAGNITUDE_EARLY_FINISH(512, 520, 528, 536, 544, 552, 560, 568)
	MAGNITUDE_EARLY_LOAD(160)
	MAGNITUDE_EARLY_FINISH(576, 584, 592, 600, 608, 616, 624, 632)
	MAGNITUDE_EARLY_LOAD(176)
	MAGNITUDE_EARLY_FINISH(640, 648, 656, 664, 672, 680, 688, 696)
	MAGNITUDE_EARLY_LOAD(192)
	MAGNITUDE_EARLY_FINISH(704, 712, 720, 728, 736, 744, 752, 760)
	MAGNITUDE_EARLY_LOAD(208)
	MAGNITUDE_EARLY_FINISH(768, 776, 784, 792, 800, 808, 816, 824)
	MAGNITUDE_EARLY_LOAD(224)
	MAGNITUDE_EARLY_FINISH(832, 840, 848, 856, 864, 872, 880, 888)
	MAGNITUDE_EARLY_LOAD(240)
	MAGNITUDE_EARLY_FINISH(896, 904, 912, 920, 928, 936, 944, 952)
	MAGNITUDE_EARLY_LOAD(256)
	MAGNITUDE_EARLY_FINISH(960, 968, 976, 984, 992, 1000, 1008, 1016)
	ADD $256, R1
	ADD $1024, R0
	MAGNITUDE_EARLY_LOAD(16)
	MAGNITUDE_EARLY_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	MAGNITUDE_EARLY_LOAD(32)
	MAGNITUDE_EARLY_FINISH(64, 72, 80, 88, 96, 104, 112, 120)
	MAGNITUDE_EARLY_LOAD(48)
	MAGNITUDE_EARLY_FINISH(128, 136, 144, 152, 160, 168, 176, 184)
	MAGNITUDE_EARLY_LOAD(64)
	MAGNITUDE_EARLY_FINISH(192, 200, 208, 216, 224, 232, 240, 248)
	MAGNITUDE_EARLY_LOAD(80)
	MAGNITUDE_EARLY_FINISH(256, 264, 272, 280, 288, 296, 304, 312)
	MAGNITUDE_EARLY_LOAD(96)
	MAGNITUDE_EARLY_FINISH(320, 328, 336, 344, 352, 360, 368, 376)
	MAGNITUDE_EARLY_LOAD(112)
	MAGNITUDE_EARLY_FINISH(384, 392, 400, 408, 416, 424, 432, 440)
	MAGNITUDE_EARLY_LOAD(128)
	MAGNITUDE_EARLY_FINISH(448, 456, 464, 472, 480, 488, 496, 504)
	MAGNITUDE_EARLY_LOAD(144)
	MAGNITUDE_EARLY_FINISH(512, 520, 528, 536, 544, 552, 560, 568)
	MAGNITUDE_EARLY_LOAD(160)
	MAGNITUDE_EARLY_FINISH(576, 584, 592, 600, 608, 616, 624, 632)
	MAGNITUDE_EARLY_LOAD(176)
	MAGNITUDE_EARLY_FINISH(640, 648, 656, 664, 672, 680, 688, 696)
	MAGNITUDE_EARLY_LOAD(192)
	MAGNITUDE_EARLY_FINISH(704, 712, 720, 728, 736, 744, 752, 760)
	MAGNITUDE_EARLY_LOAD(208)
	MAGNITUDE_EARLY_FINISH(768, 776, 784, 792, 800, 808, 816, 824)
	MAGNITUDE_EARLY_LOAD(224)
	MAGNITUDE_EARLY_FINISH(832, 840, 848, 856, 864, 872, 880, 888)
	MAGNITUDE_EARLY_LOAD(240)
	MAGNITUDE_EARLY_FINISH(896, 904, 912, 920, 928, 936, 944, 952)
	MAGNITUDE_EARLY_LOAD(256)
	MAGNITUDE_EARLY_FINISH(960, 968, 976, 984, 992, 1000, 1008, 1016)
	ADD $256, R1
	ADD $1024, R0
	SUB $256, R3
	CBNZ R3, magnitude_loop

magnitude_final_32:
	MAGNITUDE_EARLY_LOAD(16)
	MAGNITUDE_EARLY_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	MAGNITUDE_EARLY_LOAD(32)
	MAGNITUDE_EARLY_FINISH(64, 72, 80, 88, 96, 104, 112, 120)
	MAGNITUDE_EARLY_LOAD(48)
	MAGNITUDE_EARLY_FINISH(128, 136, 144, 152, 160, 168, 176, 184)
	MAGNITUDE_EARLY_LOAD(64)
	MAGNITUDE_EARLY_FINISH(192, 200, 208, 216, 224, 232, 240, 248)
	MAGNITUDE_EARLY_LOAD(80)
	MAGNITUDE_EARLY_FINISH(256, 264, 272, 280, 288, 296, 304, 312)
	MAGNITUDE_EARLY_LOAD(96)
	MAGNITUDE_EARLY_FINISH(320, 328, 336, 344, 352, 360, 368, 376)
	MAGNITUDE_EARLY_LOAD(112)
	MAGNITUDE_EARLY_FINISH(384, 392, 400, 408, 416, 424, 432, 440)
	MAGNITUDE_EARLY_LOAD(128)
	MAGNITUDE_EARLY_FINISH(448, 456, 464, 472, 480, 488, 496, 504)
	MAGNITUDE_EARLY_LOAD(144)
	MAGNITUDE_EARLY_FINISH(512, 520, 528, 536, 544, 552, 560, 568)
	MAGNITUDE_EARLY_LOAD(160)
	MAGNITUDE_EARLY_FINISH(576, 584, 592, 600, 608, 616, 624, 632)
	MAGNITUDE_EARLY_LOAD(176)
	MAGNITUDE_EARLY_FINISH(640, 648, 656, 664, 672, 680, 688, 696)
	MAGNITUDE_EARLY_LOAD(192)
	MAGNITUDE_EARLY_FINISH(704, 712, 720, 728, 736, 744, 752, 760)
	MAGNITUDE_EARLY_LOAD(208)
	MAGNITUDE_EARLY_FINISH(768, 776, 784, 792, 800, 808, 816, 824)
	MAGNITUDE_EARLY_LOAD(224)
	MAGNITUDE_EARLY_FINISH(832, 840, 848, 856, 864, 872, 880, 888)
	MAGNITUDE_EARLY_LOAD(240)
	MAGNITUDE_EARLY_FINISH(896, 904, 912, 920, 928, 936, 944, 952)
	MAGNITUDE_EARLY_LOAD(256)
	MAGNITUDE_EARLY_FINISH(960, 968, 976, 984, 992, 1000, 1008, 1016)
	ADD $256, R1
	ADD $1024, R0
	MAGNITUDE_EARLY_LOAD(16)
	MAGNITUDE_EARLY_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	MAGNITUDE_EARLY_LOAD(32)
	MAGNITUDE_EARLY_FINISH(64, 72, 80, 88, 96, 104, 112, 120)
	MAGNITUDE_EARLY_LOAD(48)
	MAGNITUDE_EARLY_FINISH(128, 136, 144, 152, 160, 168, 176, 184)
	MAGNITUDE_EARLY_LOAD(64)
	MAGNITUDE_EARLY_FINISH(192, 200, 208, 216, 224, 232, 240, 248)
	MAGNITUDE_EARLY_LOAD(80)
	MAGNITUDE_EARLY_FINISH(256, 264, 272, 280, 288, 296, 304, 312)
	MAGNITUDE_EARLY_LOAD(96)
	MAGNITUDE_EARLY_FINISH(320, 328, 336, 344, 352, 360, 368, 376)
	MAGNITUDE_EARLY_LOAD(112)
	MAGNITUDE_EARLY_FINISH(384, 392, 400, 408, 416, 424, 432, 440)
	MAGNITUDE_EARLY_LOAD(128)
	MAGNITUDE_EARLY_FINISH(448, 456, 464, 472, 480, 488, 496, 504)
	MAGNITUDE_EARLY_LOAD(144)
	MAGNITUDE_EARLY_FINISH(512, 520, 528, 536, 544, 552, 560, 568)
	MAGNITUDE_EARLY_LOAD(160)
	MAGNITUDE_EARLY_FINISH(576, 584, 592, 600, 608, 616, 624, 632)
	MAGNITUDE_EARLY_LOAD(176)
	MAGNITUDE_EARLY_FINISH(640, 648, 656, 664, 672, 680, 688, 696)
	MAGNITUDE_EARLY_LOAD(192)
	MAGNITUDE_EARLY_FINISH(704, 712, 720, 728, 736, 744, 752, 760)
	MAGNITUDE_EARLY_LOAD(208)
	MAGNITUDE_EARLY_FINISH(768, 776, 784, 792, 800, 808, 816, 824)
	MAGNITUDE_EARLY_LOAD(224)
	MAGNITUDE_EARLY_FINISH(832, 840, 848, 856, 864, 872, 880, 888)
	MAGNITUDE_EARLY_LOAD(240)
	MAGNITUDE_EARLY_FINISH(896, 904, 912, 920, 928, 936, 944, 952)
	MAGNITUDE_FINAL_FINISH(960, 968, 976, 984, 992, 1000, 1008, 1016)
	RET

magnitude_final_single:
	MAGNITUDE_FINAL_FINISH(0, 8, 16, 24, 32, 40, 48, 56)
	RET
