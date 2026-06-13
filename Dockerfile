FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN go build \
  -ldflags "-X github.com/cmesh/cmesh/internal/version.Version=${VERSION} -X github.com/cmesh/cmesh/internal/version.Commit=${COMMIT} -X github.com/cmesh/cmesh/internal/version.Date=${DATE}" \
  -o /out/cmesh ./cmd/cmesh

FROM debian:bookworm-slim

RUN useradd --system --create-home --home-dir /var/lib/cmesh cmesh
COPY --from=build /out/cmesh /usr/local/bin/cmesh

USER cmesh
WORKDIR /var/lib/cmesh
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/cmesh"]
CMD ["manager", "start", "--addr", ":8080"]
