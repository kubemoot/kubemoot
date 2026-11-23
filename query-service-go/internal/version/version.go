// Package version holds the build's version, stamped at link time from the git tag
// or commit; the source never carries a version.
package version

// Version is stamped by the build (-ldflags -X); a plain go build reports "dev".
var Version = "dev"

// Service is the service name /version reports, the same one the Python service used.
const Service = "kubemoot-query-service"
