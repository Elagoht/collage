# Consent: a cookie-consent banner and gate, in the browser, readable on the server

Date: 2026-10-09
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 3, "consent"
("the consent cookie is a Vary dimension; validates **A5**, repeatable hoist;
integrates with analytics").

## Motivation

A site that loads analytics, embeds (YouTube, maps) or marketing tags must, in much
of the world, ask before they run. elagoht/analytics has its own client-side
`RequireConsent`, but no site-wide banner, categories or gate exist for anything
else. The roadmap pictured consent as a server-side Vary dimension. A consent that
varies every page would split the cache and break static export, and neither is
needed for the common case.

## Decisions

- **Consent is decided in the browser.** The server sends every visitor the same
  HTML. Gated scripts and iframes sit inert until their category is granted. The
  cache has no Vary, and static export works.
- **Opt-in server reading.** On paths the application lists, the plugin turns the
  consent cookie into a `collage.Vary` dimension. `consent.Granted(rc, category)`
  is then cache-safe there. Everywhere else it reports false.
- **A5 is not needed.** One script is hoisted once, and the gate is markup. The
  roadmap item closes as "not needed; consent resolved in the browser". There is
  no core change.

## Design

### 1. Plugin `elagoht/consent` (new repo Elagoht/collage-consent, v0.1.0)

**Configuration** (`plugins-config.json` or `NewWith`, read with `collage.PluginConfig`):

```json
{ "elagoht/consent": {
    "version": 1,
    "categories": [
      { "name": "necessary", "required": true },
      { "name": "analytics" },
      { "name": "media" }
    ],
    "text": {
      "en": { "title": "…", "body": "…", "accept": "Accept all", "reject": "Reject all",
              "save": "Save choices", "settings": "Choose",
              "categories": { "necessary": "Necessary", "analytics": "Analytics", "media": "Embedded media" },
              "placeholder": "This content loads from {host}.", "allow": "Allow {category}" }
    },
    "policyURL": "/privacy",
    "maxAgeDays": 180,
    "serverPaths": []
} }
```

**Validation.** `Init` refuses each of these with an error that starts
`elagoht/consent: `:
- a category name outside `[a-z0-9-]`, or a duplicate;
- no categories;
- `version < 1`;
- a missing text block for the default locale, or a missing category label in it;
- `maxAgeDays` outside 1..400;
- a `serverPaths` entry that does not start with `/`.

Other locales fall back to the default locale's text, key by key.

**The script.**
- The plugin serves one static file at `/_collage/consent/consent.js`. It sets
  `Content-Type: text/javascript; charset=utf-8` and `nosniff`, and the URL
  carries a content-hash query so a long cache is safe.
- Through `AfterRender`/hoist, the plugin adds
  `<script defer src="/_collage/consent/consent.js?v=<hash>" data-…>` to the `head` area
  of every HTML page, once per page.
- The settings travel in `data-*` attributes: the version, the categories with
  their required flags, the text for the page's locale (JSON in one attribute,
  HTML-escaped), the policy URL and the cookie max-age.
- There is no inline script, so the page works under a CSP `script-src 'self'`
  without a nonce.
- The file is also written by a static export (as a mount or a document that the
  build writes).

**The banner.**
- The JS builds a `<dialog>` that is keyboard-operable, moves focus into it, traps
  focus while it is open, and returns focus when it closes. It offers Accept all,
  Reject all, Choose (one checkbox per category; required ones are checked and
  disabled), Save choices, and a link to `policyURL`.
- It has its own small stylesheet, injected as a `<style>` element built by the
  script. The script builds it rather than writing it inline, so CSP `style-src`
  still applies. Theming goes through CSS custom properties (`--consent-bg`, …).
  If a strict CSP forbids JS-built styles, the banner falls back to unstyled but
  usable markup.
- The banner opens:
  - when there is no decision;
  - when the stored version is lower than the configured one;
  - when an element with `data-consent-open` is clicked.

**The gate (markup).**

```html
<script type="text/plain" data-consent="analytics" src="https://example.com/a.js"></script>
<script type="text/plain" data-consent="analytics">/* inline, too */</script>
<iframe data-consent="media" data-src="https://www.youtube-nocookie.com/embed/…" title="…"></iframe>
```

- **On grant:**
  - each gated `<script>` is replaced by a real one, with the same attributes
    except `type`/`data-consent`, the same text, and the same document position.
    Scripts run in document order;
  - each gated iframe gets its `src` from `data-src`.
- **While a category is not granted,** a gated iframe is preceded by a
  placeholder button: "This content loads from {host}. Allow {category}". It
  grants that one category.
- **Withdrawal.** When a grant is withdrawn, the page reloads, because a script
  that has run cannot be undone.
- **Unknown category.** A `data-consent` naming a category that is not configured
  stays inert, and a `console.warn` names it.

**Storage.**
- The decision is stored in the first-party cookie `collage_consent`:
  `v=<version>&c=<sorted,granted,categories>&t=<unix>`.
- Its attributes are `Path=/`, `SameSite=Lax`, `Secure` on https, and
  `Max-Age=maxAgeDays*86400`. It is not `HttpOnly`, because the script reads it.
  The cookie itself belongs to "necessary".
- A malformed cookie or an older version counts as no decision.

**Global Privacy Control.** When `navigator.globalPrivacyControl` is true, the
banner opens with every non-required category off. Accept all still grants them,
because the visitor's explicit choice wins. Until the visitor decides, nothing is
granted.

**The JS API** is `window.collageConsent.get()` (granted categories),
`.set({analytics: true, …})`, `.open()`, and a `collage:consent` event on
`document` whose `detail` is the granted list.

### 2. Server-side reading (opt-in)

```go
if consent.Granted(rc, "analytics") { … }
```

**The middleware.** For a request whose path is under a `serverPaths` prefix
(segment-aware: `/shop` matches `/shop` and `/shop/…`, never `/shopping`), the
plugin's middleware:
1. parses the cookie;
2. computes the granted non-required categories, sorted and comma-joined (`""`
   when none);
3. calls `collage.Vary(r, "Cookie", combo)`.

The collage cache then keys on the combo, so at most 2^(non-required) entries
exist per page. The response carries `Vary: Cookie`, so a CDN keeps visitors
apart. The cost is that these paths hardly cache at a CDN, which is why the
setting is per path.

**`Granted(rc, category)`:**
- a required category is true;
- otherwise it reads `collage.Varied(rc, "Cookie")` and reports whether the
  category is in the combo;
- if nothing was varied, because the path is not in `serverPaths`, it is a static
  export, or it is a capture request, it is false. A path outside `serverPaths`
  also logs one Warn per category name ("not readable on the server here; add the
  path to serverPaths").

Server and browser agree, because both read the same cookie with the same rules
(version, format, GPC excepted: the server cannot see GPC, so it honours what
the visitor saved).

### 3. elagoht/analytics (minor release)

- **New option `consentCategory` (string).** When it is set, analytics writes its
  tags as gated markup (`type="text/plain" data-consent="<category>"`, and inline
  loaders the same way), and the consent plugin opens them.
- **`RequireConsent` and `collageAnalyticsConsent()` keep working.** The README
  recommends `consentCategory` with elagoht/consent.
- **Validation.** Setting both is an error.
- **Do Not Track is unchanged.**

## Testing

**Go**
- Config validation, one case per rule.
- The script tag is hoisted exactly once per page, with `data-*` for the page's
  locale and fallback text, and HTML-escaped.
- `/_collage/consent/consent.js` is served with the right type, nosniff and a long cache
  under its hash.
- A static export writes the JS file.
- `serverPaths` matching respects segments.
- Varied combos:
  - two visitors with different cookies on a cached serverPath page get different
    cached renders, and `Vary: Cookie`;
  - the same combo shares one entry.
- `Granted`:
  - outside serverPaths it is false and warns once;
  - for a required category it is true;
  - a malformed cookie or an old version gives false;
  - a capture request and a static export give false.
- analytics with `consentCategory` emits gated markup.

**JS**, in headless Chrome against a real collage test site. The page reports
results by POSTing them back, as collage-live's browser tests do.
- **Banner**: it appears with no cookie; Accept, Reject and Choose write the
  cookie format; a version bump reopens it; `data-consent-open` reopens it.
- **Gate**: scripts run in order after grant and not before; iframe src is set;
  the placeholder grants one category; an unknown category stays inert.
- **Withdrawal** reloads the page.
- **GPC** defaults.
- **Focus**: it moves into the dialog and returns afterwards, and Escape closes
  the dialog without deciding.

The JS tests skip when no Chrome is found (the CI matrix), and are required
locally before release.

## Release

1. **Elagoht/collage-consent v0.1.0**, then the CI-matrix commit (44 plugins).
2. **elagoht/analytics** minor release with `consentCategory`.
3. **Docs:** the docs site (EN, TR), the extension schema, and the roadmap
   (A5 closed as not needed).

There is no collage release.

## Out of scope

- IAB TCF.
- Geo-targeted banners (only in the EU, say).
- Server-side logging of consent records.
- Per-vendor (rather than per-category) choices.
- Reading GPC on the server (the `Sec-GPC` header). That is a possible follow-up:
  `Granted` could honour it.
