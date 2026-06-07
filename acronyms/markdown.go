package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

/* example of what to expect

# Business & Software Acronyms Reference

*Domains: Engineering · Agile/Process · Communication · People/Roles · Strategy*

| Acronym | Domain | Meaning(s) |
|---------|--------|------------|
| ADR | Engineering | Architecture Decision Record |
| AFAIK | Communication | As Far As I Know |
| AFK | Communication | Away from Keyboard |


*/

func loadMarkdown(fileName string, store *Holder) (bool, error) {

	file, err := os.Open(fileName)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// 3. Create a scanner for the file
	scanner := bufio.NewScanner(file)

	foundTableContent := false
	columns := 2

	// 4. Scan through the file line by line
	for scanner.Scan() {
		line := scanner.Text()
		if foundTableContent {
			if parts := strings.Split(line, "|"); len(parts) != columns+2 {
				// skip this line
			} else {
				key := trim(parts[1])
				value := trim(parts[columns])

				for _, entry := range getValues(value) {
					store.add(key, entry)
				}
			}
		} else {
			if strings.HasPrefix(line, "|-----") {
				foundTableContent = true
				if parts := len(strings.Split(line, "|")); parts < 4 {
					// to few parts - exit
					return false, nil
				} else {
					columns = parts - 2
				}
			}
		}

	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func getValues(original string) []string {
	const separator = " · "
	const comma = "; "
	values := []string{}

	unified := strings.Replace(original, separator, comma, -1)
	for _, entry := range strings.Split(unified, comma) {
		trimmed := trim(entry)
		if len(trimmed) > 0 {
			values = append(values, trimmed)
		}
	}

	return values
}

func md_test() {
	store := Holder{make(map[string]Entry)}
	if _, err := loadMarkdown(".test/acronyms.md", &store); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := loadMarkdown(".test/acronyms2.md", &store); err != nil {
		fmt.Println(err)
		return
	}
	store.print()
}
