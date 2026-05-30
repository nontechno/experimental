# downloader

downloads (specified) files from given reference page.

## usage
```
./page.download URL [pattern]
```

## example
```aiignore
 ./page.download https://go.dev/dl/ "go*.25.*windows*amd64*.msi"

```
this will download all windows *.msi files for version 25.x of amd-64 flavor