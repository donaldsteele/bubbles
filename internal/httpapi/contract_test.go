package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// specPath is docs/openapi.yaml relative to this package.
var specPath = filepath.Join("..", "..", "docs", "openapi.yaml")

var (
	specOnce   sync.Once
	specDoc    *openapi3.T
	specRouter routers.Router
	specErr    error
)

// loadSpec loads and validates docs/openapi.yaml once per test binary.
func loadSpec(t testing.TB) (*openapi3.T, routers.Router) {
	t.Helper()
	specOnce.Do(func() {
		loader := openapi3.NewLoader()
		specDoc, specErr = loader.LoadFromFile(specPath)
		if specErr != nil {
			return
		}
		if specErr = specDoc.Validate(context.Background()); specErr != nil {
			return
		}
		specRouter, specErr = legacy.NewRouter(specDoc)
	})
	if specErr != nil {
		t.Fatalf("docs/openapi.yaml: %v", specErr)
	}
	return specDoc, specRouter
}

// contractHandler wraps h so that every request/response pair that passes
// through it is validated against docs/openapi.yaml; violations fail t.
//
//   - A request the spec describes and accepts must get a response the spec
//     allows for that operation (status, headers, body schema), and the status
//     must be listed for the operation (only 5xx may fall back to `default`).
//   - A request that violates the spec must be refused with a 4xx error.
//   - A path under /api/ the spec does not describe must answer the JSON 404.
//   - Every API answer is Cache-Control: no-store and carries X-Request-Id; the
//     one exception is the content of a file (GET /files/{id}/{name}), which is
//     cached privately and immutably.
//   - Paths outside the API (the SPA) are not checked.
func contractHandler(t testing.TB, h http.Handler) http.Handler {
	doc, router := loadSpec(t)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody []byte
		if r.Body != nil {
			reqBody, _ = io.ReadAll(r.Body)
			r.Body.Close()
		}
		r.Body = io.NopCloser(bytes.NewReader(reqBody))
		vreq := r.Clone(r.Context())
		vreq.Body = io.NopCloser(bytes.NewReader(reqBody))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		checkExchange(t, doc, router, vreq, rec)

		for k, vs := range rec.Header() {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// isFileContent reports whether the exchange is a successful (or conditional)
// file download, whose caching headers the file endpoint sets itself.
func isFileContent(req *http.Request, status int) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/api/v1/files/")
	if !ok || strings.Count(rest, "/") != 1 {
		return false
	}
	switch status {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
		return true
	}
	return false
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/hooks/") || p == "/healthz" || p == "/auth/handoff"
}

func checkExchange(t testing.TB, doc *openapi3.T, router routers.Router, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	what := req.Method + " " + req.URL.Path
	body := rec.Body.Bytes()
	if !isAPIPath(req.URL.Path) {
		// SPA client routes such as /channels share names with API paths; the
		// spec's second server (/) must not make them look like API calls.
		return
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" && !isFileContent(req, rec.Code) {
		t.Errorf("contract: %s -> %d: Cache-Control = %q, want no-store", what, rec.Code, got)
	}
	if rec.Header().Get(RequestIDHeader) == "" {
		t.Errorf("contract: %s -> %d: no %s header", what, rec.Code, RequestIDHeader)
	}

	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			if rec.Code != http.StatusNotFound {
				t.Errorf("contract: %s is not in openapi.yaml but answered %d", what, rec.Code)
				return
			}
			checkErrorBody(t, doc, what, rec.Code, body)
		}
		return
	}

	rvi := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if reqErr := openapi3filter.ValidateRequest(context.Background(), rvi); reqErr != nil {
		// The request breaks the contract: the server must refuse it.
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("contract: %s violates the spec (%v) but was answered %d", what, firstLine(reqErr), rec.Code)
			return
		}
		checkErrorBody(t, doc, what, rec.Code, body)
		return
	}

	status := rec.Code
	if route.Operation.Responses.Status(status) == nil && status < 500 {
		t.Errorf("contract: %s answered %d, which is not listed for operation %s", what, status, route.Operation.OperationID)
		return
	}
	rvi.Request.Body = io.NopCloser(bytes.NewReader(nil))
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: rvi,
		Status:                 status,
		Header:                 rec.Header(),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	input.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Errorf("contract: %s -> %d violates %s: %v\nbody: %s", what, status, route.Operation.OperationID, firstLine(err), truncate(body))
	}
}

// checkErrorBody validates body against the Error schema.
func checkErrorBody(t testing.TB, doc *openapi3.T, what string, status int, body []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Errorf("contract: %s -> %d: body is not JSON: %q", what, status, truncate(body))
		return
	}
	if err := doc.Components.Schemas["Error"].Value.VisitJSON(v); err != nil {
		t.Errorf("contract: %s -> %d: body is not an Error: %v\nbody: %s", what, status, firstLine(err), truncate(body))
	}
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}

// TestSpecIsValid loads docs/openapi.yaml and checks its shape: OpenAPI 3.1,
// the error schema, and an operationId plus an error fallback on every
// operation.
func TestSpecIsValid(t *testing.T) {
	doc, _ := loadSpec(t)
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi = %q", doc.OpenAPI)
	}
	if doc.Components.Schemas["Error"] == nil || doc.Components.Schemas["ErrorDetail"] == nil {
		t.Fatal("Error schemas missing")
	}
	seen := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" || seen[op.OperationID] {
				t.Errorf("%s %s: operationId %q missing or duplicate", method, path, op.OperationID)
			}
			seen[op.OperationID] = true
			if op.Responses.Default() == nil {
				t.Errorf("%s %s: no default error response", method, path)
			}
		}
	}
	if len(seen) < 13 {
		t.Errorf("only %d operations documented", len(seen))
	}
}

// TestContractHarnessDetectsViolations proves the harness is not vacuous.
func TestContractHarnessDetectsViolations(t *testing.T) {
	doc, router := loadSpec(t)
	cases := []struct {
		name    string
		path    string
		status  int
		body    string
		wantErr bool
	}{
		{"valid health", "/healthz", 200, `{"status":"ok","version":"v1"}`, false},
		{"wrong schema", "/healthz", 200, `{"status":"ok"}`, true},
		{"unlisted status", "/healthz", 418, `{"error":{"code":"internal","message":"x"}}`, true},
		{"undocumented api path", "/api/v1/zzz", 200, `{}`, true},
		{"documented 404 envelope", "/api/v1/zzz", 404, `{"error":{"code":"not_found","message":"x"}}`, false},
		{"404 that is not an Error", "/api/v1/zzz", 404, `{"oops":1}`, true},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json; charset=utf-8")
		rec.Header().Set("Cache-Control", "no-store")
		rec.Header().Set(RequestIDHeader, "x")
		rec.WriteHeader(tc.status)
		rec.WriteString(tc.body)
		sub := &recordingT{TB: t}
		checkExchange(sub, doc, router, httptest.NewRequest(http.MethodGet, tc.path, nil), rec)
		if (sub.errors > 0) != tc.wantErr {
			t.Errorf("%s: harness reported %d errors, want error=%v", tc.name, sub.errors, tc.wantErr)
		}
	}
}

// recordingT counts Errorf calls instead of failing the test.
type recordingT struct {
	testing.TB
	errors int
}

func (r *recordingT) Errorf(string, ...any) { r.errors++ }
