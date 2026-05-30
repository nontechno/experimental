#include "textflag.h"

// func cpuid(eaxIn, ecxIn uint32) (eax, ebx, ecx, edx uint32)
// Arguments  : eaxIn @ +0(FP), ecxIn @ +4(FP)   (8 bytes in)
// Return vals: eax   @ +8(FP), ebx  @ +12(FP),
//              ecx   @ +16(FP), edx  @ +20(FP)   (16 bytes out)
// Total frame: 24 bytes
TEXT ·cpuid(SB),NOSPLIT,$0-24
    MOVL eaxIn+0(FP), AX
    MOVL ecxIn+4(FP), CX
    CPUID
    MOVL AX, eax+8(FP)
    MOVL BX, ebx+12(FP)
    MOVL CX, ecx+16(FP)
    MOVL DX, edx+20(FP)
    RET
