package agentcontext

import (
	"net/http"
	"testing"
)

// mcpHTTPClient returns an isolated *http.Client when running inside
// tests, or nil for production. During tests, httptest.Server.Close()
// calls http.DefaultTransport.CloseIdleConnections(), which disrupts any
// MCP client sharing that transport. When DefaultTransport is a
// *http.Transport it is cloned; otherwise a minimal transport with
// ProxyFromEnvironment is created as a fallback.
//
// This lives in its own file (rather than mcprunner.go) so the runner
// does not import "testing": the timeout ruleguard lint treats any file
// that imports "testing" as test code.
func mcpHTTPClient() *http.Client {
	if !testing.Testing() {
		return nil
	}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		return &http.Client{Transport: dt.Clone()}
	}
	return &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}}
}
