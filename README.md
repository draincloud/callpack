# Callpack

Modules that I constantly implement in my projects: and which I decided to collect in one place for easy reuse! 

| Module | Import path | What it does |
| --- | --- | --- |
| `caller` | `github.com/draincloud/callpack/caller` | `http.Client` fancy wrapper with a round-tripper middleware chain: headers, logging, etc |
| `registry` | `github.com/draincloud/callpack/registry` | Registers a service instance in consul and heartbeats its TTL check. |
| `postgres` | `github.com/draincloud/callpack/postgres` | Easy to use postgres wrapper |

Each is tagged with its own prefix — `caller/v0.1.0`, `registry/v0.1.0`, etc...

`integration/` holds the tests that exercise the two against each other. 