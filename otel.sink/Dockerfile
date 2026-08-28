# Build
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -ldflags "-X main.version=docker" -o /out/otel.sink .

# Run
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/otel.sink /otel.sink
# 4317 OTLP/gRPC, 4318 OTLP/HTTP, 8080 dashboard and query API
EXPOSE 4317 4318 8080
ENTRYPOINT ["/otel.sink"]
