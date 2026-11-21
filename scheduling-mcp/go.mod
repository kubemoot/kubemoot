module github.com/kubemoot/kubemoot/scheduling-mcp

go 1.27.4

require (
	github.com/google/uuid v1.6.0
	github.com/kubemoot/kubemoot/operator v0.0.0
	github.com/nats-io/nats.go v1.56.0
)

require (
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/nats-io/nkeys v0.4.17 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)

replace github.com/kubemoot/kubemoot/operator => ../operator
