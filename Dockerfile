FROM golang:1.26.5 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/csms-server ./cmd/csms-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/csms-server /csms-server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/csms-server"]
