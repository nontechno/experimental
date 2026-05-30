// cpuid_amd64.s — assembly implementation of func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)
//
// Uses Go ABI0 (stack-based), so the compiler automatically generates a
// register→stack bridge wrapper when this is called from regular Go code.
//
// Stack frame layout (24 bytes, no local vars):
//   FP+ 0  leaf     uint32  (input)
//   FP+ 4  subleaf  uint32  (input)
//   FP+ 8  eax      uint32  (output)
//   FP+12  ebx      uint32  (output)
//   FP+16  ecx      uint32  (output)
//   FP+20  edx      uint32  (output)

#include "textflag.h"

// func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)
TEXT ·cpuid(SB), NOSPLIT, $0-24
    MOVL leaf+0(FP),    AX
    MOVL subleaf+4(FP), CX
    CPUID
    MOVL AX, eax+8(FP)
    MOVL BX, ebx+12(FP)
    MOVL CX, ecx+16(FP)
    MOVL DX, edx+20(FP)
    RET
