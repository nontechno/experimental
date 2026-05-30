//go:build linux && amd64

package main

// cpuid executes the CPUID instruction with the given EAX and ECX leaf/sub-leaf values.
// Implemented in cpuid_amd64.s.
func cpuid(eaxIn, ecxIn uint32) (eax, ebx, ecx, edx uint32)
