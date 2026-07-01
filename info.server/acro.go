package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	acroInnerPrefix = "/inner/"

	acronyms          = map[string][]string{}
	lowerCaseAcronyms = map[string]string{}

	acroStaticFiles = map[string]string{
		"/favicon.ico": "",
		"/style.css":   "",
	}

	acroLinkMatch *regexp.Regexp
	acroLinksMap  sync.Map
)

func acroInit() {
	acroLinkMatch = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
}

func acroHandler(w http.ResponseWriter, r *http.Request) {

	if acroLocalFile(w, r) {
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")

	if len(acronyms) == 0 || (len(parts) == 1 && parts[0] == "reload") {
		reloadAcronyms(w)
	}

	if len(acronyms) == 0 {
		showMissingData(w, r)
		return
	}
	if parts[0] == "" {
		acroShowAll(w, r)
	} else if strings.ContainsAny(parts[0], "*?") {
		acroShowWildcard(w, r, parts[0])
	} else {
		acroShowSingle(w, r, parts[0])
	}

	// fmt.Fprintf(w, "path [%v], parts [%v]", r.URL.Path, parts)
}

func stringMatch(pattern, candidate string) bool {
	if matched, err := filepath.Match(pattern, candidate); err == nil {
		return matched
	}
	return false
}

func showMissingData(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "no data was found")
}

func acroShowAll(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, acroTableHeader)

	keys := []string{}
	for key := range acronyms {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		values := acronyms[key]
		printEntry(w, key, values)
	}
	fmt.Fprintf(w, acroTableFooter)
}

func acroShowWildcard(w http.ResponseWriter, r *http.Request, wildcard string) {
	wildcard = strings.ToLower(wildcard)
	matches := []string{}
	for key := range lowerCaseAcronyms {
		if stringMatch(wildcard, key) {
			matches = append(matches, key)
		}
	}

	if len(matches) == 0 {
		fmt.Fprintf(w, "found no entries for [%s]", wildcard)
		return
	}

	sort.Strings(matches)

	fmt.Fprintf(w, acroTableHeader)
	for _, key := range matches {
		values := acronyms[lowerCaseAcronyms[key]]
		printEntry(w, key, values)
	}
	fmt.Fprintf(w, acroTableFooter)
}

func acroShowSingle(w http.ResponseWriter, r *http.Request, term string) {

	if key, found := lowerCaseAcronyms[strings.ToLower(term)]; found && len(key) > 0 {
		fmt.Fprintf(w, acroTableHeader)
		printEntry(w, key, acronyms[key])
		fmt.Fprintf(w, acroTableFooter)
	} else {
		fmt.Fprintf(w, "single part not found")
	}
}

func acroLocalFile(w http.ResponseWriter, r *http.Request) bool {
	urlPath := strings.Split(strings.ToLower(r.URL.Path), "?")[0]
	if value, found := acroStaticFiles[urlPath]; found {
		if len(value) == 0 {
			value = urlPath
		}

		localPath := path.Join(getConfig("acro.files", "/."), value)
		log.Printf("serving static file %v", localPath)
		http.ServeFile(w, r, localPath)

		return true
	}

	if strings.HasPrefix(urlPath, acroInnerPrefix) {
		hash := strings.TrimPrefix(urlPath, acroInnerPrefix)
		if value, found := acroLinksMap.Load(hash); found {
			path := value.(string)
			http.ServeFile(w, r, path)
		} else {
			fmt.Fprintf(w, "not found")
			w.WriteHeader(http.StatusNotFound)
		}
		return true
	}

	return false
}

/*

  <tr>
	<td class="label">Go</td>
    <td class="content">
      <ul>
        <li>Statically typed, compiled language</li>
        <li>Built-in concurrency with goroutines and channels</li>
        <li>Fast build times and single binary output</li>
      </ul>
    </td>
  </tr>


*/

const (
	acroTableEntry = `  <tr>
	<td class="label">%s</td>
    <td class="content">
      %s
    </td>
  </tr>
`
	acroTableHeader = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Table</title>
<link rel="stylesheet" href="style.css">
</head>
<body>
<table>
`
	acroTableFooter = `</table>
</body>
</html>
`
)

func printEntry(media io.Writer, name string, values []string) {
	insert := ""
	if len(values) > 1 {
		insert = "<ul>"
		for _, value := range values {

			text := value
			if m := acroLinkMatch.FindStringSubmatch(value); m != nil {
				linkText := m[1]
				url := convertUrl(m[2])
				text = fmt.Sprintf(`<a href="%s">%s</a>`, url, linkText)
			}

			insert += fmt.Sprintf("<li>%s</li>\n", text)
		}
		insert += "</ul>\n"
	} else {
		insert = fmt.Sprintf("%s", values[0])
	}

	fmt.Fprintf(media, acroTableEntry, name, insert)
}

func isLocalFilePath(url string) bool {
	return strings.HasPrefix(url, "/")
}

func getHash(from string) string {
	h := sha1.New()
	h.Write([]byte(from))
	return hex.EncodeToString(h.Sum(nil))
}

func convertUrl(url string) string {
	if isLocalFilePath(url) {
		hash := getHash(url)
		if _, ok := acroLinksMap.Load(hash); ok {
			return acroInnerPrefix + hash
		}

		acroLinksMap.Store(hash, url)
		return acroInnerPrefix + hash
	}
	return url
}

/*

acroLinkMatch := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
m := acroLinkMatch.FindStringSubmatch(s)
if m != nil {
    linkText := m[1]
    url := m[2]
}


*/
