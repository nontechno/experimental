# slack.check

Tiny Go CLI that verifies:

1. **Network reachability** — raw TCP+TLS handshake to `slack.com:443`
   (checks DNS, routing, firewall/proxy, and TLS independent of any API call).
2. **Token validity** — calls Slack's `auth.test` endpoint to confirm a
   token authenticates and shows which workspace/user it belongs to.

No external dependencies — standard library only.

## Setup

1. Make sure Go is installed (1.21+): `go version`
2. From this directory:

   ```
   go build -o slack.check .
   ```

3. Get a Slack token you're authorized to use:
   - Bot token (`xoxb-...`): from a Slack App's **OAuth & Permissions** page,
     after installing the app to your workspace.
   - User token (`xoxp-...`): same page, under "User OAuth Token", if you
     need to test as a user rather than a bot.

   The token needs no special scopes to run `auth.test` — any valid token works.

## Running it

Reachability-only check (no token needed):

```
./slack.check
```

With a token, via environment variable (recommended — keeps it out of shell history/process list):

```
export SLACK_TOKEN=xoxb-your-token-here
./slack.check
```

Or via flag:

```
./slack.check -token=xoxb-your-token-here
```

Custom timeout (default 10s):

```
./slack.check -timeout=5s
```

## Expected output

```
== Slack connectivity check ==
[ OK ] TCP+TLS connection to slack.com:443 established

== Slack auth check (auth.test) ==
[ OK ] token is valid
       team:    Acme Corp (T0123ABC)
       user:    yourbot (U0456DEF)
       bot_id:  B0789GHI
```

Exit code is `0` on success, `1` if either check fails (network unreachable,
invalid/expired token, revoked token, etc.), with a `[FAIL]` line explaining why.

## Notes

- This only calls the read-only `auth.test` endpoint — it doesn't post
  messages, read channels, or touch any workspace data.
- If reachability fails but you have working internet otherwise, check for
  a corporate proxy/firewall blocking `slack.com`, or DNS issues.
- If auth fails with `invalid_auth` or `token_revoked`, regenerate the token
  from your Slack App's settings.
