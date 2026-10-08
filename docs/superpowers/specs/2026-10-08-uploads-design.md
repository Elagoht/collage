# Uploads: validated, stored file uploads, ready for streaming

Date: 2026-10-08
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 3, "uploads"
(multipart action bodies, streaming reads, temp-file cleanup, a storage
interface).

## Motivation

An action's body is bounded (`MaxBodyBytes`, default 4 MiB). A multipart body
over the parser's memory budget spills to temp files, and these are removed when
the request ends.

The CSRF check reads the token from the `X-CSRF-Token` header first. Only when
that header is missing does it fall back to the form field, and reading the field
parses the whole multipart body before the handler runs. A JavaScript client that
sends the header therefore already reaches the handler with the body unread. A
plain HTML form does not.

Nothing validates files, names them safely, stores them, or serves them back
without the classic same-origin XSS.

The goal is the common case (forms with files up to tens of MB) done well. The
API must also be one that streaming large files can use later without a
breaking change.

## Decisions

- **The plugin carries the work.** `elagoht/uploads` provides `Receive` over
  either path (a parsed form, or a `MultipartReader` stream), plus a `Store`
  interface, a disk store, and a safe `Handler`.
- **The core adds one small seam.** `WithStreamingBody()` makes "never parse
  this body" a guarantee instead of a client's choice.

## Design

### 1. Core (collage v0.57.0)

```go
collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost).
	WithStreamingBody().
	WithMaxBodyBytes(2 << 30).
	WithHandler(handler)
```

`Action.StreamingBody bool`, set by `ActionBuilder.WithStreamingBody()`.

- **The body is never read by the core.** It is never read or parsed before the
  handler. `rc.Request.Body` reaches the handler unread.
- **CSRF comes only from the header.** The token is accepted only from the
  `X-CSRF-Token` header (the configured header name), never from a form field.
  A missing header is refused with 403, and the message says the token must come
  in the header for a streaming action.
- **`SkipCSRF` still skips the check entirely.** The body stays unread.
- **`MaxBodyBytes` applies as for any action.** It is enforced while the body is
  read and answered with 413. The 4 MiB default is unchanged, so a large upload
  must raise it explicitly.
- **Guards, page rendering and result handling are unchanged.** A helper that
  parses the form later, such as `validate.Form`, consumes the stream. The docs
  say so.
- **Method check.** `RegisterAction` refuses `WithStreamingBody` on an action
  whose methods include none of POST, PUT or PATCH. This is a wrapped error
  naming the action.
- **Opt-in.** Actions that do not declare it are unaffected.

### 2. Plugin `elagoht/uploads` (new repo Elagoht/collage-uploads, v0.1.0)

**Receiving**

```go
u := uploads.New(uploads.Dir("var/uploads"))
got, err := u.Receive(rc,
	uploads.Field("avatar", uploads.Rules{MaxSize: 5 << 20, MaxFiles: 1,
		Types: []string{"image/png", "image/jpeg"}}),
)
// got.Files["avatar"] []uploads.File — Key, Name, Size, Type, SHA256
// got.Values url.Values — the non-file fields
```

- **Two paths, one result.** When the request's form is already parsed,
  `Receive` reads files from it. Otherwise it walks `MultipartReader` part by
  part, streaming each file into the store. The same input gives the same
  result on both paths.
- **Size.** Counted as the file streams. Reading stops as soon as `MaxSize` is
  passed.
- **Type.** Sniffed from the first 512 bytes (`http.DetectContentType`), never
  taken from the extension or the client's `Content-Type`. The sniffed bytes are
  part of what is stored, so nothing is lost. An empty file is
  `application/octet-stream`. An empty `Types` list allows any type.
- **`MaxFiles`.** The number of files a field may carry. Default 1. A field that
  appears twice counts each file.
- **Unexpected files.** A file in a field that was not declared fails with
  `ErrUnexpectedField`.
- **Non-file values.** Collected in `Values`, at most 64 KiB in total, in any
  order relative to the files.
- **All or nothing.** If any file breaks a rule, or the client disconnects, or
  the body limit is hit, every file already stored for this request is deleted
  before `Receive` returns the error.
- **Errors.** `*uploads.Error{Field, Err}` wraps one of `ErrTooLarge`,
  `ErrType`, `ErrTooMany`, `ErrUnexpectedField`, so an action can show it as a
  form error. A second `Receive` on one request returns `ErrAlreadyRead`.
- **Names.**
  - The store key is 128 random bits in hex, plus the sniffed type's extension.
  - `Name` is the client's file name, cleaned: its base name, with control
    characters and path separators removed, and at most 255 bytes. It is never
    used as a path.
- **SHA-256** is computed while streaming.

**Storage**

```go
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) (int64, error) // never overwrites
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
```

- **`uploads.Dir(path)`** is the disk store.
  - It writes to a temp file in the same directory, then hard-links it into place (a rename would silently replace an existing file); where hard links are unsupported it falls back to an exclusive create plus copy.
  - The directory is 0700 and files are 0600.
  - It refuses keys containing `/`, `\`, `..` or control characters.
  - An existing key is an error.
- **Metadata** is not stored. `Receive` returns it, and the application keeps
  it.
- **Remote stores** (S3 and similar) come later as their own plugins over the
  same interface.

**Serving (optional)**

`u.Handler()` returns an `http.Handler` to mount with `host.Handle("/uploads/", …)`.
For a request, it:
1. Takes the key from the path, rejecting any key `Dir` would reject.
2. Opens the file and re-sniffs its type.
3. Answers with:
   - `Content-Type`: the sniffed type;
   - `X-Content-Type-Options: nosniff`;
   - `Content-Security-Policy: sandbox`;
   - `Content-Disposition: attachment` for everything except raster images
     (png, jpeg, gif, webp). SVG and HTML are always attachments;
   - `Cache-Control: private, max-age=0` by default, configurable.

A missing key is 404.

**Configuration** (`plugins-config.json`): `{ "elagoht/uploads": { "dir": "var/uploads" } }`.
This is used when the plugin is built with `New()` and no store. Rules are
given in code, per call.

## Testing

**Core**
- With `WithStreamingBody`, the body reaches the handler unread.
- CSRF: a form-field token is refused with 403, and the header token passes.
- `MaxBodyBytes` is enforced while the body streams.
- A GET-only action with `WithStreamingBody` fails at registration.
- Actions without the flag behave exactly as before.

**Plugin**
- Table tests run on both paths, and the same input must give the same result.
- Each rule violation produces its own error.
- All or nothing: a second file that breaks a rule deletes the first.
- A client disconnect and the body limit leave no files behind.
- Sniffing traps:
  - HTML that starts with a PNG signature;
  - SVG with a `.png` extension;
  - files shorter than 512 bytes;
  - empty files.
- `Dir`: key traversal, no overwrite, permissions.
- `Handler`: the headers, with SVG and HTML served as sandboxed attachments.
- End to end with a real server: a 100 MB streamed upload with memory measured
  flat, and the files stored intact (checked against SHA-256).

## Release

1. collage **v0.57.0** (additive): `WithStreamingBody`.
2. **Elagoht/collage-uploads v0.1.0**, then the CI-matrix commit and a matrix
   run from main (43 plugins).
3. Docs: framework `docs/actions.md`, the docs site (EN, TR), and the
   extension's schema.

## Out of scope

- S3 and other remote stores (the interface is ready).
- Resumable or chunked uploads (tus).
- Image processing (opti-image).
- Virus scanning.
- Client-side progress UI.
- Quotas and per-user limits.
