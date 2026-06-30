package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

func runScript(path string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(path, args...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()

	return outBuf.String(), errBuf.String(), err
}

func reloadAcronyms(w http.ResponseWriter) {
	defer func() {
		if err := recover(); err != nil {
			fmt.Fprintf(w, "panic: %v", err)
		}
	}()

	report := ""
	scriptName := getConfig("acro.reload.script", "")
	if len(scriptName) > 0 {
		parts := strings.Split(scriptName, " ")
		args := parts[1:]
		outText, errText, err := runScript(parts[0], args...)
		if err != nil {
			fmt.Fprintf(w, "failed: err: [%v]; stdout: [%s]; stderr: [%s]", err, outText, errText)
			return
		}
		report += outText
	}

	fileName := getConfig("acro.path", "./acronyms.json")
	raw, err := os.ReadFile(fileName)
	if err != nil {
		log.Printf("Error reading file %v: %v", fileName, err)
		fmt.Fprintf(w, "Error reading file %v: %v", fileName, err)
		return
	}

	if err := json.Unmarshal(raw, &acronyms); err != nil {
		log.Printf("Error parsing file %v: %v", fileName, err)
		return
	}

	report += fmt.Sprintf("\nreloaded file [%s]", fileName)

	// rebuild lower-cased map
	lowerCaseAcronyms = map[string]string{}
	for key := range acronyms {
		lowerCaseAcronyms[strings.ToLower(key)] = key
	}

	fmt.Fprintf(w, "reload: %s", report)
}
