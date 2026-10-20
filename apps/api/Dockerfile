# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# modernc.org/sqlite is pure Go, so the binary is fully static.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/metric-api \
    ./app

# A named volume inherits the ownership of the image directory it covers. The
# API runs as nonroot, so create the mount point with that ownership here.
RUN mkdir -p /out/data && touch /out/data/.keep

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/metric-api /metric-api
COPY --from=build --chown=65532:65532 /out/data /data

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/metric-api"]
