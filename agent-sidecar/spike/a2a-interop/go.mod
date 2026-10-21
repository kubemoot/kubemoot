module github.com/kubemoot/kubemoot/agent-sidecar/spike/a2a-interop

go 1.27.2

require (
	github.com/a2aproject/a2a-go/v2 v2.6.0
	github.com/kubemoot/kubemoot/agent-sidecar v0.0.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
)

replace github.com/kubemoot/kubemoot/agent-sidecar => ../..

replace github.com/kubemoot/kubemoot/operator => ../../../operator
