package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/serf/coordinate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/consul/testrpc"
)

func TestHealthChecksInState(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	t.Run("warning", func(t *testing.T) {
		a := NewTestAgent(t, "")
		defer a.Shutdown()

		req, _ := http.NewRequest("GET", "/v1/health/state/warning?dc=dc1", nil)
		retry.Run(t, func(r *retry.R) {
			resp := httptest.NewRecorder()
			obj, err := a.srv.HealthChecksInState(resp, req)
			if err != nil {
				r.Fatal(err)
			}
			if err := checkIndex(resp); err != nil {
				r.Fatal(err)
			}

			// Should be a non-nil empty list
			nodes := obj.(structs.HealthChecks)
			if nodes == nil || len(nodes) != 0 {
				r.Fatalf("bad: %v", obj)
			}
		})
	})

	t.Run("passing", func(t *testing.T) {
		a := NewTestAgent(t, "")
		defer a.Shutdown()

		req, _ := http.NewRequest("GET", "/v1/health/state/passing?dc=dc1", nil)
		retry.Run(t, func(r *retry.R) {
			resp := httptest.NewRecorder()
			obj, err := a.srv.HealthChecksInState(resp, req)
			if err != nil {
				r.Fatal(err)
			}
			if err := checkIndex(resp); err != nil {
				r.Fatal(err)
			}

			// Should be 1 health check for the server
			nodes := obj.(structs.HealthChecks)
			if len(nodes) != 1 {
				r.Fatalf("bad: %v", obj)
			}
		})
	})
}

func TestHealthChecksInState_NodeMetaFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:   "bar",
			Name:   "node check",
			Status: api.HealthCritical,
		},
	}
	var out struct{}
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ := http.NewRequest("GET", "/v1/health/state/critical?node-meta=somekey:somevalue", nil)
	retry.Run(t, func(r *retry.R) {
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthChecksInState(resp, req)
		if err != nil {
			r.Fatal(err)
		}
		if err := checkIndex(resp); err != nil {
			r.Fatal(err)
		}

		// Should be 1 health check for the server
		nodes := obj.(structs.HealthChecks)
		if len(nodes) != 1 {
			r.Fatalf("bad: %v", obj)
		}
	})
}

func TestHealthChecksInState_Filter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:   "bar",
			Name:   "node check",
			Status: api.HealthCritical,
		},
	}
	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	args = &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:   "bar",
			Name:   "node check 2",
			Status: api.HealthCritical,
		},
		SkipNodeUpdate: true,
	}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ := http.NewRequest("GET", "/v1/health/state/critical?filter="+url.QueryEscape("Name == `node check 2`"), nil)
	retry.Run(t, func(r *retry.R) {
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthChecksInState(resp, req)
		require.NoError(r, err)
		require.NoError(r, checkIndex(resp))

		// Should be 1 health check for the server
		nodes := obj.(structs.HealthChecks)
		require.Len(r, nodes, 1)
	})
}

func TestHealthChecksInState_DistanceSort(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		Check: &structs.HealthCheck{
			Node:   "bar",
			Name:   "node check",
			Status: api.HealthCritical,
		},
	}

	var out struct{}
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	args.Node, args.Check.Node = "foo", "foo"
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ := http.NewRequest("GET", "/v1/health/state/critical?dc=dc1&near=foo", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthChecksInState(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)
	nodes := obj.(structs.HealthChecks)
	if len(nodes) != 2 {
		t.Fatalf("bad: %v", nodes)
	}
	if nodes[0].Node != "bar" {
		t.Fatalf("bad: %v", nodes)
	}
	if nodes[1].Node != "foo" {
		t.Fatalf("bad: %v", nodes)
	}

	// Send an update for the node and wait for it to get applied.
	arg := structs.CoordinateUpdateRequest{
		Datacenter: "dc1",
		Node:       "foo",
		Coord:      coordinate.NewCoordinate(coordinate.DefaultConfig()),
	}
	if err := a.RPC("Coordinate.Update", &arg, &out); err != nil {
		t.Fatalf("err: %v", err)
	}
	// Retry until foo moves to the front of the line.
	retry.Run(t, func(r *retry.R) {
		resp = httptest.NewRecorder()
		obj, err = a.srv.HealthChecksInState(resp, req)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		assertIndex(t, resp)
		nodes = obj.(structs.HealthChecks)
		if len(nodes) != 2 {
			r.Fatalf("bad: %v", nodes)
		}
		if nodes[0].Node != "foo" {
			r.Fatalf("bad: %v", nodes)
		}
		if nodes[1].Node != "bar" {
			r.Fatalf("bad: %v", nodes)
		}
	})
}

func TestHealthNodeChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/node/nope?dc=dc1", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthNodeChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes := obj.(structs.HealthChecks)
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("bad: %v", obj)
	}

	req, _ = http.NewRequest("GET", fmt.Sprintf("/v1/health/node/%s?dc=dc1", a.Config.NodeName), nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthNodeChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be 1 health check for the server
	nodes = obj.(structs.HealthChecks)
	if len(nodes) != 1 {
		t.Fatalf("bad: %v", obj)
	}
}

func TestHealthNodeChecks_Filtering(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	// Create a node check
	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "test-health-node",
		Address:    "127.0.0.2",
		Check: &structs.HealthCheck{
			Node: "test-health-node",
			Name: "check1",
		},
	}

	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	// Create a second check
	args = &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "test-health-node",
		Address:    "127.0.0.2",
		Check: &structs.HealthCheck{
			Node: "test-health-node",
			Name: "check2",
		},
		SkipNodeUpdate: true,
	}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ := http.NewRequest("GET", "/v1/health/node/test-health-node?filter="+url.QueryEscape("Name == check2"), nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthNodeChecks(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	// Should be 1 health check for the server
	nodes := obj.(structs.HealthChecks)
	require.Len(t, nodes, 1)
}

func TestHealthServiceChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes := obj.(structs.HealthChecks)
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("bad: %v", obj)
	}

	// Create a service check
	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
			Type:      "grpc",
		},
	}

	var out struct{}
	if err = a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ = http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1", nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be 1 health check for consul
	nodes = obj.(structs.HealthChecks)
	if len(nodes) != 1 {
		t.Fatalf("bad: %v", obj)
	}
	if nodes[0].Type != "grpc" {
		t.Fatalf("expected grpc check type, got %s", nodes[0].Type)
	}
}

func TestHealthServiceChecks_NodeMetaFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1&node-meta=somekey:somevalue", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes := obj.(structs.HealthChecks)
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("bad: %v", obj)
	}

	// Create a service check
	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
		},
	}

	var out struct{}
	if err = a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ = http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1&node-meta=somekey:somevalue", nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)

	// Should be 1 health check for consul
	nodes = obj.(structs.HealthChecks)
	if len(nodes) != 1 {
		t.Fatalf("bad: %v", obj)
	}
}

func TestHealthServiceChecks_Filtering(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1&node-meta=somekey:somevalue", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceChecks(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes := obj.(structs.HealthChecks)
	require.Empty(t, nodes)

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
		},
		SkipNodeUpdate: true,
	}

	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	// Create a new node, service and check
	args = &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "test-health-node",
		Address:    "127.0.0.2",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Service: &structs.NodeService{
			ID:      "consul",
			Service: "consul",
		},
		Check: &structs.HealthCheck{
			Node:      "test-health-node",
			Name:      "consul check",
			ServiceID: "consul",
		},
	}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ = http.NewRequest("GET", "/v1/health/checks/consul?dc=dc1&filter="+url.QueryEscape("Node == `test-health-node`"), nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceChecks(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	// Should be 1 health check for consul
	nodes = obj.(structs.HealthChecks)
	require.Len(t, nodes, 1)
}

func TestHealthServiceChecks_DistanceSort(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	// Create a service check
	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		Service: &structs.NodeService{
			ID:      "test",
			Service: "test",
		},
		Check: &structs.HealthCheck{
			Node:      "bar",
			Name:      "test check",
			ServiceID: "test",
		},
	}

	var out struct{}
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	args.Node, args.Check.Node = "foo", "foo"
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ := http.NewRequest("GET", "/v1/health/checks/test?dc=dc1&near=foo", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceChecks(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)
	nodes := obj.(structs.HealthChecks)
	if len(nodes) != 2 {
		t.Fatalf("bad: %v", obj)
	}
	if nodes[0].Node != "bar" {
		t.Fatalf("bad: %v", nodes)
	}
	if nodes[1].Node != "foo" {
		t.Fatalf("bad: %v", nodes)
	}

	// Send an update for the node and wait for it to get applied.
	arg := structs.CoordinateUpdateRequest{
		Datacenter: "dc1",
		Node:       "foo",
		Coord:      coordinate.NewCoordinate(coordinate.DefaultConfig()),
	}
	if err := a.RPC("Coordinate.Update", &arg, &out); err != nil {
		t.Fatalf("err: %v", err)
	}
	// Retry until foo has moved to the front of the line.
	retry.Run(t, func(r *retry.R) {
		resp = httptest.NewRecorder()
		obj, err = a.srv.HealthServiceChecks(resp, req)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		assertIndex(t, resp)
		nodes = obj.(structs.HealthChecks)
		if len(nodes) != 2 {
			r.Fatalf("bad: %v", obj)
		}
		if nodes[0].Node != "foo" {
			r.Fatalf("bad: %v", nodes)
		}
		if nodes[1].Node != "bar" {
			r.Fatalf("bad: %v", nodes)
		}
	})
}

func TestHealthServiceNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/service/consul?dc=dc1", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceNodes(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	assertIndex(t, resp)

	// Should be 1 health check for consul
	nodes := obj.(structs.CheckServiceNodes)
	if len(nodes) != 1 {
		t.Fatalf("bad: %v", obj)
	}

	req, _ = http.NewRequest("GET", "/v1/health/service/nope?dc=dc1", nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceNodes(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes = obj.(structs.CheckServiceNodes)
	if nodes == nil || len(nodes) != 0 {
		t.Fatalf("bad: %v", obj)
	}

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "bar",
		Address:    "127.0.0.1",
		Service: &structs.NodeService{
			ID:      "test",
			Service: "test",
		},
	}

	var out struct{}
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ = http.NewRequest("GET", "/v1/health/service/test?dc=dc1", nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceNodes(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	assertIndex(t, resp)

	// Should be a non-nil empty list for checks
	nodes = obj.(structs.CheckServiceNodes)
	if len(nodes) != 1 || nodes[0].Checks == nil || len(nodes[0].Checks) != 0 {
		t.Fatalf("bad: %v", obj)
	}

	// Test caching
	{
		// List instances with cache enabled
		req, _ := http.NewRequest("GET", "/v1/health/service/test?cached", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthServiceNodes(resp, req)
		require.NoError(t, err)
		nodes := obj.(structs.CheckServiceNodes)
		assert.Len(t, nodes, 1)

		// Should be a cache miss
		assert.Equal(t, "MISS", resp.Header().Get("X-Cache"))
	}

	{
		// List instances with cache enabled
		req, _ := http.NewRequest("GET", "/v1/health/service/test?cached", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthServiceNodes(resp, req)
		require.NoError(t, err)
		nodes := obj.(structs.CheckServiceNodes)
		assert.Len(t, nodes, 1)

		// Should be a cache HIT now!
		assert.Equal(t, "HIT", resp.Header().Get("X-Cache"))
	}

	// Ensure background refresh works
	{
		// Register a new instance of the service
		args2 := args
		args2.Node = "baz"
		args2.Address = "127.0.0.2"
		require.NoError(t, a.RPC("Catalog.Register", args, &out))

		retry.Run(t, func(r *retry.R) {
			// List it again
			req, _ := http.NewRequest("GET", "/v1/health/service/test?cached", nil)
			resp := httptest.NewRecorder()
			obj, err := a.srv.HealthServiceNodes(resp, req)
			r.Check(err)

			nodes := obj.(structs.CheckServiceNodes)
			if len(nodes) != 2 {
				r.Fatalf("Want 2 nodes")
			}
			header := resp.Header().Get("X-Consul-Index")
			if header == "" || header == "0" {
				r.Fatalf("Want non-zero header: %q", header)
			}
			_, err = strconv.ParseUint(header, 10, 64)
			r.Check(err)

			// Should be a cache hit! The data should've updated in the cache
			// in the background so this should've been fetched directly from
			// the cache.
			if resp.Header().Get("X-Cache") != "HIT" {
				r.Fatalf("should be a cache hit")
			}
		})
	}
}

func TestHealthServiceNodes_Blocking(t *testing.T) {
	cases := []struct {
		name         string
		hcl          string
		grpcMetrics  bool
		queryBackend string
	}{
		{
			name:         "no streaming",
			queryBackend: "blocking-query",
			hcl:          `use_streaming_backend = false`,
		},
		{
			name:        "streaming",
			grpcMetrics: true,
			hcl: `
rpc { enable_streaming = true }
use_streaming_backend = true
`,
			queryBackend: "streaming",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			sink := metrics.NewInmemSink(5*time.Second, time.Minute)
			metrics.NewGlobal(&metrics.Config{
				ServiceName:     "testing",
				AllowedPrefixes: []string{"testing.grpc."},
			}, sink)

			a := NewTestAgent(t, tc.hcl)
			defer a.Shutdown()
			testrpc.WaitForTestAgent(t, a.RPC, "dc1")

			// Register some initial service instances
			for i := 0; i < 2; i++ {
				args := &structs.RegisterRequest{
					Datacenter: "dc1",
					Node:       "bar",
					Address:    "127.0.0.1",
					Service: &structs.NodeService{
						ID:      fmt.Sprintf("test%03d", i),
						Service: "test",
					},
				}

				var out struct{}
				require.NoError(t, a.RPC("Catalog.Register", args, &out))
			}

			// Initial request should return two instances
			req, _ := http.NewRequest("GET", "/v1/health/service/test?dc=dc1", nil)
			resp := httptest.NewRecorder()
			obj, err := a.srv.HealthServiceNodes(resp, req)
			require.NoError(t, err)

			nodes := obj.(structs.CheckServiceNodes)
			require.Len(t, nodes, 2)

			idx := getIndex(t, resp)
			require.True(t, idx > 0)

			// errCh collects errors from goroutines since it's unsafe for them to use
			// t to fail tests directly.
			errCh := make(chan error, 1)

			checkErrs := func() {
				// Ensure no errors were sent on errCh and drain any nils we have
				for {
					select {
					case err := <-errCh:
						require.NoError(t, err)
					default:
						return
					}
				}
			}

			// Blocking on that index should block. We test that by launching another
			// goroutine that will wait a while before updating the registration and
			// make sure that we unblock before timeout and see the update but that it
			// takes at least as long as the sleep time.
			sleep := 200 * time.Millisecond
			start := time.Now()
			go func() {
				time.Sleep(sleep)

				args := &structs.RegisterRequest{
					Datacenter: "dc1",
					Node:       "zoo",
					Address:    "127.0.0.3",
					Service: &structs.NodeService{
						ID:      "test",
						Service: "test",
					},
				}

				var out struct{}
				errCh <- a.RPC("Catalog.Register", args, &out)
			}()

			{
				timeout := 30 * time.Second
				url := fmt.Sprintf("/v1/health/service/test?dc=dc1&index=%d&wait=%s", idx, timeout)
				req, _ := http.NewRequest("GET", url, nil)
				resp := httptest.NewRecorder()
				obj, err := a.srv.HealthServiceNodes(resp, req)
				require.NoError(t, err)
				elapsed := time.Since(start)
				require.True(t, elapsed > sleep, "request should block for at "+
					" least as long as sleep. sleep=%s, elapsed=%s", sleep, elapsed)

				require.True(t, elapsed < timeout, "request should unblock before"+
					" it timed out. timeout=%s, elapsed=%s", timeout, elapsed)

				nodes := obj.(structs.CheckServiceNodes)
				require.Len(t, nodes, 3)

				newIdx := getIndex(t, resp)
				require.True(t, idx < newIdx, "index should have increased."+
					"idx=%d, newIdx=%d", idx, newIdx)

				require.Equal(t, tc.queryBackend, resp.Header().Get("X-Consul-Query-Backend"))

				idx = newIdx

				checkErrs()
			}

			// Blocking should last until timeout in absence of updates
			start = time.Now()
			{
				timeout := 200 * time.Millisecond
				url := fmt.Sprintf("/v1/health/service/test?dc=dc1&index=%d&wait=%s",
					idx, timeout)
				req, _ := http.NewRequest("GET", url, nil)
				resp := httptest.NewRecorder()
				obj, err := a.srv.HealthServiceNodes(resp, req)
				require.NoError(t, err)
				elapsed := time.Since(start)
				// Note that servers add jitter to timeout requested but don't remove it
				// so this should always be true.
				require.True(t, elapsed > timeout, "request should block for at "+
					" least as long as timeout. timeout=%s, elapsed=%s", timeout, elapsed)

				nodes := obj.(structs.CheckServiceNodes)
				require.Len(t, nodes, 3)

				newIdx := getIndex(t, resp)
				require.Equal(t, idx, newIdx)
				require.Equal(t, tc.queryBackend, resp.Header().Get("X-Consul-Query-Backend"))
			}

			if tc.grpcMetrics {
				data := sink.Data()
				if l := len(data); l < 1 {
					t.Errorf("expected at least 1 metrics interval, got :%v", l)
				}
				if count := len(data[0].Gauges); count < 2 {
					t.Errorf("expected at least 2 grpc gauge metrics, got: %v", count)
				}
			}
		})
	}
}

func TestHealthServiceNodes_NodeMetaFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()

	tests := []struct {
		name         string
		config       string
		queryBackend string
	}{
		{
			name:         "blocking-query",
			config:       `use_streaming_backend=false`,
			queryBackend: "blocking-query",
		},
		{
			name: "cache-with-streaming",
			config: `
			rpc{
				enable_streaming=true
			}
			use_streaming_backend=true
		    `,
			queryBackend: "streaming",
		},
	}
	for _, tst := range tests {
		t.Run(tst.name, func(t *testing.T) {

			a := NewTestAgent(t, tst.config)
			defer a.Shutdown()
			testrpc.WaitForLeader(t, a.RPC, "dc1")

			req, _ := http.NewRequest("GET", "/v1/health/service/consul?dc=dc1&node-meta=somekey:somevalue", nil)
			resp := httptest.NewRecorder()
			obj, err := a.srv.HealthServiceNodes(resp, req)
			if err != nil {
				t.Fatalf("err: %v", err)
			}

			assertIndex(t, resp)

			cIndex, err := strconv.ParseUint(resp.Header().Get("X-Consul-Index"), 10, 64)
			require.NoError(t, err)

			// Should be a non-nil empty list
			nodes := obj.(structs.CheckServiceNodes)
			if nodes == nil || len(nodes) != 0 {
				t.Fatalf("bad: %v", obj)
			}

			args := &structs.RegisterRequest{
				Datacenter: "dc1",
				Node:       "bar",
				Address:    "127.0.0.1",
				NodeMeta:   map[string]string{"somekey": "somevalue"},
				Service: &structs.NodeService{
					ID:      "test",
					Service: "test",
				},
			}

			var out struct{}
			if err := a.RPC("Catalog.Register", args, &out); err != nil {
				t.Fatalf("err: %v", err)
			}

			args = &structs.RegisterRequest{
				Datacenter: "dc1",
				Node:       "bar2",
				Address:    "127.0.0.1",
				NodeMeta:   map[string]string{"somekey": "othervalue"},
				Service: &structs.NodeService{
					ID:      "test2",
					Service: "test",
				},
			}

			if err := a.RPC("Catalog.Register", args, &out); err != nil {
				t.Fatalf("err: %v", err)
			}

			req, _ = http.NewRequest("GET", fmt.Sprintf("/v1/health/service/test?dc=dc1&node-meta=somekey:somevalue&index=%d&wait=10ms", cIndex), nil)
			resp = httptest.NewRecorder()
			obj, err = a.srv.HealthServiceNodes(resp, req)
			if err != nil {
				t.Fatalf("err: %v", err)
			}

			assertIndex(t, resp)

			// Should be a non-nil empty list for checks
			nodes = obj.(structs.CheckServiceNodes)
			if len(nodes) != 1 || nodes[0].Checks == nil || len(nodes[0].Checks) != 0 {
				t.Fatalf("bad: %v", obj)
			}

			require.Equal(t, tst.queryBackend, resp.Header().Get("X-Consul-Query-Backend"))
		})
	}
}

func TestHealthServiceNodes_Filter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/service/consul?dc=dc1&filter="+url.QueryEscape("Node.Node == `test-health-node`"), nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceNodes(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	// Should be a non-nil empty list
	nodes := obj.(structs.CheckServiceNodes)
	require.Empty(t, nodes)

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
		},
	}

	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	// Create a new node, service and check
	args = &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       "test-health-node",
		Address:    "127.0.0.2",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Service: &structs.NodeService{
			ID:      "consul",
			Service: "consul",
		},
		Check: &structs.HealthCheck{
			Node:      "test-health-node",
			Name:      "consul check",
			ServiceID: "consul",
		},
	}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ = http.NewRequest("GET", "/v1/health/service/consul?dc=dc1&filter="+url.QueryEscape("Node.Node == `test-health-node`"), nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceNodes(resp, req)
	require.NoError(t, err)

	assertIndex(t, resp)

	// Should be a list of checks with 1 element
	nodes = obj.(structs.CheckServiceNodes)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Checks, 1)
}

func TestHealthServiceNodes_DistanceSort(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	dc := "dc1"
	// Create a service check
	args := &structs.RegisterRequest{
		Datacenter: dc,
		Node:       "bar",
		Address:    "127.0.0.1",
		Service: &structs.NodeService{
			ID:      "test",
			Service: "test",
		},
		Check: &structs.HealthCheck{
			Node:      "bar",
			Name:      "test check",
			ServiceID: "test",
		},
	}
	testrpc.WaitForLeader(t, a.RPC, dc)
	var out struct{}
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	args.Node, args.Check.Node = "foo", "foo"
	if err := a.RPC("Catalog.Register", args, &out); err != nil {
		t.Fatalf("err: %v", err)
	}

	req, _ := http.NewRequest("GET", "/v1/health/service/test?dc=dc1&near=foo", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceNodes(resp, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	assertIndex(t, resp)
	nodes := obj.(structs.CheckServiceNodes)
	if len(nodes) != 2 {
		t.Fatalf("bad: %v", obj)
	}
	if nodes[0].Node.Node != "bar" {
		t.Fatalf("bad: %v", nodes)
	}
	if nodes[1].Node.Node != "foo" {
		t.Fatalf("bad: %v", nodes)
	}

	// Send an update for the node and wait for it to get applied.
	arg := structs.CoordinateUpdateRequest{
		Datacenter: "dc1",
		Node:       "foo",
		Coord:      coordinate.NewCoordinate(coordinate.DefaultConfig()),
	}
	if err := a.RPC("Coordinate.Update", &arg, &out); err != nil {
		t.Fatalf("err: %v", err)
	}
	// Retry until foo has moved to the front of the line.
	retry.Run(t, func(r *retry.R) {
		resp = httptest.NewRecorder()
		obj, err = a.srv.HealthServiceNodes(resp, req)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		assertIndex(t, resp)
		nodes = obj.(structs.CheckServiceNodes)
		if len(nodes) != 2 {
			r.Fatalf("bad: %v", obj)
		}
		if nodes[0].Node.Node != "foo" {
			r.Fatalf("bad: %v", nodes)
		}
		if nodes[1].Node.Node != "bar" {
			r.Fatalf("bad: %v", nodes)
		}
	})
}

func TestHealthServiceNodes_PassingFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()

	dc := "dc1"
	// Create a failing service check
	args := &structs.RegisterRequest{
		Datacenter: dc,
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
			Status:    api.HealthCritical,
		},
	}

	retry.Run(t, func(r *retry.R) {
		var out struct{}
		if err := a.RPC("Catalog.Register", args, &out); err != nil {
			r.Fatalf("err: %v", err)
		}
	})

	t.Run("bc_no_query_value", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/v1/health/service/consul?passing", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthServiceNodes(resp, req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		assertIndex(t, resp)

		// Should be 0 health check for consul
		nodes := obj.(structs.CheckServiceNodes)
		if len(nodes) != 0 {
			t.Fatalf("bad: %v", obj)
		}
	})

	t.Run("passing_true", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/v1/health/service/consul?passing=true", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthServiceNodes(resp, req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		assertIndex(t, resp)

		// Should be 0 health check for consul
		nodes := obj.(structs.CheckServiceNodes)
		if len(nodes) != 0 {
			t.Fatalf("bad: %v", obj)
		}
	})

	t.Run("passing_false", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/v1/health/service/consul?passing=false", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthServiceNodes(resp, req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		assertIndex(t, resp)

		// Should be 1 consul, it's unhealthy, but we specifically asked for
		// everything.
		nodes := obj.(structs.CheckServiceNodes)
		if len(nodes) != 1 {
			t.Fatalf("bad: %v", obj)
		}
	})

	t.Run("passing_bad", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/v1/health/service/consul?passing=nope-nope-nope", nil)
		resp := httptest.NewRecorder()
		_, err := a.srv.HealthServiceNodes(resp, req)
		if _, ok := err.(BadRequestError); !ok {
			t.Fatalf("Expected bad request error but got %v", err)
		}
		if !strings.Contains(err.Error(), "Invalid value for ?passing") {
			t.Errorf("bad %s", err.Error())
		}
	})
}

func TestHealthServiceNodes_CheckType(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForTestAgent(t, a.RPC, "dc1")

	req, _ := http.NewRequest("GET", "/v1/health/service/consul?dc=dc1", nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthServiceNodes(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	// Should be 1 health check for consul
	nodes := obj.(structs.CheckServiceNodes)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}

	args := &structs.RegisterRequest{
		Datacenter: "dc1",
		Node:       a.Config.NodeName,
		Address:    "127.0.0.1",
		NodeMeta:   map[string]string{"somekey": "somevalue"},
		Check: &structs.HealthCheck{
			Node:      a.Config.NodeName,
			Name:      "consul check",
			ServiceID: "consul",
			Type:      "grpc",
		},
	}

	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ = http.NewRequest("GET", "/v1/health/service/consul?dc=dc1", nil)
	resp = httptest.NewRecorder()
	obj, err = a.srv.HealthServiceNodes(resp, req)
	require.NoError(t, err)

	assertIndex(t, resp)

	// Should be a non-nil empty list for checks
	nodes = obj.(structs.CheckServiceNodes)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Checks, 2)

	for _, check := range nodes[0].Checks {
		if check.Name == "consul check" && check.Type != "grpc" {
			t.Fatalf("exptected grpc check type, got %s", check.Type)
		}
	}
}

func TestHealthServiceNodes_WanTranslation(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	a1 := NewTestAgent(t, `
		datacenter = "dc1"
		translate_wan_addrs = true
		acl_datacenter = ""
	`)
	defer a1.Shutdown()

	a2 := NewTestAgent(t, `
		datacenter = "dc2"
		translate_wan_addrs = true
		acl_datacenter = ""
	`)
	defer a2.Shutdown()

	// Wait for the WAN join.
	addr := fmt.Sprintf("127.0.0.1:%d", a1.Config.SerfPortWAN)
	_, err := a2.srv.agent.JoinWAN([]string{addr})
	require.NoError(t, err)
	retry.Run(t, func(r *retry.R) {
		require.Len(r, a1.WANMembers(), 2)
	})

	// Register a node with DC2.
	{
		args := &structs.RegisterRequest{
			Datacenter: "dc2",
			Node:       "foo",
			Address:    "127.0.0.1",
			TaggedAddresses: map[string]string{
				"wan": "127.0.0.2",
			},
			Service: &structs.NodeService{
				Service: "http_wan_translation_test",
				Address: "127.0.0.1",
				Port:    8080,
				TaggedAddresses: map[string]structs.ServiceAddress{
					"wan": {
						Address: "1.2.3.4",
						Port:    80,
					},
				},
			},
		}

		var out struct{}
		require.NoError(t, a2.RPC("Catalog.Register", args, &out))
	}

	// Query for a service in DC2 from DC1.
	req, _ := http.NewRequest("GET", "/v1/health/service/http_wan_translation_test?dc=dc2", nil)
	resp1 := httptest.NewRecorder()
	obj1, err1 := a1.srv.HealthServiceNodes(resp1, req)
	require.NoError(t, err1)
	require.NoError(t, checkIndex(resp1))

	// Expect that DC1 gives us a WAN address (since the node is in DC2).
	nodes1, ok := obj1.(structs.CheckServiceNodes)
	require.True(t, ok, "obj1 is not a structs.CheckServiceNodes")
	require.Len(t, nodes1, 1)
	node1 := nodes1[0]
	require.NotNil(t, node1.Node)
	require.Equal(t, node1.Node.Address, "127.0.0.2")
	require.NotNil(t, node1.Service)
	require.Equal(t, node1.Service.Address, "1.2.3.4")
	require.Equal(t, node1.Service.Port, 80)

	// Query DC2 from DC2.
	resp2 := httptest.NewRecorder()
	obj2, err2 := a2.srv.HealthServiceNodes(resp2, req)
	require.NoError(t, err2)
	require.NoError(t, checkIndex(resp2))

	// Expect that DC2 gives us a local address (since the node is in DC2).
	nodes2, ok := obj2.(structs.CheckServiceNodes)
	require.True(t, ok, "obj2 is not a structs.ServiceNodes")
	require.Len(t, nodes2, 1)
	node2 := nodes2[0]
	require.NotNil(t, node2.Node)
	require.Equal(t, node2.Node.Address, "127.0.0.1")
	require.NotNil(t, node2.Service)
	require.Equal(t, node2.Service.Address, "127.0.0.1")
	require.Equal(t, node2.Service.Port, 8080)
}

func TestHealthConnectServiceNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()

	a := NewTestAgent(t, "")
	defer a.Shutdown()

	// Register
	args := structs.TestRegisterRequestProxy(t)
	var out struct{}
	assert.Nil(t, a.RPC("Catalog.Register", args, &out))

	// Request
	req, _ := http.NewRequest("GET", fmt.Sprintf(
		"/v1/health/connect/%s?dc=dc1", args.Service.Proxy.DestinationServiceName), nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthConnectServiceNodes(resp, req)
	assert.Nil(t, err)
	assertIndex(t, resp)

	// Should be a non-nil empty list for checks
	nodes := obj.(structs.CheckServiceNodes)
	assert.Len(t, nodes, 1)
	assert.Len(t, nodes[0].Checks, 0)
}

func TestHealthIngressServiceNodes(t *testing.T) {
	t.Run("no streaming", func(t *testing.T) {
		testHealthIngressServiceNodes(t, ` rpc { enable_streaming = false } use_streaming_backend = false `)
	})
	t.Run("cache with streaming", func(t *testing.T) {
		testHealthIngressServiceNodes(t, ` rpc { enable_streaming = true } use_streaming_backend = true `)
	})
}

func testHealthIngressServiceNodes(t *testing.T, agentHCL string) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	a := NewTestAgent(t, agentHCL)
	defer a.Shutdown()
	testrpc.WaitForLeader(t, a.RPC, "dc1")

	// Register gateway
	gatewayArgs := structs.TestRegisterIngressGateway(t)
	gatewayArgs.Service.Address = "127.0.0.27"
	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", gatewayArgs, &out))

	args := structs.TestRegisterRequest(t)
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	// Associate service to gateway
	cfgArgs := &structs.IngressGatewayConfigEntry{
		Name: "ingress-gateway",
		Kind: structs.IngressGateway,
		Listeners: []structs.IngressListener{
			{
				Port:     8888,
				Protocol: "tcp",
				Services: []structs.IngressService{
					{Name: args.Service.Service},
				},
			},
		},
	}

	req := structs.ConfigEntryRequest{
		Op:         structs.ConfigEntryUpsert,
		Datacenter: "dc1",
		Entry:      cfgArgs,
	}
	var outB bool
	require.Nil(t, a.RPC("ConfigEntry.Apply", req, &outB))
	require.True(t, outB)

	checkResults := func(t *testing.T, obj interface{}) {
		nodes := obj.(structs.CheckServiceNodes)
		require.Len(t, nodes, 1)
		require.Equal(t, structs.ServiceKindIngressGateway, nodes[0].Service.Kind)
		require.Equal(t, gatewayArgs.Service.Address, nodes[0].Service.Address)
		require.Equal(t, gatewayArgs.Service.Proxy, nodes[0].Service.Proxy)
	}

	require.True(t, t.Run("associated service", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/ingress/%s", args.Service.Service), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthIngressServiceNodes(resp, req)
		require.NoError(t, err)
		assertIndex(t, resp)

		checkResults(t, obj)
	}))

	require.True(t, t.Run("non-associated service", func(t *testing.T) {
		req, _ := http.NewRequest("GET",
			"/v1/health/connect/notexist", nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthIngressServiceNodes(resp, req)
		require.NoError(t, err)
		assertIndex(t, resp)

		nodes := obj.(structs.CheckServiceNodes)
		require.Len(t, nodes, 0)
	}))

	require.True(t, t.Run("test caching miss", func(t *testing.T) {
		// List instances with cache enabled
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/ingress/%s?cached", args.Service.Service), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthIngressServiceNodes(resp, req)
		require.NoError(t, err)

		checkResults(t, obj)

		// Should be a cache miss
		require.Equal(t, "MISS", resp.Header().Get("X-Cache"))
		// always a blocking query, because the ingress endpoint does not yet support streaming.
		require.Equal(t, "blocking-query", resp.Header().Get("X-Consul-Query-Backend"))
	}))

	require.True(t, t.Run("test caching hit", func(t *testing.T) {
		// List instances with cache enabled
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/ingress/%s?cached", args.Service.Service), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthIngressServiceNodes(resp, req)
		require.NoError(t, err)

		checkResults(t, obj)

		// Should be a cache HIT now!
		require.Equal(t, "HIT", resp.Header().Get("X-Cache"))
		// always a blocking query, because the ingress endpoint does not yet support streaming.
		require.Equal(t, "blocking-query", resp.Header().Get("X-Consul-Query-Backend"))
	}))
}

func TestHealthConnectServiceNodes_Filter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()

	a := NewTestAgent(t, "")
	defer a.Shutdown()
	testrpc.WaitForLeader(t, a.RPC, "dc1")

	// Register
	args := structs.TestRegisterRequestProxy(t)
	args.Service.Address = "127.0.0.55"
	var out struct{}
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	args = structs.TestRegisterRequestProxy(t)
	args.Service.Address = "127.0.0.55"
	args.Service.Meta = map[string]string{
		"version": "2",
	}
	args.Service.ID = "web-proxy2"
	args.SkipNodeUpdate = true
	require.NoError(t, a.RPC("Catalog.Register", args, &out))

	req, _ := http.NewRequest("GET", fmt.Sprintf(
		"/v1/health/connect/%s?filter=%s",
		args.Service.Proxy.DestinationServiceName,
		url.QueryEscape("Service.Meta.version == 2")), nil)
	resp := httptest.NewRecorder()
	obj, err := a.srv.HealthConnectServiceNodes(resp, req)
	require.NoError(t, err)
	assertIndex(t, resp)

	nodes := obj.(structs.CheckServiceNodes)
	require.Len(t, nodes, 1)
	require.Equal(t, structs.ServiceKindConnectProxy, nodes[0].Service.Kind)
	require.Equal(t, args.Service.Address, nodes[0].Service.Address)
	require.Equal(t, args.Service.Proxy, nodes[0].Service.Proxy)
}

func TestHealthConnectServiceNodes_PassingFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()

	a := NewTestAgent(t, "")
	defer a.Shutdown()

	// Register
	args := structs.TestRegisterRequestProxy(t)
	args.Check = &structs.HealthCheck{
		Node:      args.Node,
		Name:      "check",
		ServiceID: args.Service.Service,
		Status:    api.HealthCritical,
	}
	var out struct{}
	assert.Nil(t, a.RPC("Catalog.Register", args, &out))

	t.Run("bc_no_query_value", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/connect/%s?passing", args.Service.Proxy.DestinationServiceName), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthConnectServiceNodes(resp, req)
		assert.Nil(t, err)
		assertIndex(t, resp)

		// Should be 0 health check for consul
		nodes := obj.(structs.CheckServiceNodes)
		assert.Len(t, nodes, 0)
	})

	t.Run("passing_true", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/connect/%s?passing=true", args.Service.Proxy.DestinationServiceName), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthConnectServiceNodes(resp, req)
		assert.Nil(t, err)
		assertIndex(t, resp)

		// Should be 0 health check for consul
		nodes := obj.(structs.CheckServiceNodes)
		assert.Len(t, nodes, 0)
	})

	t.Run("passing_false", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/connect/%s?passing=false", args.Service.Proxy.DestinationServiceName), nil)
		resp := httptest.NewRecorder()
		obj, err := a.srv.HealthConnectServiceNodes(resp, req)
		assert.Nil(t, err)
		assertIndex(t, resp)

		// Should be 1
		nodes := obj.(structs.CheckServiceNodes)
		assert.Len(t, nodes, 1)
	})

	t.Run("passing_bad", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf(
			"/v1/health/connect/%s?passing=nope-nope", args.Service.Proxy.DestinationServiceName), nil)
		resp := httptest.NewRecorder()
		_, err := a.srv.HealthConnectServiceNodes(resp, req)
		assert.NotNil(t, err)
		_, ok := err.(BadRequestError)
		assert.True(t, ok)

		assert.True(t, strings.Contains(err.Error(), "Invalid value for ?passing"))
	})
}

func TestFilterNonPassing(t *testing.T) {
	t.Parallel()
	nodes := structs.CheckServiceNodes{
		structs.CheckServiceNode{
			Checks: structs.HealthChecks{
				&structs.HealthCheck{
					Status: api.HealthCritical,
				},
				&structs.HealthCheck{
					Status: api.HealthCritical,
				},
			},
		},
		structs.CheckServiceNode{
			Checks: structs.HealthChecks{
				&structs.HealthCheck{
					Status: api.HealthCritical,
				},
				&structs.HealthCheck{
					Status: api.HealthCritical,
				},
			},
		},
		structs.CheckServiceNode{
			Checks: structs.HealthChecks{
				&structs.HealthCheck{
					Status: api.HealthPassing,
				},
			},
		},
	}
	out := filterNonPassing(nodes)
	if len(out) != 1 && reflect.DeepEqual(out[0], nodes[2]) {
		t.Fatalf("bad: %v", out)
	}
}
