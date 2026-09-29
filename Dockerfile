FROM golang:1.27-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcphub ./cmd/mcphub

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/mcphub /usr/local/bin/mcphub
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/mcphub"]
