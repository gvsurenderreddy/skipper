// Copyright 2015 Zalando SE
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxy

import (
	"bytes"
	"fmt"
	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/filters/builtin"
	"github.com/zalando/skipper/routing"
	"github.com/zalando/skipper/routing/testdataclient"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

const (
	streamingDelay    time.Duration = 3 * time.Millisecond
	sourcePollTimeout time.Duration = 6 * time.Millisecond
)

type requestCheck func(*http.Request)

type priorityRoute struct {
	route  *routing.Route
	params map[string]string
	match  func(r *http.Request) bool
}

type (
	preserveOriginalSpec   struct{}
	preserveOriginalFilter struct{}
)

func (cors *preserveOriginalSpec) Name() string { return "preserveOriginal" }

func (cors *preserveOriginalSpec) CreateFilter(_ []interface{}) (filters.Filter, error) {
	return &preserveOriginalFilter{}, nil
}

func preserveHeader(from, to http.Header) {
	for key, vals := range from {
		to[key+"-Preserved"] = vals
	}
}

func (corf *preserveOriginalFilter) Request(ctx filters.FilterContext) {
	preserveHeader(ctx.OriginalRequest().Header, ctx.Request().Header)
}

func (corf *preserveOriginalFilter) Response(ctx filters.FilterContext) {
	preserveHeader(ctx.OriginalResponse().Header, ctx.Response().Header)
}

func (prt *priorityRoute) Match(r *http.Request) (*routing.Route, map[string]string) {
	if prt.match(r) {
		return prt.route, prt.params
	}

	return nil, nil
}

func voidCheck(*http.Request) {}

func writeParts(w io.Writer, parts int, data []byte) {
	partSize := len(data) / parts
	for i := 0; i < len(data); i += partSize {
		w.Write(data[i : i+partSize])
		time.Sleep(streamingDelay)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	w.Write(data[:len(data)-len(data)%parts])
}

func startTestServer(payload []byte, parts int, check requestCheck) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check(r)

		w.Header().Set("X-Test-Response-Header", "response header value")

		if len(payload) <= 0 {
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)

		if parts > 0 {
			writeParts(w, parts, payload)
			return
		}

		w.Write(payload)
	}))
}

// used to let the data client updates be propagated
func delay() { time.Sleep(24 * time.Millisecond) }

func TestGetRoundtrip(t *testing.T) {
	payload := []byte("Hello World!")

	s := startTestServer(payload, 0, func(r *http.Request) {
		if r.Method != "GET" {
			t.Error("wrong request method")
		}

		if th, ok := r.Header["X-Test-Header"]; !ok || th[0] != "test value" {
			t.Error("wrong request header")
		}
	})

	defer s.Close()

	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "GET",
		Header: http.Header{"X-Test-Header": []string{"test value"}}}
	w := httptest.NewRecorder()

	doc := fmt.Sprintf(`hello: Path("/hello") -> "%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	p.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Error("wrong status", w.Code)
	}

	if ct, ok := w.Header()["Content-Type"]; !ok || ct[0] != "text/plain" {
		t.Errorf("wrong content type. Expected 'text/plain' but got '%s'", w.Header().Get("Content-Type"))
	}

	if cl, ok := w.Header()["Content-Length"]; !ok || cl[0] != strconv.Itoa(len(payload)) {
		t.Error("wrong content length")
	}

	if xpb, ok := w.Header()["X-Powered-By"]; !ok || xpb[0] != "Skipper" {
		t.Error("Wrong X-Powered-By header value")
	}

	if xpb, ok := w.Header()["Server"]; !ok || xpb[0] != "Skipper" {
		t.Error("Wrong Server header value")
	}

	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Error("wrong content", string(w.Body.Bytes()))
	}
}

func TestPostRoundtrip(t *testing.T) {
	s := startTestServer(nil, 0, func(r *http.Request) {
		if r.Method != "POST" {
			t.Error("wrong request method", r.Method)
		}

		if th, ok := r.Header["X-Test-Header"]; !ok || th[0] != "test value" {
			t.Error("wrong request header")
		}
	})
	defer s.Close()

	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "POST",
		Header: http.Header{"X-Test-Header": []string{"test value"}}}
	w := httptest.NewRecorder()

	doc := fmt.Sprintf(`hello: Path("/hello") -> "%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	p.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Error("wrong status", w.Code)
	}

	if w.Body.Len() != 0 {
		t.Error("wrong content", string(w.Body.Bytes()))
	}
}

func TestRoute(t *testing.T) {
	payload1 := []byte("host one")
	s1 := startTestServer(payload1, 0, voidCheck)
	defer s1.Close()

	payload2 := []byte("host two")
	s2 := startTestServer(payload2, 0, voidCheck)
	defer s2.Close()

	doc := fmt.Sprintf(`
		route1: Path("/host-one/*any") -> "%s";
		route2: Path("/host-two/*any") -> "%s"
	`, s1.URL, s2.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	var (
		r *http.Request
		w *httptest.ResponseRecorder
		u *url.URL
	)

	u, _ = url.ParseRequestURI("https://www.example.org/host-one/some/path")
	r = &http.Request{
		URL:    u,
		Method: "GET"}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), payload1) {
		t.Error("wrong routing 1")
	}

	u, _ = url.ParseRequestURI("https://www.example.org/host-two/some/path")
	r = &http.Request{
		URL:    u,
		Method: "GET"}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), payload2) {
		t.Error("wrong routing 2")
	}
}

func TestStreaming(t *testing.T) {
	const expectedParts = 3

	payload := []byte("some data to stream")
	s := startTestServer(payload, expectedParts, voidCheck)
	defer s.Close()

	doc := fmt.Sprintf(`hello: Path("/hello") -> "%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "GET"}
	w := httptest.NewRecorder()

	parts := 0
	total := 0
	done := make(chan int)
	go p.ServeHTTP(w, r)
	go func() {
		for {
			buf := w.Body.Bytes()

			if len(buf) == 0 {
				time.Sleep(streamingDelay)
				continue
			}

			parts++
			total += len(buf)

			if total >= len(payload) {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
		if parts <= expectedParts {
			t.Error("streaming failed", parts)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("streaming timeout")
	}
}

func TestAppliesFilters(t *testing.T) {
	payload := []byte("Hello World!")

	s := startTestServer(payload, 0, func(r *http.Request) {
		if h, ok := r.Header["X-Test-Request-Header"]; !ok ||
			h[0] != "request header value" {
			t.Error("request header is missing")
		}
	})
	defer s.Close()

	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "GET",
		Header: http.Header{"X-Test-Header": []string{"test value"}}}
	w := httptest.NewRecorder()

	fr := make(filters.Registry)
	fr.Register(builtin.NewRequestHeader())
	fr.Register(builtin.NewResponseHeader())

	doc := fmt.Sprintf(`hello:
		Path("/hello") ->
		requestHeader("X-Test-Request-Header", "request header value") ->
		responseHeader("X-Test-Response-Header", "response header value") ->
		"%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			fr,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	p.ServeHTTP(w, r)

	if h, ok := w.Header()["X-Test-Response-Header"]; !ok || h[0] != "response header value" {
		t.Error("missing response header")
	}
}

type breaker struct {
	resp *http.Response
}

func (b *breaker) Request(c filters.FilterContext)                       { c.Serve(b.resp) }
func (_ *breaker) Response(filters.FilterContext)                        {}
func (b *breaker) CreateFilter(fc []interface{}) (filters.Filter, error) { return b, nil }
func (_ *breaker) Name() string                                          { return "breaker" }

func TestBreakFilterChain(t *testing.T) {
	s := startTestServer([]byte("Hello World!"), 0, func(r *http.Request) {
		t.Error("This should never be called")
	})
	defer s.Close()

	fr := make(filters.Registry)
	fr.Register(builtin.NewRequestHeader())
	resp1 := &http.Response{
		Header:     make(http.Header),
		Body:       ioutil.NopCloser(new(bytes.Buffer)),
		StatusCode: http.StatusUnauthorized,
		Status:     "Impossible body",
	}
	fr.Register(&breaker{resp1})
	fr.Register(builtin.NewResponseHeader())

	doc := fmt.Sprintf(`breakerDemo:
		Path("/breaker") ->
		requestHeader("X-Expected", "request header") ->
		responseHeader("X-Expected", "response header") ->
		breaker() ->
		requestHeader("X-Unexpected", "foo") ->
		responseHeader("X-Unexpected", "bar") ->
		"%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			fr,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	r, _ := http.NewRequest("GET", "https://www.example.org/breaker", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)

	if _, has := r.Header["X-Expected"]; !has {
		t.Error("Request is missing the expected header (added during filter chain winding)")
	}

	if _, has := w.Header()["X-Expected"]; !has {
		t.Error("Response is missing the expected header (added during filter chain unwinding)")
	}

	if _, has := r.Header["X-Unexpected"]; has {
		t.Error("Request has an unexpected header from a filter after the breaker in the chain")
	}

	if _, has := w.Header()["X-Unexpected"]; has {
		t.Error("Response has an unexpected header from a filter after the breaker in the chain")
	}

	if w.Code != http.StatusUnauthorized && w.Body.String() != "Impossible body" {
		t.Errorf("Wrong status code/body. Expected 401 - Impossible body but got %d - %s", w.Code, w.Body.String())
	}
}

func TestProcessesRequestWithShuntBackend(t *testing.T) {
	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "GET",
		Header: http.Header{"X-Test-Header": []string{"test value"}}}
	w := httptest.NewRecorder()

	fr := make(filters.Registry)
	fr.Register(builtin.NewResponseHeader())

	doc := `hello: Path("/hello") -> responseHeader("X-Test-Response-Header", "response header value") -> <shunt>`
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			fr,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	p.ServeHTTP(w, r)

	if h, ok := w.Header()["X-Test-Response-Header"]; !ok || h[0] != "response header value" {
		t.Error("wrong response header")
	}
}

func TestProcessesRequestWithPriorityRoute(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Header", "test-value")
	}))
	defer s.Close()

	req, err := http.NewRequest(
		"GET",
		"https://example.org",
		nil)
	if err != nil {
		t.Error(err)
	}

	u, err := url.Parse(s.URL)
	if err != nil {
		t.Error(err)
	}

	prt := &priorityRoute{&routing.Route{Scheme: u.Scheme, Host: u.Host}, nil, func(r *http.Request) bool {
		return r == req
	}}

	doc := `hello: Path("/hello") -> <shunt>`
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags:          FlagsNone,
		PriorityRoutes: []PriorityRoute{prt}})

	delay()

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Header().Get("X-Test-Header") != "test-value" {
		t.Error("failed match priority route")
	}
}

func TestProcessesRequestWithPriorityRouteOverStandard(t *testing.T) {
	s0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Header", "priority-value")
	}))
	defer s0.Close()

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Header", "normal-value")
	}))
	defer s0.Close()

	req, err := http.NewRequest(
		"GET",
		"https://example.org/hello/world",
		nil)
	if err != nil {
		t.Error(err)
	}

	u, err := url.Parse(s0.URL)
	if err != nil {
		t.Error(err)
	}

	prt := &priorityRoute{&routing.Route{Scheme: u.Scheme, Host: u.Host}, nil, func(r *http.Request) bool {
		return r == req
	}}

	doc := fmt.Sprintf(`hello: Path("/hello") -> "%s"`, s1.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags:          FlagsNone,
		PriorityRoutes: []PriorityRoute{prt}})

	delay()

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Header().Get("X-Test-Header") != "priority-value" {
		t.Error("failed match priority route")
	}
}

func TestFlusherImplementation(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, "))
		time.Sleep(15 * time.Millisecond)
		w.Write([]byte("world!"))
	})

	ts := httptest.NewServer(h)
	defer ts.Close()

	doc := fmt.Sprintf(`* -> "%s"`, ts.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			nil,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: FlagsNone})

	delay()

	a := fmt.Sprintf(":%d", 1<<16-rand.Intn(1<<15))
	ps := &http.Server{Addr: a, Handler: p}
	go ps.ListenAndServe()

	// let the server start listening
	time.Sleep(15 * time.Millisecond)

	rsp, err := http.Get("http://127.0.0.1" + a)
	if err != nil {
		t.Error(err)
		return
	}
	defer rsp.Body.Close()
	b, err := ioutil.ReadAll(rsp.Body)
	if err != nil {
		t.Error(err)
		return
	}
	if string(b) != "Hello, world!" {
		t.Error("failed to receive response")
	}
}

func TestOriginalRequestResponse(t *testing.T) {
	s := startTestServer(nil, 0, func(r *http.Request) {
		if th, ok := r.Header["X-Test-Header-Preserved"]; !ok || th[0] != "test value" {
			t.Error("wrong request header")
		}
	})

	defer s.Close()

	u, _ := url.ParseRequestURI("https://www.example.org/hello")
	r := &http.Request{
		URL:    u,
		Method: "GET",
		Header: http.Header{"X-Test-Header": []string{"test value"}}}
	w := httptest.NewRecorder()

	doc := fmt.Sprintf(`hello: Path("/hello") -> preserveOriginal() -> "%s"`, s.URL)
	dc, err := testdataclient.NewDoc(doc)
	if err != nil {
		t.Error(err)
	}

	fr := builtin.MakeRegistry()
	fr.Register(&preserveOriginalSpec{})
	p := WithParams(Params{
		Routing: routing.New(routing.Options{
			fr,
			routing.MatchingOptionsNone,
			sourcePollTimeout,
			[]routing.DataClient{dc},
			nil,
			0}),
		Flags: PreserveOriginal})

	delay()

	p.ServeHTTP(w, r)

	if th, ok := w.Header()["X-Test-Response-Header-Preserved"]; !ok || th[0] != "response header value" {
		t.Error("wrong response header", ok)
	}
}

func TestHostHeader(t *testing.T) {
	// start a test backend that returns the received host header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Received-Host", r.Host)
	}))
	defer backend.Close()

	// take the generated host part of the backend
	bu, err := url.Parse(backend.URL)
	if err != nil {
		t.Error("failed to parse test backend url")
		return
	}
	backendHost := bu.Host

	for _, ti := range []struct {
		msg          string
		flags        Flags
		routeFmt     string
		incomingHost string
		expectedHost string
	}{{
		"no proxy preserve",
		FlagsNone,
		`route: Any() -> "%s"`,
		"www.example.org",
		backendHost,
	}, {
		"no proxy preserve, route preserve not",
		FlagsNone,
		`route: Any() -> preserveHost("false") -> "%s"`,
		"www.example.org",
		backendHost,
	}, {
		"no proxy preserve, route preserve",
		FlagsNone,
		`route: Any() -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"www.example.org",
	}, {
		"no proxy preserve, route preserve not, explicit host last",
		FlagsNone,
		`route: Any() -> preserveHost("false") -> requestHeader("Host", "custom.example.org") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"no proxy preserve, route preserve, explicit host last",
		FlagsNone,
		`route: Any() -> preserveHost("true") -> requestHeader("Host", "custom.example.org") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"no proxy preserve, route preserve not, explicit host first",
		FlagsNone,
		`route: Any() -> requestHeader("Host", "custom.example.org") -> preserveHost("false") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"no proxy preserve, route preserve, explicit host last",
		FlagsNone,
		`route: Any() -> requestHeader("Host", "custom.example.org") -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"proxy preserve",
		PreserveHost,
		`route: Any() -> "%s"`,
		"www.example.org",
		"www.example.org",
	}, {
		"proxy preserve, route preserve not",
		PreserveHost,
		`route: Any() -> preserveHost("false") -> "%s"`,
		"www.example.org",
		backendHost,
	}, {
		"proxy preserve, route preserve",
		PreserveHost,
		`route: Any() -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"www.example.org",
	}, {
		"proxy preserve, route preserve not, explicit host last",
		PreserveHost,
		`route: Any() -> preserveHost("false") -> requestHeader("Host", "custom.example.org") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"proxy preserve, route preserve, explicit host last",
		PreserveHost,
		`route: Any() -> preserveHost("true") -> requestHeader("Host", "custom.example.org") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"proxy preserve, route preserve not, explicit host first",
		PreserveHost,
		`route: Any() -> requestHeader("Host", "custom.example.org") -> preserveHost("false") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"proxy preserve, route preserve, explicit host last",
		PreserveHost,
		`route: Any() -> requestHeader("Host", "custom.example.org") -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}, {
		"debug proxy, route not found",
		PreserveHost | Debug,
		`route: Path("/hello") -> requestHeader("Host", "custom.example.org") -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"",
	}, {
		"debug proxy, shunt route",
		PreserveHost | Debug,
		`route: Any() -> <shunt>`,
		"www.example.org",
		"",
	}, {
		"debug proxy, full circle",
		PreserveHost | Debug,
		`route: Any() -> requestHeader("Host", "custom.example.org") -> preserveHost("true") -> "%s"`,
		"www.example.org",
		"custom.example.org",
	}} {
		// replace the host in the route format
		f := ti.routeFmt + `;healthcheck: Path("/healthcheck") -> "%s"`
		route := fmt.Sprintf(f, backend.URL, backend.URL)

		// create a dataclient with the route
		dc, err := testdataclient.NewDoc(route)
		if err != nil {
			t.Error(ti.msg, "failed to parse route")
			continue
		}

		// start a proxy server
		r := routing.New(routing.Options{
			FilterRegistry:  builtin.MakeRegistry(),
			MatchingOptions: routing.MatchingOptionsNone,
			PollTimeout:     42 * time.Microsecond,
			DataClients:     []routing.DataClient{dc}})
		ps := httptest.NewServer(WithParams(Params{Routing: r, Flags: ti.flags}))

		// wait for the routing table was activated
		healthcheckDone := make(chan struct{})
		go func() {
			for {
				rs, _ := http.Get(ps.URL + "/healthcheck")
				if rs != nil &&
					rs.StatusCode >= http.StatusOK &&
					rs.StatusCode < http.StatusMultipleChoices {
					healthcheckDone <- struct{}{}
					return
				}
			}
		}()
		timeouted := false
		select {
		case <-time.After(999 * time.Millisecond):
			timeouted = true
		case <-healthcheckDone:
		}
		if timeouted {
			t.Error(ti.msg, "startup timeout")
			ps.Close()
			continue
		}

		req, err := http.NewRequest("GET", ps.URL, nil)
		if err != nil {
			t.Error(ti.msg, err)
			ps.Close()
			continue
		}

		req.Host = ti.incomingHost
		rsp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Error(ti.msg, "failed to make request")
			ps.Close()
			continue
		}

		if ti.flags.Debug() {
			ps.Close()
			return
		}

		if rsp.Header.Get("X-Received-Host") != ti.expectedHost {
			t.Error(ti.msg, "wrong host", rsp.Header.Get("X-Received-Host"), ti.expectedHost)
		}

		ps.Close()
	}
}
