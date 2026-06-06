package main

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	runs  = 10
	cores = 4
	bits  = 4096
)

func generateKey(keysize int) error {
	// 1. Generate an RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, keysize)
	if err != nil {
		return err
	}

	_ = privateKey

	return nil
}
func keyGenProc(results chan string) {
	if took, err := generate(bits, runs); err != nil {
		results <- fmt.Sprintf("error: %v", err)
	} else {
		results <- fmt.Sprintf("rsa.%v: Took %v microseconds per key (over course of %v runs)", bits, formatWithCommas(took), runs)
	}
}

func main() {
	readArguments()
	fmt.Printf("(doing rsa.%v key gen; %v runs; using %v cores)\n", bits, runs, cores)
	if runs < 1 || cores < 1 || runs > 10_000 || bits < 1024 || (bits%8 != 0) {
		fmt.Printf("arguments are out of allowed range...\n")
		return
	}

	// multiCore(func(results chan string) { results <- fmt.Sprint("*")})
	multiCore(keyGenProc)
}

func generate(keysize, runs int) (uint64, error) {
	start := time.Now()
	for i := 0; i < runs; i++ {
		if err := generateKey(keysize); err != nil {
			fmt.Println(err)
			return 0, err
		}
	}
	tookMicroseconds := time.Since(start).Microseconds()
	rate := float64(tookMicroseconds) / float64(runs)
	return uint64(rate), nil
}

// formatWithCommas formats an integer to a string with comma delimiters
func formatWithCommas(num uint64) string {
	str := fmt.Sprintf("%v", num)
	length := len(str)
	if length <= 3 {
		return str
	}

	// Calculate capacity to minimize allocations
	result := make([]byte, 0, length+(length-1)/3)

	// Handle negative numbers if necessary
	start := 0
	if str[0] == '-' {
		result = append(result, '-')
		start = 1
	}

	for i := start; i < length; i++ {
		if (length-i)%3 == 0 && i != start {
			result = append(result, ',')
		}
		result = append(result, str[i])
	}
	return string(result)
}

func multiCore(doWork func(chan string)) {

	cores2use := runtime.NumCPU()
	if cores2use > cores {
		cores2use = cores
	}

	results := make(chan string, cores*10)

	start := make(chan struct{})
	ready := sync.WaitGroup{}
	done := sync.WaitGroup{}

	for range cores2use {
		ready.Add(1)
		done.Add(1)

		go func() {
			ready.Done()
			<-start // blocks until channel is closed
			doWork(results)
			done.Done()
		}()
	}

	// ... do setup ...
	ready.Wait() // wait for all goroutines to be ready

	launched := time.Now()
	close(start) // unblocks ALL goroutines simultaneously
	done.Wait()  // wait for all goroutines to finish
	tookMicroseconds := time.Since(launched).Microseconds()

	close(results)
	for msg := range results {
		fmt.Printf("\t%s\n", msg)
	}
	fmt.Printf("it took %v microsec to complete %v cores run\n", formatWithCommas(uint64(tookMicroseconds)), cores)
}

func readArguments() {
	for _, arg := range os.Args[1:] {
		parts := strings.Split(arg, "=")
		key := strings.TrimPrefix(strings.ToLower(parts[0]), "-")
		svalue := ""
		ivalue := 0
		if len(parts) > 1 {
			svalue = parts[1]
			if value, err := strconv.Atoi(svalue); err == nil {
				ivalue = value
			}
		}

		switch key {
		case "runs":
			runs = ivalue
		case "cores":
			cores = ivalue
		case "bits":
			bits = ivalue

		default:
			fmt.Printf("unknown argument: %v\n", parts[0])
			fallthrough
		case "help":
			fmt.Printf("usage:\n%s [-bits=%v] [-runs=%v] [-cores=%v]\n", os.Args[0], bits, runs, cores)
			os.Exit(7)
		}
	}
}
