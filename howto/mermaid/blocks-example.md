Here's a single diagram exercising each construct:

```mermaid
sequenceDiagram
    box Purple Client Tier
        participant C as Client
        participant W as Worker
    end
    participant S as Server
    participant DB as Database

    rect rgb(240, 240, 255)
    Note over C,S: Initial request
    C->>S: GET /dashboard
    end

    opt is authenticated
        S->>S: validate session token
    end

    alt cache hit
        S-->>C: 200 OK (cached)
    else cache miss
        S->>DB: query data
        DB-->>S: rows
    end

    par fetch user profile
        S->>DB: SELECT user
    and fetch notifications
        S->>DB: SELECT notifications
    and fetch settings
        S->>W: compute settings
    end

    critical establish DB connection
        S->>DB: connect
    option network timeout
        S->>S: retry with backoff
    option auth failure
        S-->>C: 401 Unauthorized
    end

    loop every 30 seconds
        W->>S: heartbeat ping
    end

    break invalid payload
        C->>S: malformed request
        S-->>C: 400 Bad Request
    end

    Note right of DB: All queries logged<br/>for audit trail
```

Quick notes on what's demonstrated:

- **`box`** groups Client + Worker visually as one tier
- **`rect`** shades the initial request/response pair
- **`opt`** — single-branch conditional (session check)
- **`alt`/`else`** — cache hit vs miss branching
- **`par`** — three concurrent fetches with `and`
- **`critical`/`option`** — a must-run block with two fallback paths
- **`loop`** — recurring heartbeat
- **`break`** — abnormal exit path (renders with a distinct highlighted style, usually red-tinted, to signal "flow stops here")
- **`Note`** — free-form annotation, no `end` needed for single-participant notes

If you paste this into a Mermaid live editor or a renderer that supports it, you'll see each block's visual treatment differs (dashed vs solid borders, background tint for `rect`, etc.) — useful for eyeballing which construct you're looking at in someone else's diagram.