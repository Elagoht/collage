# newsroom-api

A fake magazine backend: a read-only JSON API over an embedded corpus of twenty
articles, five sections and six writers.

**This is not a collage program.** It imports only the standard library, and it has
no idea what renders its JSON. It exists so [`../magazine`](../magazine) has a real
backend to talk to — one that answers over HTTP, can be slow, and can fail — rather
than an in-process slice that never does any of those things.

```
go run .                  # serves on localhost:8080
go run . -addr :9000      # somewhere else
go test ./...
```

## Endpoints

| | |
|---|---|
| `GET /v1/articles?category=&author=&q=&page=&per_page=` | one page of a listing |
| `GET /v1/articles/{slug}` | one article, or 404 |
| `GET /v1/categories`, `GET /v1/categories/{slug}` | sections |
| `GET /v1/authors`, `GET /v1/authors/{slug}` | writers |
| `GET /v1/popular?limit=` | most-read, ranked |
| `GET /healthz` | liveness |

Every listing endpoint answers the same shape, so a client needs one decode path
for the front page, the sections, the writers and search.

## Failure injection

```
go run . -latency 800ms    # every response is slow
go run . -fail-every 3     # every third request answers 503
```

Without these, a consumer's degraded paths are code that compiles and has never
run. The schedule is deterministic rather than probabilistic: a probability would
be more lifelike and would make every test that touches it flaky.

`/healthz` is exempt, and does not advance the counter. A health endpoint the
injector can knock over stops reporting whether the process is alive; one that
advances the counter shifts the schedule the rest of the API sees.

A caller that retries will absorb a lot of this. With `-fail-every 2` against a
client that retries once, *nothing* fails: the retry always lands on a success.
That is the retry working, not the injection failing.

## Configuration

Flags, or the environment: `MAGAZINE_API_ADDR`, `MAGAZINE_API_LATENCY`,
`MAGAZINE_API_FAIL_EVERY`, `LOG_LEVEL`. Logs are JSON on stdout.

The corpus is `content.json`, embedded at build time. It is checked for referential
integrity at startup — an article naming a section or writer that does not exist is
a startup error, because the alternative is a landing page link that 404s and a
crawler report three weeks later.
