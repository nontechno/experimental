These are combined-fragment blocks in Mermaid sequence diagrams — control-flow wrappers around a group of messages, borrowed from UML sequence diagram notation. Each one draws a labeled box around the messages inside it.

**`loop`** — repeated messages
```
loop every 5 seconds
    Client->>Server: poll status
end
```

**`opt`** — optional, happens only if a condition holds (single-branch, no else)
```
opt is authenticated
    Client->>Server: fetch profile
end
```

**`alt` / `else`** — if/else branching, any number of branches
```
alt success
    Server-->>Client: 200 OK
else failure
    Server-->>Client: 500 Error
end
```

**`par`** — parallel messages, happening concurrently; use `and` to add more concurrent branches
```
par fetch user
    Client->>UserSvc: GET /user
and fetch orders
    Client->>OrderSvc: GET /orders
end
```

**`critical`** — one path that must run, with optional `option` fallbacks (useful for things like retry/failure handling)
```
critical connect to DB
    Service->>DB: connect
option network timeout
    Service->>Service: retry
end
```

**`break`** — like opt, but signals the flow halts/errors out if the condition is true (renders with a distinct style to show it's an abnormal exit)
```
break invalid input
    Server-->>Client: 400 Bad Request
end
```

A few other block-like constructs that aren't conditionals but follow the same `... end` pattern:

- **`rect`** — background-colored box around messages, purely visual grouping (e.g. `rect rgb(200, 220, 255)`)
- **`box`** — groups participants (not messages) under a shared label/color
- **`note over/left of/right of`** — annotation text, not a block with `end` unless spanning multiple participants

All of these nest, so you can put a `loop` inside an `alt` branch, etc. One gotcha: participant names inside labels can't contain colons or they'll break parsing.
