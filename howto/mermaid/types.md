Here are rich examples of each Mermaid type:

---

## Flowchart
```mermaid
flowchart TD
    A([Start]) --> B{Auth token present?}
    B -- No --> C[Redirect to login]
    C --> D[User submits credentials]
    D --> E{Valid?}
    E -- No --> F[Show error] --> D
    E -- Yes --> G[Issue JWT]
    B -- Yes --> H{Token expired?}
    H -- Yes --> I[Refresh token]
    I --> J{Refresh valid?}
    J -- No --> C
    J -- Yes --> G
    H -- No --> G
    G --> K([Proceed to app])
```

---

## Sequence Diagram
```mermaid
sequenceDiagram
    actor User
    participant API
    participant Auth
    participant DB

    User->>API: POST /login {user, pass}
    API->>Auth: ValidateCredentials(user, pass)
    Auth->>DB: SELECT * FROM users WHERE email=?
    DB-->>Auth: UserRecord
    Auth-->>API: {valid: true, userID: 42}
    API->>Auth: GenerateJWT(userID=42)
    Auth-->>API: signed_token
    API-->>User: 200 OK {token}

    User->>API: GET /data (Bearer token)
    API->>Auth: VerifyJWT(token)
    alt Token valid
        Auth-->>API: {userID: 42}
        API->>DB: SELECT * FROM data WHERE user_id=42
        DB-->>API: rows
        API-->>User: 200 OK {data}
    else Token expired
        Auth-->>API: ErrExpired
        API-->>User: 401 Unauthorized
    end
```

---

## Class Diagram
```mermaid
classDiagram
    class Animal {
        +String name
        +int age
        +speak() string
        +move() void
    }
    class Dog {
        +String breed
        +speak() string
        +fetch() void
    }
    class Cat {
        +bool indoor
        +speak() string
        +purr() void
    }
    class Owner {
        +String name
        +List~Animal~ pets
        +adopt(Animal) void
        +release(Animal) void
    }

    Animal <|-- Dog
    Animal <|-- Cat
    Owner "1" --> "0..*" Animal : owns
```

---

## State Diagram
```mermaid
stateDiagram-v2
    [*] --> Idle

    Idle --> Connecting : connect()
    Connecting --> Connected : handshake OK
    Connecting --> Error : timeout

    Connected --> Authenticated : auth success
    Connected --> Error : auth fail

    Authenticated --> Processing : request received
    Processing --> Authenticated : response sent
    Processing --> Error : handler panic

    Error --> Idle : reset()
    Authenticated --> Idle : disconnect()

    state Processing {
        [*] --> Parsing
        Parsing --> Executing
        Executing --> Serializing
        Serializing --> [*]
    }
```

---

## Entity Relationship
```mermaid
erDiagram
    USER {
        uuid id PK
        string email UK
        string password_hash
        timestamp created_at
    }
    ORGANIZATION {
        uuid id PK
        string name
        string plan
    }
    PROJECT {
        uuid id PK
        uuid org_id FK
        string name
        string status
    }
    TASK {
        uuid id PK
        uuid project_id FK
        uuid assignee_id FK
        string title
        string priority
        timestamp due_at
    }
    COMMENT {
        uuid id PK
        uuid task_id FK
        uuid author_id FK
        text body
        timestamp created_at
    }

    USER }o--o{ ORGANIZATION : "member of"
    ORGANIZATION ||--o{ PROJECT : owns
    PROJECT ||--o{ TASK : contains
    USER ||--o{ TASK : "assigned to"
    TASK ||--o{ COMMENT : has
    USER ||--o{ COMMENT : authors
```

---

## Git Graph
```mermaid
gitGraph
    commit id: "init"
    commit id: "add auth module"

    branch feature/oauth
    checkout feature/oauth
    commit id: "add google provider"
    commit id: "add github provider"
    commit id: "token refresh logic"

    branch bugfix/token-expiry
    checkout bugfix/token-expiry
    commit id: "fix off-by-one in exp check"
    checkout feature/oauth
    merge bugfix/token-expiry

    checkout main
    branch release/v1.2
    checkout release/v1.2
    commit id: "bump version"
    merge feature/oauth id: "merge oauth"
    commit id: "release notes"
    checkout main
    merge release/v1.2 tag: "v1.2.0"
```

---

## Gantt
```mermaid
gantt
    title Backend Service — Q3 Roadmap
    dateFormat YYYY-MM-DD
    excludes weekends

    section Infrastructure
    Provision OCI instances     :done,    inf1, 2024-07-01, 5d
    Configure VCN & networking  :done,    inf2, after inf1, 3d
    Set up bastion & IAM        :active,  inf3, after inf2, 4d

    section Auth Service
    Design JWT flow             :done,    auth1, 2024-07-01, 3d
    Implement token endpoints   :active,  auth2, after auth1, 6d
    Integration tests           :         auth3, after auth2, 4d

    section API Gateway
    Route configuration         :         gw1, after inf3, 5d
    Rate limiting middleware    :         gw2, after gw1, 3d
    Load testing                :crit,    gw3, after gw2, 4d
```

---

## C4 Diagram
```mermaid
C4Context
    title System Context — Payment Platform

    Person(customer, "Customer", "Makes purchases")
    Person(admin, "Admin", "Manages platform")

    System(payment, "Payment Platform", "Handles checkout, billing, refunds")

    System_Ext(stripe, "Stripe", "Payment processing")
    System_Ext(bank, "Banking Network", "Settlement")
    System_Ext(email, "Email Service", "Transactional emails")
    System_Ext(fraud, "Fraud Detection API", "Risk scoring")

    Rel(customer, payment, "Checkout, view history")
    Rel(admin, payment, "Manage refunds, reports")
    Rel(payment, stripe, "Charge cards")
    Rel(payment, fraud, "Score transactions")
    Rel(payment, email, "Send receipts")
    Rel(stripe, bank, "Settle funds")
```

---

## Pie Chart
```mermaid
pie title API Error Distribution (last 30d)
    "5xx Server Error"   : 42
    "4xx Client Error"   : 31
    "Timeout"            : 15
    "Auth Failure"       : 8
    "Rate Limited"       : 4
```

---

## Timeline
```mermaid
timeline
    title Linux Kernel Milestones
    1991 : First release (0.01)
         : Linus posts to comp.os.minix
    1994 : Kernel 1.0 released
    1996 : Kernel 2.0 — SMP support
    2003 : Kernel 2.6 — major scheduler rewrite
    2011 : Kernel 3.0
    2015 : Kernel 4.0 — live patching
    2019 : Kernel 5.0
    2022 : Kernel 6.0 — new arch support
```

---

## Mindmap
```mermaid
mindmap
  root((Distributed Systems))
    Consistency
      Strong
      Eventual
      Causal
    Consensus
      Raft
      Paxos
      PBFT
    Networking
      gRPC
      REST
      Message Queues
        Kafka
        NATS
        RabbitMQ
    Observability
      Metrics
        Prometheus
      Tracing
        Jaeger
        Tempo
      Logging
        Loki
        ELK
    Storage
      SQL
        PostgreSQL
        CockroachDB
      NoSQL
        Redis
        Cassandra
```

---

## User Journey
```mermaid
journey
    title Developer Onboarding Flow
    section Discovery
      Find docs site:        5: Dev
      Read quickstart:       4: Dev
      Clone example repo:    4: Dev
    section Setup
      Install CLI:           3: Dev
      Configure credentials: 2: Dev, Support
      Run hello world:       4: Dev
    section First Integration
      Write first API call:  4: Dev
      Hit rate limit:        1: Dev
      Read rate limit docs:  3: Dev, Support
      Successful response:   5: Dev
```

---

## XY Chart
```mermaid
xychart-beta
    title "Monthly API Requests (millions)"
    x-axis [Jan, Feb, Mar, Apr, May, Jun, Jul, Aug, Sep, Oct, Nov, Dec]
    y-axis "Requests (M)" 0 --> 120
    bar  [20, 25, 30, 42, 55, 61, 70, 68, 80, 95, 105, 118]
    line [20, 25, 30, 42, 55, 61, 70, 68, 80, 95, 105, 118]
```

---

## Quadrant Chart
```mermaid
quadrantChart
    title Tech Debt vs Business Value
    x-axis Low Effort --> High Effort
    y-axis Low Value --> High Value
    quadrant-1 Do First
    quadrant-2 Plan Carefully
    quadrant-3 Reconsider
    quadrant-4 Quick Wins

    Auth refactor: [0.3, 0.8]
    DB migration: [0.8, 0.9]
    CSS cleanup: [0.2, 0.2]
    API versioning: [0.6, 0.85]
    Log rotation fix: [0.15, 0.5]
    Rewrite in Rust: [0.95, 0.4]
    Add metrics: [0.4, 0.75]
```

---

## Sankey
```mermaid
sankey-beta
    source,target,value
    Traffic,Direct,40
    Traffic,Search,35
    Traffic,Social,25
    Direct,Signup,20
    Direct,Bounce,20
    Search,Signup,25
    Search,Bounce,10
    Social,Signup,10
    Social,Bounce,15
    Signup,Paid,30
    Signup,Free,25
```

---

## Block Diagram
```mermaid
block-beta
    columns 3

    Client["Browser / Mobile"]:1
    space:1
    CDN["CDN Edge"]:1

    space:3

    LB["Load Balancer"]:1
    space:1
    Auth["Auth Service"]:1

    space:3

    API1["API Pod 1"]:1
    API2["API Pod 2"]:1
    API3["API Pod 3"]:1

    space:3

    Cache["Redis Cache"]:1
    space:1
    DB["PostgreSQL"]:1

    Client --> CDN
    CDN --> LB
    LB --> Auth
    LB --> API1
    LB --> API2
    LB --> API3
    API1 --> Cache
    API2 --> Cache
    API3 --> Cache
    Cache --> DB
```

---

That covers all major types. The ones most useful for systems/infra work (your domain) are **Flowchart**, **Sequence**, **ER**, **State**, **C4**, and **Git Graph**.