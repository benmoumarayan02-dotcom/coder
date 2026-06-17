package coderd

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/coder/coder/v2/coderd/httpapi"
	"github.com/coder/coder/v2/codersdk"
)

// RegisterInMemoryAIBridgeProxydHTTPHandler mounts [aibridgeproxyd.Server]'s HTTP handler
// onto [API]'s router, so that requests to aibridgedproxy will be relayed from Coder's API server
// to the in-memory aibridgedproxy.
func (api *API) RegisterInMemoryAIBridgeProxydHTTPHandler(srv http.Handler) {
	if srv == nil {
		panic("aibridgeproxyd cannot be nil")
	}

	api.aibridgeproxydHandler = srv
}

// aibridgeproxyHandler handles legacy AI Bridge Proxy endpoints under /api/v2/aibridge/proxy.
// Kept for backward compatibility. New endpoints should be added to /ai-gateway only.
func aibridgeproxyHandler(api *API, middlewares ...func(http.Handler) http.Handler) func(r chi.Router) {
	return aiGatewayProxyRoutes(api, "/api/v2/aibridge/proxy", middlewares...)
}

// aiGatewayProxyHTTPHandler handles AI Gateway Proxy endpoints under /api/v2/ai-gateway/proxy.
func aiGatewayProxyHTTPHandler(api *API, middlewares ...func(http.Handler) http.Handler) func(r chi.Router) {
	return aiGatewayProxyRoutes(api, "/api/v2/ai-gateway/proxy", middlewares...)
}

// aiGatewayProxyRoutes builds the route tree for AI Gateway Proxy endpoints.
func aiGatewayProxyRoutes(api *API, stripPrefix string, middlewares ...func(http.Handler) http.Handler) func(r chi.Router) {
	return func(r chi.Router) {
		r.Use(api.RequireFeatureMW(codersdk.FeatureAIBridge))
		r.Use(middlewares...)

		r.HandleFunc("/*", func(rw http.ResponseWriter, r *http.Request) {
			// Check if the proxy is enabled.
			if !api.DeploymentValues.AI.BridgeProxyConfig.Enabled.Value() {
				httpapi.Write(r.Context(), rw, http.StatusNotFound, codersdk.Response{
					Message: "AI Gateway Proxy is not enabled.",
				})
				return
			}

			// Check if the handler is registered.
			if api.aibridgeproxydHandler == nil {
				httpapi.Write(r.Context(), rw, http.StatusNotFound, codersdk.Response{
					Message: "AI Gateway Proxy handler not mounted.",
				})
				return
			}

			// Strip the prefix and relay to the aibridgeproxyd handler.
			http.StripPrefix(stripPrefix, api.aibridgeproxydHandler).ServeHTTP(rw, r)
		})
	}
}
