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

	loadConfig()
	list := getListOfFiles()

	store := Holder{make(map[string]Entry)}
	for _, arg := range list {
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
	if fileName := configGet("save.as"); len(fileName) > 0 {
		trace("saving .json into [%s]\n", fileName)
		if err := store.save(fileName); err != nil {
			fmt.Printf("failed to produce [%s]: due to: %v\n", fileName, err)
		}
	} else {
		trace("not saving .json version\n")
	}
}

func getListOfFiles() []string {
	listOfFiles := make([]string, 0)
	index := 1
	for index < len(os.Args) {
		arg := os.Args[index]
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "-config":
				if (index + 1) < len(os.Args) {
					configFileName = os.Args[index+1]
					loadConfig()
				} else {
					failure("config file not found")
				}

			case "-md", "-html":
				//reserved for future use
				break

			default:
				failure("unknown argument [%s]", arg)
			}
			// it is a param: skip it and (maybe) next one
			index++
		} else if strings.HasSuffix(strings.ToLower(arg), ".acronyms") {
			if raw, err := os.ReadFile(arg); err == nil {
				trace("opened file [%s]", arg)
				lines := strings.Split(string(raw), "\n")
				for _, line := range lines {
					if line = strings.Trim(line, " \t\r"); len(line) > 0 {
						listOfFiles = append(listOfFiles, line)
					}
				}
			} else {
				failure("failed to read [%s]: %v\n", arg, err)
			}
		} else if len(arg) > 0 {
			listOfFiles = append(listOfFiles, arg)
		}
		index++
	}
	return listOfFiles
}

func processPlain(filename string, store *Holder) error {
	// 1. Open the file
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	trace("opened file [%s]", filename)

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
		// fmt.Printf("\ndiscarded %v entries: [%v]\n", len(discarded), strings.Join(discarded, "; "))
	}
	return nil
}

func failure(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(7)
}
