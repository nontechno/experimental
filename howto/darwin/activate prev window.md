Q: Is there a shell command (or any other way) to activate window that is next to the current one in a-order, without activations other windows to that app?

You're asking for something quite specific: cycle through windows of **all apps** in some order, activating the "next" one — without bringing other windows of the same app into focus.

This is tricky on macOS because the native window management doesn't expose a clean "next window globally" concept. But here's what works:

---

## Option 1: `switcher` (JXA/AppleScript)

You can write a JXA script that:
1. Gets all windows across all apps (via Accessibility API)
2. Finds the current frontmost window
3. Activates the next one

```javascript
// next-window.js — run with: osascript -l JavaScript next-window.js
ObjC.import('AppKit');

const se = Application('System Events');
const allProcs = se.processes.whose({ backgroundOnly: false })();

let windows = [];
for (const proc of allProcs) {
  try {
    for (const win of proc.windows()) {
      windows.push({ proc, win, name: proc.name(), title: win.title() });
    }
  } catch (_) {}
}

// Sort by title or some stable order
windows.sort((a, b) => a.title.localeCompare(b.title));

const front = se.processes.whose({ frontmost: true })[0];
const frontTitle = front.windows[0].title();

const idx = windows.findIndex(w => w.title === frontTitle);
const next = windows[(idx + 1) % windows.length];

next.proc.frontmost = true;
next.win.actions['AXRaise'].perform();
```

Run it via Karabiner:
```json
"shell_command": "osascript -l JavaScript /usr/local/bin/next-window.js"
```

---

## Option 2: `yabai`

If you use or are open to **yabai** (tiling WM), it has first-class support for this:

```bash
yabai -m window --focus next
```

It cycles windows in a stable spatial/insertion order and focuses exactly one window without touching others. Works great as a Karabiner target.

---

## Option 3: `Contexts` app

The app [Contexts](https://contexts.co/) is specifically designed for window-centric (not app-centric) switching and has hotkey support. It maintains a sorted/ordered list of all windows. Not scriptable but very polished.

---

## Recommendation

If you want **no extra tools**, go with the JXA script — it's self-contained and Karabiner can call it directly. If you're open to a lightweight daemon, **yabai** is the cleanest solution with the most predictable behavior.

The JXA approach has one rough edge: the Accessibility API requires **accessibility permissions** for `osascript` / Terminal (or whichever process Karabiner shells out through). You'll need to grant that in System Settings → Privacy & Security → Accessibility.
