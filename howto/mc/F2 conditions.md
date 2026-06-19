The F2 user menu in Midnight Commander uses **condition lines** starting with `+` or `-` to control when menu entries are visible/enabled.

## Syntax

```
+ <condition>
```
or
```
- <condition>
```

`+` means "show if condition is **true**", `-` means "show if condition is **false**".

## Condition Characters

The condition is a sequence of letter pairs. Each pair is: **a flag letter** + **a match character**.

**Flag letters** (what to test):

| Letter | Meaning |
|--------|---------|
| `f` | current file (cursor) |
| `t` | tagged files |
| `F` | file in other panel |
| `T` | tagged files in other panel |
| `d` | current directory |
| `D` | other panel directory |
| `x` | file is executable |

**Match characters** (what condition to check):

| Char | Meaning |
|------|---------|
| `r` | is readable |
| `w` | is writable |
| `x` | is executable |
| `d` | is a directory |
| `l` | is a symlink |
| `t` | is tagged (has selections) |
| `!` | negate the next test |

## Your Example: `+ t t`

```
+ t t
```

Means: **show this entry only if there are tagged files** in the current panel.

Breaking it down: `t` (tagged files) + `t` (are tagged/exist) → "tagged files exist".

## More Examples

```
+ f r       # show if current file is readable
+ f x       # show if current file is executable
+ f !d      # show if current file is NOT a directory
+ t t       # show if any files are tagged
- t t       # hide if files are tagged (only show for single file)
+ f r f w   # show if current file is both readable AND writable
```

## Full Menu Entry Structure

```
+ t t
Y   Process tagged files
    for f in %t; do something "$f"; done
```

The condition line must immediately precede the menu entry label. If the condition is false, the entry is either hidden or greyed out (depending on MC version/config).

You can also chain multiple conditions — they're ANDed together.
