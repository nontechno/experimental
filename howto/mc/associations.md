In MC, file associations are configured in `~/.config/mc/mc.ext` (or `~/.mc/mc.ext` on older versions).

**Format:**
```
# Match by extension
regex/\.go$
    open=open -a "Visual Studio Code" %f
    view=open -a "Visual Studio Code" %f
```

- `open` = triggered by F3 (view)
- `edit` = triggered by F4 (edit)

**Example `mc.ext` entries:**
```
regex/\.(png|jpg|jpeg|gif)$
    open=open -a "Preview" %f

regex/\.pdf$
    open=open -a "Preview" %f

regex/\.go$
    open=open -a "Visual Studio Code" %f

regex/\.(mp4|mov|avi)$
    open=open -a "IINA" %f
```

**Steps:**
1. `mkdir -p ~/.config/mc`
2. Edit `~/.config/mc/mc.ext`
3. Add your rules — MC reads them top-to-bottom, first match wins
4. Restart MC

**Note:** MC ships with a default `mc.ext` (usually in `/usr/local/etc/mc/` or `/etc/mc/`). You can copy it as a starting point:
```bash
cp /usr/local/etc/mc/mc.ext ~/.config/mc/mc.ext
```

# selected file?

MC exposes the selected file path via `%f` (and related macros) in commands you define. But the mechanism depends on where you configure the binding:

**In `mc.ext`** — `%f` is the selected file:
```
regex/\.go$
    open=your-script %f
```

**In user menu (`~/.config/mc/menu`)** — same `%f` macro:
```
G  Open in my app
    your-script %f
```
Opened with F2.

**In `~/.config/mc/mc.keymap`** — you can bind a key, but keymap entries only trigger named MC actions, not arbitrary shell commands. So you can't directly bind a key to `your-script %f` this way.

**Practical approach** for a custom key → external app:
Use the **user menu (F2)**, assign a hotkey letter, and call your script with `%f`. It's one extra keypress but fully generic.

**Available path macros:**
- `%f` — selected file name (relative)
- `%d` — current directory
- `%d/%f` — full path (or use `%p` in some versions... verify on your MC version)

So the answer is yes — `%f` gives you the selected file whenever MC launches an external command.

