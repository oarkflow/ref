# Build context must be the repository root — examples/starter's go.mod
# has `replace github.com/oarkflow/ref => ../../`, a relative path that only
# resolves if the parent module's source is inside the build context too:
#
#   docker build -f examples/starter/Dockerfile -t starter .
#
# (run from the repository root, not from examples/starter/).

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/starter
# modernc.org/sqlite is pure Go (no cgo, unlike mattn/go-sqlite3), so a
# fully static binary needs nothing else installed in this stage.
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server
RUN CGO_ENABLED=0 go build -o /out/migrator ./cmd/migrator

FROM alpine:3.20
RUN adduser -D -u 10001 app && mkdir -p /app/.data && chown -R app /app
WORKDIR /app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/migrator /usr/local/bin/migrator
COPY --from=build /src/examples/starter/resources ./resources
USER app
EXPOSE 8080
ENV PORT=8080
# SESSION_SECRET and WEBHOOK_SECRET have no default and must be supplied,
# e.g. `docker run -e SESSION_SECRET=... -e WEBHOOK_SECRET=... starter`,
# or an .env file bind-mounted at /app/.env (see ../../CONFIGURATION.md).
#
# Schema is not applied automatically on boot (see cmd/migrator/main.go's
# doc comment — deliberate, same as running it outside a container): run
# the migrator once against the target database before the first
# `docker run` of the server image, and again after any schema change —
#
#   docker run --rm -e DB_DRIVER=... -e DB_DSN=... starter /usr/local/bin/migrator cli migrate
#
# CMD, not ENTRYPOINT, so that command overrides the default (the server)
# instead of appending to it.
CMD ["/usr/local/bin/server"]
