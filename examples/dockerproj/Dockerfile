# The build context is the repository root, because this example's go.mod
# has a `replace` pointing at the gormgate checkout above it. From the
# repository root that is:
#
#	docker build -f examples/dockerproj/Dockerfile .
#
# compose.yaml sets the same context. A project depending on a released
# gormgate would build from its own directory with a plain `COPY . .` and
# none of the library COPY lines below.

FROM golang:1.26 AS build
WORKDIR /src

# The library, where the replace directive expects to find it. The root
# .go files are copied by glob rather than by name so that a new one does
# not silently break this build; backends, internal and migrations are the
# library's only package directories.
COPY go.mod go.sum ./
COPY *.go ./
COPY backends ./backends
COPY internal ./internal
COPY migrations ./migrations

# The project.
COPY examples/dockerproj ./examples/dockerproj
WORKDIR /src/examples/dockerproj
RUN go mod download
RUN CGO_ENABLED=0 go build -o /out/gorm-gate ./cmd/gorm-gate \
	&& CGO_ENABLED=0 go build -o /out/server ./cmd/server

# Two binaries, one image: the migration step and the app are built from
# the same commit, so they cannot disagree about the schema.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gorm-gate /out/server /usr/local/bin/
EXPOSE 8080
CMD ["server"]
