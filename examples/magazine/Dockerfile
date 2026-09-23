# Builds either binary from one file, selected with --build-arg CMD=api|site.
#
# Both are static and carry everything they serve — templates, stylesheet and the
# article corpus are embedded — so the runtime stage needs no application files at
# all. That is what makes scratch a viable base rather than an affectation: there is
# nothing left to copy.

FROM golang:1.26-alpine AS build
ARG CMD=site
WORKDIR /src

# Dependencies first, so editing source does not invalidate the module layer. This
# project has no third-party dependencies, which makes the layer trivial — and keeps
# it correct if that ever changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# CGO off and the symbol table stripped: a static binary with nothing to link
# against at runtime, which is what the scratch stage below requires.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/app ./examples/magazine/cmd/${CMD}

FROM scratch AS runtime
# Certificate roots, for the day the API client points at an https backend. Without
# them an outbound TLS handshake fails with a certificate error that reads like a
# server problem.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/app /app

# Bind on all interfaces: inside a container, localhost is reachable from nothing.
ENV HOST=0.0.0.0 MAGAZINE_API_ADDR=0.0.0.0:8080
EXPOSE 3000 8080
USER 65534:65534
ENTRYPOINT ["/app"]
