package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func loadRawContent(fileName string) ([]byte, error) {
	log.Printf("loadContent(%s)", fileName)

	abs, err := filepath.Abs(fileName)
	if err != nil {
		log.Printf("Error getting absolute path for %s: %v", fileName, err)
		return nil, err
	}

	rawData, err := os.ReadFile(abs)
	if err != nil {
		log.Printf("Error reading file %s: %v", fileName, err)
		return nil, err
	}

	extension := strings.ToLower(filepath.Ext(abs))
	if extension == ".md" {
		log.Printf("converting md to html")
		htmlData, err := Md2Html(rawData)
		if err != nil {
			log.Printf("Error converting file %s: %v", fileName, err)
			return nil, err
		}
		return htmlData, nil

	} else if slices.Contains([]string{".htm", ".html"}, extension) {
		return rawData, nil
	}
	return []byte(fmt.Sprintf("unhandled extension: %s", extension)), nil
}

func loadContent(fileName string) ([]byte, error) {
	return loadRawContent(fileName)
}
