package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: acronyms <file>")
		return
	}

	store := Holder{make(map[string]Entry)}

	for _, arg := range os.Args[1:] {
		if strings.HasSuffix(strings.ToLower(arg), ".md") {
			if _, err := loadMarkdown(arg, &store); err != nil {
				fmt.Printf("failed to proccess [%s]: due to: %v\n", arg, err)
				return
			}
		} else {
			if err := processPlain(arg, &store); err != nil {
				fmt.Printf("failed to proccess [%s]: due to: %v\n", arg, err)
				return
			}
		}
	}

	store.print()
}

func processPlain(filename string, store *Holder) error {
	// 1. Open the file
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	// 2. Ensure the file is closed when the function finishes
	defer file.Close()

	// 3. Create a scanner for the file
	scanner := bufio.NewScanner(file)

	discarded := []string{}

	// 4. Scan through the file line by line
	for scanner.Scan() {
		line := scanner.Text()
		line = trim(line)
		if len(line) == 0 {
			// discarded = append(discarded, line)
			continue
		}

		if !isAlphanumeric([]rune(line)[0]) {
			discarded = append(discarded, line)
			continue
		}

		if left, right, found := split(line); found {
			store.add(left, right)
			//fmt.Printf("[%s]\t\t[%s]\n", left, right) // Print the current line
		} else {
			discarded = append(discarded, line)
		}
	}

	// 5. Check for errors that occurred during scanning
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(discarded) > 0 {
		fmt.Printf("\ndiscarded %v entries: [%v]\n", len(discarded), strings.Join(discarded, "; "))
	}
	return nil
}
