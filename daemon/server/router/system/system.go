package system

import (
	"github.com/moby/moby/v2/daemon/server/router"
	"resenje.org/singleflight"
)

// systemRouter provides information about the Docker system overall.
// It gathers information about host, daemon and container events.
type systemRouter struct {
	backend  Backend
	cluster  ClusterBackend
	routes   []router.Route
	builder  BuildBackend
	features func() map[string]bool

	// collectSystemInfo is a single-flight for the /info endpoint,
	// unique per API version (as different API versions may return
	// a different API response).
	collectSystemInfo singleflight.Group[string, *infoResponse]
}

// NewRouter initializes a new system router
func NewRouter(b Backend, c ClusterBackend, builder BuildBackend, features func() map[string]bool) router.Router {
	r := &systemRouter{
		backend:  b,
		cluster:  c,
		builder:  builder,
		features: features,
	}

	r.routes = []router.Route{
		router.NewOptionsRoute("/{anyroute:.*}", optionsHandler),
		router.NewGetRoute("/_ping", r.pingHandler),
		router.NewHeadRoute("/_ping", r.pingHandler),
		router.NewGetRoute("/events", r.getEvents),
		router.NewGetRoute("/info", r.getInfo),
		router.NewGetRoute("/version", r.getVersion),
		router.NewGetRoute("/system/df", r.getDiskUsage),
		router.NewPostRoute("/auth", r.postAuth),
	}

	return r
}

// Routes returns all the API routes dedicated to the docker system
func (s *systemRouter) Routes() []router.Route {
	return s.routes
}
