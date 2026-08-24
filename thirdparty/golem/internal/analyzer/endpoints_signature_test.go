package analyzer

import (
	"testing"

	"github.com/cdxgen/cdxgen-plugins-bin/thirdparty/golem/internal/model"
)

// TestEndpointsEnrichedForGin ties both improvements delivered in this
// change together against a small, hermetic Gin fixture:
//
//   - the empty-path fix — group-root registrations like
//     `users.GET("", listUsers)` are now composed with their group's
//     prefix and reported instead of being dropped as pathless, and
//   - the handler-signature extractor — each endpoint carries path
//     parameters from `c.Param(...)`, query parameters from
//     `c.Query(...)`, the request-body type when the handler calls
//     `c.ShouldBindJSON(&x)`, and the response type from `c.JSON(...)`.
//
// If either piece regresses, exactly one of the assertions in this
// test fails, so the diff points at the guilty half.
func TestEndpointsEnrichedForGin(t *testing.T) {
	report, err := Analyze(Options{
		Dir:          "../../testdata/endpoints-gin",
		IncludeLocal: true,
		ToolVersion:  "test",
	})
	if err != nil {
		t.Fatalf("analyze endpoints-gin: %v", err)
	}

	byKey := map[string]model.APIEndpoint{}
	for _, ep := range report.APIEndpoints {
		byKey[ep.Method+" "+ep.Path] = ep
	}

	// The fixture registers 6 routes. Every one must appear — including
	// the group-root ones (POST /api/v1/users, GET /api/v1/users) whose
	// path argument was the empty string.
	wantRoutes := []string{
		"GET /health",
		"GET /api/v1/users",
		"POST /api/v1/users",
		"GET /api/v1/users/:id",
		"GET /api/v1/orders/:id",
	}
	for _, key := range wantRoutes {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("expected endpoint %q in report; got %#v", key, keys(byKey))
		}
	}
	if report.Stats.APIEndpointCount < len(wantRoutes) {
		t.Fatalf("expected at least %d endpoints, got %d (%#v)",
			len(wantRoutes), report.Stats.APIEndpointCount, keys(byKey))
	}

	// getUser calls c.Param("id") → one path parameter named "id" of
	// type string.
	if got := byKey["GET /api/v1/users/:id"]; len(got.Parameters) != 1 {
		t.Fatalf("getUser: expected 1 parameter, got %#v", got.Parameters)
	} else {
		p := got.Parameters[0]
		if p.Name != "id" || p.Location != "path" || p.TypeName != "string" {
			t.Fatalf("getUser: unexpected parameter %#v", p)
		}
	}

	// listUsers calls c.Query("limit") → one query parameter.
	if got := byKey["GET /api/v1/users"]; !hasParam(got.Parameters, "limit", "query") {
		t.Fatalf("listUsers: expected query 'limit', got %#v", got.Parameters)
	}

	// createUser calls c.ShouldBindJSON(&req) with req of type
	// CreateUserRequest → requestBodyType should carry that name.
	if got := byKey["POST /api/v1/users"]; got.RequestBodyType != "CreateUserRequest" {
		t.Fatalf("createUser: expected requestBodyType 'CreateUserRequest', got %q",
			got.RequestBodyType)
	}

	// createUser returns User via c.JSON(201, User{...}).
	if got := byKey["POST /api/v1/users"]; got.ResponseType != "User" {
		t.Fatalf("createUser: expected responseType 'User', got %q", got.ResponseType)
	}

	// listUsers returns []User via c.JSON(200, []User{}). Slice
	// multiplicity must be preserved so consumers know the shape is
	// an array of objects rather than a single object.
	if got := byKey["GET /api/v1/users"]; got.ResponseType != "[]User" {
		t.Fatalf("listUsers: expected responseType '[]User', got %q", got.ResponseType)
	}

	// health returns gin.H{...}. Ad-hoc framework map aliases collapse
	// to `object` in the emitted schema so downstream OpenAPI generators
	// produce a free-form object schema instead of a $ref to something
	// that has no declaration to point at.
	if got := byKey["GET /health"]; got.ResponseType != "object" {
		t.Fatalf("health: expected responseType 'object' (from gin.H), got %q",
			got.ResponseType)
	}
}

func hasParam(params []model.EndpointParameter, name, location string) bool {
	for _, p := range params {
		if p.Name == name && p.Location == location {
			return true
		}
	}
	return false
}

func keys(m map[string]model.APIEndpoint) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
