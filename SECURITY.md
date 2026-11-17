# Security

Collage handles request-forgery tokens, request bodies, redirects and files
served from disk, so a bug in it can be a vulnerability in every site built on
it. Please report one privately.

## Reporting

Use GitHub's private vulnerability reporting: the **Report a vulnerability**
button on this repository's **Security** tab. Please do not open a public issue,
pull request or discussion for it.

A useful report says which version you tested, what an attacker can do, and the
smallest request or program that shows it. You will get an answer within a week,
and a fix, or a reason there will not be one, as soon as it is understood.

## Supported versions

A security fix ships as a patch release on the latest v1 minor, and its
CHANGELOG entry, under **Security**, says what was affected. There are no
backports: an older minor does not get a fix, so the way to receive one is to
upgrade to the latest v1 release, which never breaks an application.

## In scope

Anything collage does on an application's behalf: the forgery check, body limits,
redirect targets, path handling in the router and in mounts, cache keys, error
pages leaking internals, and the `collage` CLI.

A handler mounted with `app.Handle` is the application's own — collage applies no
forgery check, body limit or cache to it, by design — so what it does is not a
collage vulnerability. A way for a request to reach it that should not, is.
