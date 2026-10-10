//go:build !linux

package main

import "errors"

func snpGetReport(device string, userData []byte, vmpl uint32) ([]byte, error) {
	return nil, errors.New("SNP attestation is only supported on linux")
}

func kernelLog() string { return "" }
