## Midnight Commander Configuration

### Config file locations

MC looks in these places (in order):
- `~/.config/mc/` — user config (preferred)
- `~/.mc/` — legacy fallback
- `/usr/local/etc/mc/` or `/etc/mc/` — system defaults (read-only reference)

---

### `mc.ini` — main settings

`~/.config/mc/mc.ini`

Controls UI behavior, panels, editor settings:

```ini
[Midnight-Commander]
skin=default
use_internal_view=true
use_internal_edit=true
auto_save_setup=true
verbose=true
mouse_move_pages=true

[Layout]
menubar_visible=true
command_prompt=true
keybar_visible=true
message_visible=true

[Panels]
show_dot_files=true
show_backups=true
navigate_with_arrows=true
```

Edit interactively via **F9 → Options → Configuration**.

---

### `mc.ext` — file associations (F3/F4 behavior)

`~/.config/mc/mc.ext`

This is the main customization file. Controls what happens when you press F3 (view) or Enter/F4 (edit/open) on a file.

**Syntax:**
```
# comment
regex/PATTERN
    open=COMMAND %f
    view=COMMAND %f
    edit=COMMAND %f
```

- `open` — Enter or F3
- `view` — F3 (internal viewer unless overridden)
- `edit` — F4

**Match types:**
```
regex/\.go$              # by extension (regex)
type/^directory          # by file type
shell/.tar.gz            # exact suffix match (faster)
```

**Macros:**
| Macro | Meaning |
|-------|---------|
| `%f`  | selected filename |
| `%d`  | current directory |
| `%p`  | full path (`%d/%f`) |
| `%s`  | selected files (multiple) |
| `%u`  | first selected file, then deselect |
| `%cd` | cd to path (MC internal) |

**Example `mc.ext`:**
```
shell/.tar.gz
    open=tar xzf %f
    view=tar tzf %f

shell/.tar.bz2
    open=tar xjf %f
    view=tar tjf %f

regex/\.(jpg|jpeg|png|gif|bmp)$
    open=open -a "Preview" %f
    view=open -a "Preview" %f

regex/\.pdf$
    open=open -a "Preview" %f
    view=open -a "Preview" %f

regex/\.(mp4|mov|mkv|avi)$
    open=open -a "IINA" %f

regex/\.(go|py|rs|c|cpp|h|js|ts|json|yaml|toml)$
    open=code %f
    edit=code %f

# fallback for everything
default/
    open=open %f
```

Start from the system default:
```bash
cp /usr/local/etc/mc/mc.ext ~/.config/mc/mc.ext
```

---

### `menu` — user menu (F2)

`~/.config/mc/menu`

Adds custom commands to the F2 menu. Each entry has a hotkey letter.

**Syntax:**
```
H  Entry label
    command %f
```

**Example:**
```
G  Open in VSCode
    code %f

A  Archive selected files
    tar czf archive.tar.gz %s

C  Copy path to clipboard
    echo -n %p | pbcopy

D  Diff with other panel
    diff %f %D/%f | less
```

`%D` = other panel's directory, `%F` = other panel's selected file.

MC also has a **directory-local menu**: if a file named `menu` exists in the current directory, F2 shows that instead of the user menu.

---

### `mc.keymap` — key bindings

`~/.config/mc/mc.keymap`

Remaps keys to MC's built-in named actions. You **cannot** bind arbitrary shell commands here — only MC action names.

**Sections:**
- `[main]` — file panels
- `[editor]` — internal editor
- `[viewer]` — internal viewer

**Example:**
```ini
[main]
PanelSwitch=tab
Help=f1
Menu=f2
View=f3
Edit=f4
Copy=f5
Move=f6
MkDir=f7
Delete=f8
Quit=f10

[editor]
Save=ctrl-s
Quit=ctrl-q
Search=ctrl-f
```

View all available action names:
```bash
mc --configure-options  # not always available
```
Or check the system keymap for reference:
```bash
cat /usr/local/etc/mc/mc.keymap
```

---

### `ini` (editor) — internal editor settings

`~/.config/mc/ini`

If you use MC's built-in editor (mcedit), configure it here or via **F9 → Options → Editor**:

```ini
[editor]
tab_size=4
expand_tabs=true
show_line_state=true
syntax_highlighting=true
wrap_mode=0
```

---

### `skins/` — appearance

`~/.config/mc/skins/`

Copy a system skin and modify:
```bash
ls /usr/local/share/mc/skins/
cp /usr/local/share/mc/skins/default.ini ~/.config/mc/skins/mytheme.ini
```

Select via **F9 → Options → Appearance**, or set in `mc.ini`:
```ini
[Midnight-Commander]
skin=mytheme
```

---

### `~/.config/mc/panels.ini`

Stores panel state (sort order, listing mode, current directory). Usually auto-managed by MC — editing manually is rarely needed.

---

### Summary

| File | Purpose | Edit how |
|------|---------|----------|
| `mc.ini` | General settings | F9 menus or direct edit |
| `mc.ext` | File associations, F3/F4 behavior | Direct edit |
| `menu` | Custom F2 user menu commands | Direct edit |
| `mc.keymap` | Key → action bindings | Direct edit |
| `ini` | Internal editor settings | F9 → Editor or direct edit |
| `skins/*.ini` | Color themes | Direct edit |
| `panels.ini` | Panel state | Auto-managed |

The two files you'll actually customize most: **`mc.ext`** and **`menu`**.
