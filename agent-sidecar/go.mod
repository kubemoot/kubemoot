module github.com/kubemoot/kubemoot/agent-sidecar

go 1.27.2

require (
	github.com/google/uuid v1.6.0
	github.com/kubemoot/kubemoot/operator v0.0.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/nats-io/nats-server/v2 v2.15.1
	github.com/nats-io/nats.go v1.54.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.8.0-default-no-op // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

// Spike: the sidecar imports the generated bus contract from the operator module.
replace github.com/kubemoot/kubemoot/operator => ../operator
