# Build the control-plane binary.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Copy manifests first so dependency downloads cache across source edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 works because the SQLite driver (modernc.org/sqlite) is pure Go.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/overworld-ops ./cmd/overworld-ops

FROM alpine:3.21

# ca-certificates so the Docker SDK and image pulls can talk TLS.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=build /out/overworld-ops /app/overworld-ops

# The database and any bind-mounted server data live here.
VOLUME /app/data

EXPOSE 8080
ENTRYPOINT ["/app/overworld-ops"]
