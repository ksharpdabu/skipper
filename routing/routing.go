package routing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zalando/skipper/eskip"
	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/logging"
)

const (
	// PathName represents the name of builtin path predicate.
	// (See more details about the Path and PathSubtree predicates
	// at https://godoc.org/github.com/zalando/skipper/eskip)
	PathName = "Path"

	// PathSubtreeName represents the name of the builtin path subtree predicate.
	// (See more details about the Path and PathSubtree predicates
	// at https://godoc.org/github.com/zalando/skipper/eskip)
	PathSubtreeName = "PathSubtree"

	routesTimestampName      = "X-Timestamp"
	routesCountName          = "X-Count"
	defaultRouteListingLimit = 1024
)

// MatchingOptions controls route matching.
type MatchingOptions uint

const (
	// MatchingOptionsNone indicates that all options are default.
	MatchingOptionsNone MatchingOptions = 0

	// IgnoreTrailingSlash indicates that trailing slashes in paths are ignored.
	IgnoreTrailingSlash MatchingOptions = 1 << iota
)

func (o MatchingOptions) ignoreTrailingSlash() bool {
	return o&IgnoreTrailingSlash > 0
}

// DataClient instances provide data sources for
// route definitions.
type DataClient interface {
	LoadAll() ([]*eskip.Route, error)
	LoadUpdate() ([]*eskip.Route, []string, error)
}

// Predicate instances are used as custom user defined route
// matching predicates.
type Predicate interface {

	// Returns true if the request matches the predicate.
	Match(*http.Request) bool
}

// PredicateSpec instances are used to create custom predicates
// (of type Predicate) with concrete arguments during the
// construction of the routing tree.
type PredicateSpec interface {

	// Name of the predicate as used in the route definitions.
	Name() string

	// Creates a predicate instance with concrete arguments.
	Create([]interface{}) (Predicate, error)
}

// Options for initialization for routing.
type Options struct {

	// Registry containing the available filter
	// specifications that are used during processing
	// the filter chains in the route definitions.
	FilterRegistry filters.Registry

	// Matching options are flags that control the
	// route matching.
	MatchingOptions MatchingOptions

	// The timeout between requests to the data
	// clients for route definition updates.
	PollTimeout time.Duration

	// The set of different data clients where the
	// route definitions are read from.
	DataClients []DataClient

	// Specifications of custom, user defined predicates.
	Predicates []PredicateSpec

	// Performance tuning option.
	//
	// When zero, the newly constructed routing
	// tree will take effect on the next routing
	// query after every update from the data
	// clients. In case of higher values, the
	// routing queries have priority over the
	// update channel, but the next routing tree
	// takes effect only a few requests later.
	//
	// (Currently disabled and used with hard wired
	// 0, until the performance benefit is verified
	// by benchmarks.)
	UpdateBuffer int

	// Set a custom logger if necessary.
	Log logging.Logger

	// SuppressLogs indicates whether to log only a summary of the route changes.
	SuppressLogs bool

	// PostProcessrs contains custom route post-processors.
	PostProcessors []PostProcessor
}

// RouteFilter contains extensions to generic filter
// interface, serving mainly logging/monitoring
// purpose.
type RouteFilter struct {
	filters.Filter
	Name  string
	Index int
}

// Route object with preprocessed filter instances.
type Route struct {

	// Fields from the static route definition.
	eskip.Route

	// path predicate matching a subtree
	path string

	// path predicate matching a subtree
	pathSubtree string

	// The backend scheme and host.
	Scheme, Host string

	// The preprocessed custom predicate instances.
	Predicates []Predicate

	// The preprocessed filter instances.
	Filters []*RouteFilter

	// TODO: would a circular list be better?

	// Next is forming a linked to the next route of a
	// loadbalanced group of routes. This is nil if the route is
	// the last in the linked list or there is only one route. To
	// find the Next in case of the last route of the list, you
	// have to use the Head.
	Next *Route

	// Head is the pointer to the head of linked list that forms
	// the loadbalancer group of Route. Every Route will point to
	// the same Route for being Head.
	Head *Route

	// Me is a pointer to self, to workaround Go type missmatch
	// check, because eskip.Route != routing.Route
	Me *Route

	// Group is equal for all routes, members, forming a loadbalancer pool.
	Group string

	// IsLoadBalanced tells the proxy that the current route
	// is a member of a load balanced group.
	IsLoadBalanced bool
}

// PostProcessor is an interface for custom post-processors applying changes
// to the routes after they were created from their data representation and
// before they were passed to the proxy.
//
// This feature is experimental.
type PostProcessor interface {
	Do([]*Route) []*Route
}

// Routing ('router') instance providing live
// updatable request matching.
type Routing struct {
	routeTable atomic.Value // of struct routeTable
	log        logging.Logger
	quit       chan struct{}
}

// New initializes a routing instance, and starts listening for route
// definition updates.
func New(o Options) *Routing {
	if o.Log == nil {
		o.Log = &logging.DefaultLog{}
	}

	r := &Routing{log: o.Log, quit: make(chan struct{})}
	initialMatcher, _ := newMatcher(nil, MatchingOptionsNone)
	rt := &routeTable{
		m:       initialMatcher,
		created: time.Now().UTC(),
	}
	r.routeTable.Store(rt)
	r.startReceivingUpdates(o)
	return r
}

// ServeHTTP renders the list of current routes.
func (r *Routing) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rt := r.routeTable.Load().(*routeTable)
	req.ParseForm()
	createdUnix := strconv.FormatInt(rt.created.Unix(), 10)
	ts := req.Form.Get("timestamp")
	if ts != "" && createdUnix != ts {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}

	if req.Method == "HEAD" {
		w.Header().Set(routesTimestampName, createdUnix)
		w.Header().Set(routesCountName, strconv.Itoa(len(rt.validRoutes)))

		if strings.Contains(req.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}

		return
	}

	offset, err := extractParam(req, "offset", 0)
	if err != nil {
		http.Error(w, "invalid offset", http.StatusBadRequest)
		return
	}

	limit, err := extractParam(req, "limit", defaultRouteListingLimit)
	if err != nil {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}

	w.Header().Set(routesTimestampName, createdUnix)
	w.Header().Set(routesCountName, strconv.Itoa(len(rt.validRoutes)))

	routes := slice(rt.validRoutes, offset, limit)
	if strings.Contains(req.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(routes); err != nil {
			http.Error(
				w,
				http.StatusText(http.StatusInternalServerError),
				http.StatusInternalServerError,
			)
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	eskip.Fprint(w, extractPretty(req), routes...)
}

func (r *Routing) startReceivingUpdates(o Options) {
	c := make(chan *routeTable)
	go receiveRouteMatcher(o, c, r.quit)
	go func() {
		for {
			select {
			case rt := <-c:
				r.routeTable.Store(rt)
				r.log.Info("route settings applied")
			case <-r.quit:
				return
			}
		}
	}()
}

// Route matches a request in the current routing tree.
//
// If the request matches a route, returns the route and a map of
// parameters constructed from the wildcard parameters in the path
// condition if any. If there is no match, it returns nil.
func (r *Routing) Route(req *http.Request) (*Route, map[string]string) {
	rt := r.routeTable.Load().(*routeTable)
	return rt.m.match(req)
}

// RouteLookup captures a single generation of the lookup tree, allowing multiple
// lookups to the same version of the lookup tree.
//
// Experimental feature. Using this solution potentially can cause large memory
// consumption in extreme cases, typically when:
// the total number routes is large, the backend responses to a subset of these
// routes is slow, and there's a rapid burst of consecutive updates to the
// routing table. This situation is considered an edge case, but until a protection
// against is found, the feature is experimental and its exported interface may
// change.
type RouteLookup struct {
	matcher *matcher
}

// Do executes the lookup against the captured routing table. Equivalent to
// Routing.Route().
func (rl *RouteLookup) Do(req *http.Request) (*Route, map[string]string) {
	return rl.matcher.match(req)
}

// Get returns a captured generation of the lookup table. This feature is
// experimental. See the description of the RouteLookup type.
func (r *Routing) Get() *RouteLookup {
	rt := r.routeTable.Load().(*routeTable)
	return &RouteLookup{matcher: rt.m}
}

// Close closes routing, stops receiving routes.
func (r *Routing) Close() {
	close(r.quit)
}

func slice(r []*eskip.Route, offset int, limit int) []*eskip.Route {
	if offset > len(r) {
		offset = len(r)
	}
	end := offset + limit
	if end > len(r) {
		end = len(r)
	}
	result := r[offset:end]
	if result == nil {
		return []*eskip.Route{}
	}
	return result
}

func extractParam(r *http.Request, key string, defaultValue int) (int, error) {
	param := r.Form.Get(key)
	if param == "" {
		return defaultValue, nil
	}
	val, err := strconv.Atoi(param)
	if err != nil {
		return 0, err
	}
	if val < 0 {
		return 0, fmt.Errorf("invalid value `%d` for `%s`", val, key)
	}
	return val, nil
}

func extractPretty(r *http.Request) eskip.PrettyPrintInfo {
	vals, ok := r.Form["nopretty"]
	if !ok || len(vals) == 0 {
		return eskip.PrettyPrintInfo{Pretty: true, IndentStr: "  "}
	}
	val := vals[0]
	if val == "0" || val == "false" {
		return eskip.PrettyPrintInfo{Pretty: true, IndentStr: "  "}
	}
	return eskip.PrettyPrintInfo{Pretty: false, IndentStr: ""}
}
