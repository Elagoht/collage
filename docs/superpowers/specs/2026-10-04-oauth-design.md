# elagoht/oauth: sign in with an OpenID Connect provider, and call its APIs

Date: 2026-10-04
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 1, the "oauth"
item

## Motivation

The roadmap's question for this plugin is redirects. A sign-in flow takes a `next`
from the URL and sends the reader there afterwards. Today the core validates only
its own redirects (`unsafeRedirectReason` in the router). A Location from a guard
or an action, and any `next` a login handler reads, is the application's to check.
The roadmap carries this as an open decision.

The user wants sign-in and API access. A reader signs in with Google (or any
OpenID Connect provider), and the application can then call that provider's APIs
on the reader's behalf.

## Goals

- A plugin, `elagoht/oauth` (`github.com/Elagoht/collage-oauth`), that signs a reader in
  with any OpenID Connect provider, with one-line presets for Google, Microsoft
  and GitLab.
- The application owns its users. A callback maps the provider's identity to the
  application's user ID. The plugin then signs the reader in through
  elagoht/session, so existing `session.RequireUser` guards work unchanged.
- Optional API access:
  - tokens are sealed by the plugin and stored by the application through a small
    interface;
  - an `*http.Client` refreshes the token as needed.
- stdlib only (beyond collage and collage-session).
- Core, additive: `collage.SafeRedirect(next, fallback string) string`, the
  router's own redirect check plus a backslash and a cleaned-path rule, open to
  applications and plugins. The core's defaults do not change.

## Non-goals

- GitHub, which is not an OpenID Connect provider. A later preset can read
  `/user`.
- Account linking (adding a provider to an already signed-in account). A later
  feature.
- Verifying the id_token's signature (JWKS). The token comes straight from the
  token endpoint over TLS, which OIDC Core 3.1.3.7 allows in place of the
  signature check.
- A logout route. The application's own logout action calls
  `session.FromContext(ctx).Clear()`; `Revoke` is offered for the tokens.
- Coordinating token refresh across several instances. Concurrent refreshes are
  coalesced within one process only; this is documented.
- Retrying an API call that answered 401.

## Core (collage v0.44.0)

`collage.SafeRedirect(next, fallback string) string` returns `next` when it is a
path on this site and `fallback` otherwise.

- A path on this site starts with `/`, does not start with `//` or `/\`, and
  contains no control character. This is the router's `unsafeRedirectReason`,
  moved where both the router and `pkg/collage` can call it, and unchanged; the
  router itself keeps using it as it was.
- `SafeRedirect` adds two rules, because its output is handed to
  `http.Redirect`, which runs `path.Clean` on a rooted path: `/./\evil.com` and
  `/a/../\evil.com` pass the router's check but leave as `/\evil.com`, which a
  browser reads as `//evil.com`. So `SafeRedirect` also refuses a backslash
  anywhere (the query included; a legitimate `next` escapes it as `%5C`), and
  refuses a value whose part before `?` or `#`, once `path.Clean`ed, fails the
  router's check. With backslashes gone the second rule cannot fire on its own
  (a cleaned rooted path never starts with `//`); it stays as the statement of
  what `http.Redirect` will send. `/.//evil.com` cleans to `/evil.com` and is kept.
- An absolute URL is never accepted, not even one on this site's own origin. The
  rule stays one simple check.
- A `fallback` that is itself not a path on this site gives `"/"`.

Nothing else in the core changes: guard and action Locations stay unchecked,
because a plugin such as this one must send the reader to another site. The docs
change their login examples, and session's, to use `SafeRedirect`.

## The plugin

### Options

```go
const Name = "elagoht/oauth"

type Options struct {
	Providers  []Provider `json:"providers"`
	Prefix     string     `json:"prefix"`     // default "/auth"
	AfterLogin string     `json:"afterLogin"` // where a sign-in without next ends; default "/"
	ErrorPath  string     `json:"errorPath"`  // a page given ?error=<code>; empty = built-in status pages
	Key        []byte     `json:"-"`          // seals tokens; required with Store
	KeyHex     string     `json:"key"`
	PreviousKeys    [][]byte `json:"-"`        // still open what they sealed
	PreviousKeysHex []string `json:"previousKeys"`

	OnLogin    LoginFunc    `json:"-"` // required
	Store      TokenStore   `json:"-"` // optional; without it no token is kept
	HTTPClient *http.Client `json:"-"` // for the provider calls; default: 10s timeout
}

type Provider struct {
	Name            string   `json:"name"`            // URL segment: {prefix}/{name}/login
	Preset          string   `json:"preset"`          // "google" | "microsoft" | "gitlab"
	Issuer          string   `json:"issuer"`          // OIDC issuer; with a preset, overrides its issuer
	ClientID        string   `json:"clientID"`
	ClientSecretEnv string   `json:"clientSecretEnv"` // name of the environment variable
	ClientSecret    string   `json:"-"`               // or set from Go
	Scopes          []string `json:"scopes"`          // added to "openid email profile"
	Offline         bool     `json:"offline"`         // ask for a refresh token (offline_access without a preset)
}

type Identity struct {
	Provider      string // the Provider's Name
	Subject       string // the provider's stable ID for the reader ("sub")
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
}

type LoginFunc func(ctx context.Context, id Identity) (userID string, err error)

type TokenStore interface {
	Load(ctx context.Context, userID, provider string) ([]byte, error) // (nil, nil) when none
	Save(ctx context.Context, userID, provider string, sealed []byte) error
	Delete(ctx context.Context, userID, provider string) error
}

var ErrNotLinked = errors.New("oauth: no usable token for this user and provider")

func New(opts Options) *Plugin
func (p *Plugin) Client(ctx context.Context, userID, provider string) (*http.Client, error)
func (p *Plugin) Revoke(ctx context.Context, userID, provider string) error
```

Template function: `{{oauthLogin "google"}}` gives the login URL, with the
current page's path and query as `next`. `{{oauthLogin "google" "/after"}}`
names `next` explicitly.

Presets fill `Issuer` and the provider-specific parameters:

| Preset | Issuer | Offline |
|---|---|---|
| google | `https://accounts.google.com` | `access_type=offline&prompt=consent` |
| microsoft | `https://login.microsoftonline.com/common/v2.0`, with the `{tenantid}` issuer template accepted | `offline_access` scope |
| gitlab | `https://gitlab.com` | nothing added: GitLab issues a refresh token with every authorization-code exchange |
| none (an `Issuer`) | the configured one | `offline_access` scope (standard OIDC) |

A preset and an `Issuer` may both be set: the preset gives its parameters and the
`Issuer` replaces its issuer (one Microsoft tenant). The microsoft preset alone
(`common`) accepts any Entra tenant and any personal Microsoft account; the docs
say so, and that `EmailVerified` is false for Microsoft (no `email_verified`
claim).

### Routes

The plugin registers two GET routes with `host.Handle`:
`{prefix}/{name}/login` and `{prefix}/{name}/callback`.

- They run inside session's middleware.
- Without elagoht/session there is no session: the request is a 500, with one log
  line naming the missing plugin.

### Discovery

`{issuer}/.well-known/openid-configuration` is fetched on first use, not at start,
so a site still starts when a provider is unreachable.

- It is cached for one hour. A failed fetch is not cached.
- The document's `issuer` must equal the configured one. For the microsoft
  preset with no `Issuer` set it may instead match the preset's `{tenantid}`
  template; a configured `Issuer` is matched exactly.
- The issuer, and every endpoint the document names (authorization and token
  always, userinfo and revocation when present), must be `https`, or `http` only
  to `localhost` or a loopback IP literal. An issuer that is not fails the start;
  an endpoint that is not fails discovery, so the sign-in ends `unavailable`.

### Login

1. Validate `next`: `next = collage.SafeRedirect(r.URL.Query().Get("next"), AfterLogin)`.
2. Make `state`, `nonce` and a PKCE `verifier`, 32 random bytes each,
   base64url-encoded.
3. Store `{state, nonce, verifier, next, created}` in the session under
   `oauth:<name>`, as JSON without HTML escapes. There is one pending sign-in per
   provider; a new one replaces it. A `next` over 1024 bytes is dropped, and when
   the session still cannot hold the entry (`ErrTooLarge`) it is stored again
   with `next = AfterLogin`.
4. Build `redirect_uri` as `Origins.OriginFor(ctx, r.Host)` + `{prefix}/{name}/callback`.
   It follows the host, so on a multi-tenant site each host's callback must be
   registered with the provider. The docs say so.
5. Answer 303 to `authorization_endpoint` with these parameters:
   - `response_type=code`, `client_id`, `redirect_uri`;
   - `scope`: "openid email profile" plus the provider's `Scopes`, plus
     `offline_access` when `Offline` is set and the preset says so or there is
     no preset;
   - `state`, `nonce`;
   - `code_challenge=S256(verifier)` and `code_challenge_method=S256`;
   - the preset's offline parameters when `Offline` is set.

### Callback

Any failure ends the sign-in. No session is half-made, and no token, code or
secret is ever logged.

1. Read the pending sign-in from the session and delete it at once, so it is
   used once. If none is pending, fail with `state`. If it is older than 10
   minutes, fail with `expired`.
2. An `error` parameter fails with `denied`.
3. Compare `state` in constant time. A mismatch fails with `state`.
4. POST to `token_endpoint` with these fields:
   - `grant_type=authorization_code`, `code`, `redirect_uri`;
   - `code_verifier`;
   - the client's credentials: HTTP Basic when the discovery document lists
     `client_secret_basic` or lists nothing, form fields otherwise.

   The call has a 10s timeout and a 1 MiB response limit, and never follows a
   redirect (a 307/308 would re-send the client secret elsewhere); the
   revocation call likewise. A failed call fails with `exchange`.
5. Check the id_token's claims. Any failing check fails with `token`.
   - `iss` equals the discovery issuer (or matches the microsoft template when
     no `Issuer` is set).
   - `aud` contains the client ID, and `azp` equals it when there are several
     audiences.
   - `exp` is in the future, with 60s skew allowed.
   - `iat` is not more than 60s in the future.
   - `nonce` equals the pending nonce.
   - `sub` is non-empty.
6. If the id_token has no `email` and the provider has a `userinfo_endpoint`,
   fetch it with the access token. Its `sub` must equal the id_token's.
7. Call `userID, err := OnLogin(ctx, identity)`. An error or an empty userID fails
   with `rejected`.
8. Call `session.Regenerate()`, then `Set(session.UserKey, userID)`. When the
   session already belongs to a different user, it is cleared first, so nothing of
   theirs carries over.
9. With a `Store`, seal the tokens and `Save` them. A failed save is logged and the
   sign-in still completes; API access is set up again on the next sign-in.
   A sign-in that returns no refresh token keeps the one already stored.
10. Answer 303 to the pending `next`, checked again with `SafeRedirect`. Every
    303 the plugin sends is written as a raw `Location` header (bytes past ASCII
    percent-encoded), not through `http.Redirect`, which would clean the path.

The docs say to key accounts on `Provider + Subject`, never on the e-mail, and to
trust an e-mail only when `EmailVerified` is true.

### Tokens

**Sealed form:**

- One version byte, then AES-256-GCM: a 12-byte random nonce followed by the
  ciphertext.
- The key is `HMAC-SHA256(Key, "oauth-tokens")`.
- The additional data is `userID + "\x00" + provider`, so a blob moved to another
  row does not open.
- The plaintext is JSON: access token, refresh token, expiry and scopes.
- Opening tries `Key`, then each of `PreviousKeys`. A blob opened with a previous
  key is sealed again with `Key` the next time it is saved.

**`Client`** returns an `*http.Client`. Its transport asks for a valid token on
every request, so a client kept for a long time keeps working.

- **Token cache:** opened tokens are cached in memory, keyed by user and
  provider. At most 10,000 entries are held, and the oldest goes first.
- **Refresh:** when less than 60s of validity is left, the token is refreshed
  with `grant_type=refresh_token`.
  - A new refresh token, when the provider issues one, replaces the old one.
    Otherwise the old one is kept.
  - The result is sealed and saved.
  - Concurrent refreshes for one user and provider are coalesced into one call
    within the process.
- **When `ErrNotLinked` is returned:**
  - No refresh token, and the access token has expired: return `ErrNotLinked`.
  - `invalid_grant` on refresh: `Delete` the entry, drop it from the cache, and
    return `ErrNotLinked`.

**`Revoke`** calls the discovery `revocation_endpoint` when the provider has one,
then `Delete`s the entry and drops it from the cache.

### Errors

| Case | Response |
|---|---|
| Unknown provider name in the URL | 404 via `host.ServeStatus` |
| No session plugin | 500, logged once |
| Discovery or provider unreachable | 303 to `ErrorPath?error=unavailable`, else 503 |
| Callback failure | 303 to `ErrorPath?error=<code>`, else the matching status: 400 for `state`, `expired`, `denied` and `rejected`; 502 for `exchange` and `token` |

Start fails (from `Init` or `Configure`) for any of these:

- no providers;
- a provider without a name, or two with the same name;
- an unknown preset;
- neither a preset nor an issuer;
- an issuer that is not `https` (or `http` to loopback);
- no client ID;
- a client secret that is unset, or whose environment variable is empty;
- `Store` without `Key`;
- a key shorter than 32 bytes, or bad hex (the error names no byte of the key);
- `OnLogin` unset;
- a prefix that does not start with `/`.

## Testing

**Fake provider:** an `httptest.NewTLSServer` acting as the OIDC provider, using
the stdlib only. It serves discovery, token, userinfo and revocation endpoints, and
its id_tokens are unsigned JWTs (the claims are what is checked). Tests reach it
through `Options.HTTPClient = server.Client()`.

**End to end** with `collagetest` and elagoht/session:

1. GET login, which gives a 303 to the authorization endpoint with every
   parameter.
2. Simulate the provider by calling callback with the `state` from step 1 and a
   code.
3. The session is signed in under `UserKey`, its cookie value changed, and the
   answer is a 303 to `next`.
4. A page with `RequireUser` is now reachable.

**Each callback failure:**

- `state` mismatch;
- replayed callback (fails the second time);
- expired pending sign-in;
- `denied`;
- `nonce`, `aud`, `azp`, `iss`, `exp` and `iat` each wrong;
- token endpoint 500;
- userinfo `sub` mismatch;
- `OnLogin` error and empty userID.

Each one checks the response for both `ErrorPath` set and `ErrorPath` empty.

**Redirects:** `next` of `//evil.com`, `/\evil.com`, `https://evil.com` and
`javascript:x` each ends at `AfterLogin`.

**Tokens:**

- The `Store` holds no plaintext token.
- A sealed blob swapped between users fails to open.
- A blob sealed with a `PreviousKeys` key opens, and is sealed with `Key` when
  next saved.
- Refresh near expiry, with a rotated refresh token kept.
- Twenty concurrent `Client` calls make one refresh.
- `invalid_grant` gives `ErrNotLinked` and deletes the entry.
- `Revoke` calls the endpoint and deletes the entry.
- No refresh token and an expired access token give `ErrNotLinked`.

**Multi-tenant:** `redirect_uri` follows the request host through a test
`OriginResolver`.

**Start errors:** one test per case.

**Core:** `SafeRedirect` table tests:

- `/a` is kept;
- `//a`, `/\a` and `https://a` give the fallback;
- `/./\a`, `/a/../\a` and any backslash give the fallback; `/.//a`,
  `/%2F%2Fa`, `/%5Ca` and `/ /x` are kept;
- piped through `http.Redirect`, an accepted value never gives a Location
  starting with `//` or `/\`;
- a control character gives the fallback;
- an empty value gives the fallback;
- an invalid fallback gives `"/"`;
- the router's existing redirect tests still pass.

## Release order

1. collage v0.44.0 (`SafeRedirect`), with the CHANGELOG and docs.
2. The new public repo `Elagoht/collage-oauth` v0.1.0, on collage v0.44.0 and
   collage-session v0.2.1.
3. collage's CI matrix gains collage-oauth after its first tag; then a
   workflow_dispatch run.
4. Docs site, EN and TR:
   - the oauth plugin;
   - `SafeRedirect`;
   - session's login examples.
5. Roadmap core-change log:
   - "oauth → `collage.SafeRedirect`, additive, v0.44.0; the open-redirect
     decision: the core validates nothing it did not, and offers the check";
   - "oauth: guard and action Locations stay unchecked, by design".
