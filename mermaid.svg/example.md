# System Design Overview

This document describes the request lifecycle.

## Components

| Component | Role | Language |
|-----------|------|----------|
| API Gateway | TLS termination, routing | Go |
| Auth Service | JWT validation | Go |
| Backend | Business logic | Go |
| Database | Persistence | PostgreSQL |

## Request Flow

```mermaid
sequenceDiagram
    participant C as Client
    participant G as API Gateway
    participant A as Auth Service
    participant B as Backend
    participant D as Database

    C->>G: HTTPS Request + JWT
    G->>A: Validate token
    A-->>G: 200 OK, claims
    G->>B: Forward request + claims
    B->>D: Query
    D-->>B: Rows
    B-->>G: JSON response
    G-->>C: 200 OK
```

## Deployment Topology

```mermaid
graph TD
    LB[Load Balancer] --> GW1[Gateway Pod 1]
    LB --> GW2[Gateway Pod 2]
    GW1 --> AUTH[Auth Service]
    GW2 --> AUTH
    GW1 --> BE[Backend Service]
    GW2 --> BE
    BE --> PG[(PostgreSQL Primary)]
    PG --> REPLICA[(Read Replica)]
```

## Error Rates

| Endpoint | p50 | p99 | Error % |
|----------|-----|-----|---------|
| `/api/users` | 4ms | 18ms | 0.01% |
| `/api/orders` | 12ms | 95ms | 0.03% |
| `/api/search` | 28ms | 210ms | 0.08% |
