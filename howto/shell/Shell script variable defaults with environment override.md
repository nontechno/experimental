```bash
#!/bin/bash

DEFAULT_VALUE="default_setting"
MY_VAR="${MY_ENV_VAR:-$DEFAULT_VALUE}"

echo "Using: $MY_VAR"
```

`MY_VAR` gets `MY_ENV_VAR` if it's set and non-empty, otherwise falls back to `DEFAULT_VALUE`.

If you want to allow an empty string to override the default, use `${MY_ENV_VAR-$DEFAULT_VALUE}` (no colon) — that only falls back if the variable is **unset**, not if it's empty.
