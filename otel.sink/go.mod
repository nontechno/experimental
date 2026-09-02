module github.com/nontechno/experimental/otel.sink

go 1.25.0

// Dependencies are resolved by `go mod tidy`, which pins the current
// versions of:
//
//	go.opentelemetry.io/collector/pdata  (OTLP data model + gRPC service stubs)
//	google.golang.org/grpc               (OTLP/gRPC transport)
//	gopkg.in/yaml.v3                     (config file)

require (
	go.opentelemetry.io/collector/pdata v1.65.0
	google.golang.org/grpc v1.83.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	go.opentelemetry.io/collector/featuregate v1.65.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
