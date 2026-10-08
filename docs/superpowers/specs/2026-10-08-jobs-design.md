# Jobs: scheduled and background work that runs only while the application serves

Date: 2026-10-08
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 2, "jobs" ("are
`Init`/`Shutdown` enough, or is a 'server is listening' moment (`Start`) needed?
The background work's ctx and its cancellation").

## Motivation

Applications need periodic work, such as refreshing a cache, cleaning up
sessions, or pulling content. They also need work an action hands off and
returns from, such as sending an email, calling a webhook, or processing an
image. Today a plugin can only start such work in `Init`, and `Init` also runs
in `collage build`, in CLI commands, and in tests that call `Handler()` or
`Start()`. A scheduler started there runs during a static build. Nothing tells a
plugin that the server is actually serving, and nothing tells it to stop taking
new work before the drain.

## Decisions

- **Scope:** scheduled jobs (interval and cron) plus typed, bounded, in-memory
  background queues with retries. Persistence stays out, behind a `Store[T]`
  interface a later plugin can implement ("data/state out of scope").
- **Lifecycle:** the core gains one optional hook, `ServeHook`, called only when
  `ListenAndServe` serves. Its ctx is cancelled when the drain starts.
  `DrainHook` (v0.53.0) is the matching piece for the other end.
- **Plugin:** `elagoht/jobs`. Its `Shutdown` finishes in-flight and queued work
  within the shutdown ctx.

## Design

### 1. Core (collage v0.55.0)

```go
// ServeHook is implemented by a plugin that does work only while the
// application is serving — a scheduler, a queue worker. OnServe is called once,
// after ListenAndServe has bound its port and before it serves; ctx is
// cancelled when the drain starts. It must not block: start goroutines.
type ServeHook interface {
	OnServe(ctx context.Context)
}
```

- **When it is called:** `OnServe` is called once by `ListenAndServe`, in
  registration order, right after the port is bound. That is the moment
  `collage: listening` is logged.
- **When it is not called:**
  - in `collage build`;
  - in CLI commands;
  - by `Handler()` or `Start()`;
  - when `Shutdown` ran before `ListenAndServe`.
- **Panics:** a panic in `OnServe` is contained and logged with the plugin's
  name, the same way `OnDrain` panics are. The server still serves.
- **The ctx:** it derives from `context.Background()` and is cancelled when the
  drain starts. That is the same point `OnDrain` runs, whether the stop came
  from a signal or from `App.Shutdown(ctx)`. The ctx means "start no new work".
  Finishing the work already running is the plugin's own `Shutdown`, which runs
  after the server stops and is bounded by the shutdown ctx.
- **Development mode:** `collage dev` uses `ListenAndServe`, so scheduled work
  runs in development as well, and every restart sets it up again.
- **Applications that run their own `http.Server` over `app.Handler()`:** they do
  not get `OnServe`. The docs say so. Such an application starts the jobs
  plugin itself with `j.Start(ctx)`. No public "run the serve hooks" method is
  added to the core.

The stop sequence becomes:
1. drain starts (`OnDrain` runs, the `OnServe` ctx is cancelled);
2. `DrainDelay`;
3. streams;
4. `server.Shutdown`;
5. plugins' `Shutdown`.

### 2. Plugin `elagoht/jobs` (new repo Elagoht/collage-jobs, v0.1.0)

**Scheduled jobs**

```go
j := jobs.New()
j.Every("refresh-feed", 15*time.Minute, refreshFeed)  // func(ctx context.Context) error
j.Cron("cleanup", "0 3 * * *", cleanup)
```

`Every` and `Cron` return a handle with `.RunOnStart()` and `.Timeout(d)`.

- **When jobs run:** only after `OnServe`, or after `Start`. The first run is at
  the next trigger, or immediately if `RunOnStart` is set.
- **No overlap:** while a run is in progress, a trigger is skipped and logged at
  Debug.
- **Timeout:** `.Timeout(d)` bounds a run's ctx.
- **Cron syntax:** standard five fields (minute, hour, day of month, month, day
  of week) with `*`, lists, ranges and steps.
  - Day of month and day of week combine as in Vixie cron: if both are
    restricted, either one matching is enough.
  - The parser is our own, built on the stdlib only.
  - An invalid expression panics at `Cron`, naming the field.
- **Time zone:** cron expressions are read in the configured `timezone`, UTC by
  default. Across DST:
  - a time in the skipped hour runs once, at the first instant after the gap;
  - a time in the repeated hour runs once, on its first occurrence — for a schedule with a fixed hour. A schedule whose hour field starts with `*` (`*/15 * * * *`, `0 * * * *`) fires on every real occurrence, through both copies of the repeated hour, as Vixie cron does; otherwise a 15-minute job would leave a 75-minute hole.
- **Several instances:** every instance runs its own scheduled jobs. Three
  replicas run a job three times. Leader election is out of scope, and the
  README says so prominently.

**Background queues**

```go
mail := jobs.NewQueue(j, "email", sendEmail, jobs.QueueOptions{Workers: 2, MaxAttempts: 3})
// sendEmail: func(ctx context.Context, m Email) error
err := mail.Enqueue(ctx, Email{To: to})
```

`Queue[T]` is generic, so payloads are typed and `any` is never used.
`QueueOptions` holds:
- `Workers`, default 1;
- `Capacity`, default 1000;
- `MaxAttempts`, default 1;
- `OnFailure`, a `func(ctx, T, error)` called after the last failed attempt.

Behaviour:
- **When `Enqueue` works:**
  - Enqueue works before serving. Work waits in the queue until `OnServe` or
    `Start` starts the workers.
  - A full queue returns `ErrQueueFull`.
  - After the drain has started, `Enqueue` returns `ErrDraining`.
- **Failures:** an error or a panic counts as a failed attempt. Attempts are
  retried with exponential backoff (1s, 2s, 4s…, capped at 1 minute) up to
  `MaxAttempts`. The last failure is logged at Error and `OnFailure` is called.
- **Storage:** queued work lives behind
  `Store[T] interface { Push(T) error; Pop(ctx) (T, error); Len() int }`. The
  in-memory store is the only implementation.

**Lifecycle**

- `OnServe(ctx)` starts the scheduler and the workers. When ctx is cancelled
  (the drain), the scheduler stops triggering and `Enqueue` returns
  `ErrDraining`.
- `Start(ctx)` does the same for an application that serves by itself. A
  second `Start`, or `Start` after `OnServe`, is a no-op.
- `Shutdown(ctx)`:
  1. waits for running jobs and lets workers keep taking queued items until ctx
     ends;
  2. then cancels the jobs' ctx;
  3. logs at Warn, per queue, how many queued items were dropped (in-memory work
     is lost).

**Configuration** (`plugins-config.json`, read with `collage.PluginConfig`):

```json
{ "elagoht/jobs": { "timezone": "Europe/Istanbul", "disabled": ["cleanup"] } }
```

`disabled` turns off scheduled jobs by name without a code change.

**Errors**
- These panic, as programming errors:
  - a duplicate job or queue name;
  - an empty name;
  - a nil function;
  - defining a job or queue after `Init`;
  - a bad cron expression;
  - a negative `Workers`, `Capacity` or `MaxAttempts` in `NewQueue`.
- These are errors returned by `Init`:
  - an unknown name in `disabled`;
  - an invalid `timezone`.

**Observability:** each run is logged at Debug with its name and duration.
Failures are logged at Warn for an attempt that will be retried, and at Error
for the last one. Metrics are out of scope.

**Testing hook:** time is abstracted behind a small unexported clock
interface, so tests do not depend on wall-clock sleeps.

## Testing

**Core**
- `OnServe` runs once in `ListenAndServe`.
- It never runs in `Start`, `Handler`, a build, or when `Shutdown` comes first.
- Its ctx is cancelled at the drain, for both a signal and `Shutdown(ctx)`.
- A panic in it is contained and logged.

**Plugin**
- Cron parser tables: ranges, steps, lists, combined day-of-month and
  day-of-week, and DST gaps and overlaps in a real zone.
- Scheduling with a fake clock: `Every` and `Cron` trigger correctly, a trigger
  is skipped while the job is running, and `RunOnStart` and `Timeout` work.
- Nothing runs before `OnServe` or `Start`.
- `disabled` works.
- Queues:
  - `ErrQueueFull`;
  - `ErrDraining` after the drain;
  - retry with backoff;
  - `OnFailure` after the last attempt;
  - panics retried;
  - `Workers` concurrency.
- Shutdown: queued work finishes within ctx; when ctx ends, the jobs' ctx is
  cancelled and the dropped count is logged.
- A static build runs no job.
- End to end with a real server: `Shutdown(ctx)` stops the scheduler, and a
  queued item still finishes.
- `Init` validation.

## Release

1. collage **v0.55.0** (additive): `ServeHook`.
2. **Elagoht/collage-jobs v0.1.0**, then the CI-matrix commit and a matrix run
   from main (42 plugins).
3. Docs:
   - framework `docs/plugins.md` (`ServeHook`);
   - `docs/deployment.md` (the stop sequence, and `j.Start` when serving with
     your own server);
   - the docs site, EN and TR;
   - the extension's schema for `elagoht/jobs`.

## Out of scope

- A persistent queue (only the `Store[T]` interface).
- Leader election or locks across instances.
- Triggering a job by hand through the CLI or HTTP.
- Metrics.
- Priorities.
- Delayed enqueue (`EnqueueAt`).
- A public core method to run the serve hooks.
