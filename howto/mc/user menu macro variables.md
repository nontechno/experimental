Here are the main MC user menu macro variables:

**File/path related**
- `%f` — current file name
- `%d` — current directory (with trailing slash)
- `%p` — full path (`%d%f`)
- `%b` — basename (filename without extension)
- `%x` — extension

**Selection**
- `%t` — list of tagged (selected) files
- `%u` — tagged files, but untags them as they're used (destructive iteration)

**Panel-related (second panel)**
- `%F` — file in the other panel
- `%D` — directory of the other panel
- `%P` — full path in the other panel
- `%T` — tagged files in the other panel
- `%U` — tagged files in the other panel (untagging)

**Misc**
- `%s` — selected file (tagged files if any, otherwise current file)
- `%S` — same but for the other panel
- `%%` — literal `%`
- `%cd` — not a substitution, but tells MC to change directory to the command's output

**Input prompt**
- `%{prompt text}` — pops up an input dialog and substitutes whatever the user types

That last one is handy — e.g. `cp %p %{Destination:}` gives you an interactive copy with a custom destination prompt right in MC.
