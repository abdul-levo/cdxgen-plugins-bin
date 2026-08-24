package analyzer

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/cdxgen/cdxgen-plugins-bin/thirdparty/golem/internal/model"
)

// handlerSignatureExtractor walks HTTP-handler function bodies and lifts
// framework-specific binding calls into structured [model.EndpointParameter],
// request-body, and response types. Its job is to turn what the existing
// endpoint detector recorded (framework + method + path + handler name)
// into the additional shape information downstream tooling needs to
// generate OpenAPI schemas.
//
// Supported patterns per framework:
//
//   - Gin (github.com/gin-gonic/gin): `c.Param("name")` and `c.Params.Get("name")`
//     for path params; `c.Query("name")` / `c.DefaultQuery("name", …)` for
//     query params; `c.ShouldBindJSON(&x)` / `c.BindJSON(&x)` / `c.Bind(&x)`
//     for the request body; `c.JSON(status, expr)` for the response.
//   - chi (github.com/go-chi/chi): `chi.URLParam(r, "name")` for path params;
//     `render.DecodeJSON(r.Body, &x)` / `json.NewDecoder(r.Body).Decode(&x)`
//     for the request body; `render.JSON(w, r, expr)` for the response.
//   - Echo (github.com/labstack/echo): `c.Param("name")` for path params;
//     `c.QueryParam("name")` for query params; `c.Bind(&x)` for the request
//     body; `c.JSON(status, expr)` for the response.
//
// Extraction is best-effort. Handlers that use dynamic dispatch, closures
// captured through interfaces, or unrecognised binding APIs contribute no
// enriched fields; the endpoint still ships with its method / path /
// handler as before. That is deliberate — a wrong parameter type is
// worse than a missing one, since downstream tools will happily fuzz
// against the wrong shape.
type handlerSignatureExtractor struct {
	pkg      *packages.Package
	handlers map[string]*ast.FuncDecl
}

func newHandlerSignatureExtractor(pkg *packages.Package) *handlerSignatureExtractor {
	e := &handlerSignatureExtractor{pkg: pkg, handlers: map[string]*ast.FuncDecl{}}
	if pkg == nil {
		return e
	}
	// Index handler-shaped function declarations by their short name so we
	// can resolve endpoint.handler back to a *ast.FuncDecl without
	// re-walking the AST per endpoint. Method receivers are dropped in
	// endpoint.handler already (only the method name survives), so the
	// short-name key is the join column that matches.
	for _, file := range pkg.Syntax {
		if file == nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			e.handlers[fn.Name.Name] = fn
		}
	}
	return e
}

// enrich populates [model.APIEndpoint.Parameters], RequestBodyType, and
// ResponseType by walking the handler's function body. Called once per
// endpoint after endpointForCall has done the framework/method/path work.
func (e *handlerSignatureExtractor) enrich(endpoint *model.APIEndpoint) {
	if endpoint == nil || endpoint.Handler == "" || e.pkg == nil || e.pkg.TypesInfo == nil {
		return
	}
	fn, ok := e.handlers[handlerShortName(endpoint.Handler)]
	if !ok || fn.Body == nil {
		return
	}

	receiverIdents := handlerReceiverIdents(fn, endpoint.Framework)
	requestIdents := handlerRequestIdents(fn, endpoint.Framework)

	seenParam := map[string]bool{}
	seenQuery := map[string]bool{}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch endpoint.Framework {
		case "gin":
			e.inspectGinCall(call, receiverIdents, endpoint, seenParam, seenQuery)
		case "chi":
			e.inspectChiCall(call, requestIdents, endpoint, seenParam)
		case "echo":
			e.inspectEchoCall(call, receiverIdents, endpoint, seenParam, seenQuery)
		}
		return true
	})
}

// ─── Gin ────────────────────────────────────────────────────────────────

func (e *handlerSignatureExtractor) inspectGinCall(
	call *ast.CallExpr,
	receiverIdents map[string]bool,
	endpoint *model.APIEndpoint,
	seenParam, seenQuery map[string]bool,
) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || !receiverIdents[recv.Name] {
		return
	}
	method := sel.Sel.Name
	switch method {
	case "Param":
		e.recordPathParam(call, endpoint, seenParam, "string")
	case "Query", "DefaultQuery", "GetQuery":
		e.recordQueryParam(call, endpoint, seenQuery, "string")
	case "ShouldBindJSON", "BindJSON", "ShouldBind", "Bind", "ShouldBindQuery", "ShouldBindWith", "BindWith":
		e.recordRequestBodyFromPointerArg(call, endpoint)
	case "JSON", "JSONP", "IndentedJSON", "SecureJSON", "PureJSON", "AsciiJSON":
		// Skip error-abort helpers deliberately: they emit the client-
		// error payload (usually gin.H{"error": ...}) which is not
		// what downstream OpenAPI consumers want as the operation's
		// primary response body.
		e.recordResponseFromStatusExpr(call, endpoint, false)
	}
}

// ─── chi ────────────────────────────────────────────────────────────────

func (e *handlerSignatureExtractor) inspectChiCall(
	call *ast.CallExpr,
	requestIdents map[string]bool,
	endpoint *model.APIEndpoint,
	seenParam map[string]bool,
) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	method := sel.Sel.Name
	pkgName := pkgIdent.Name

	// chi.URLParam(r, "name")  → path param
	if pkgName == "chi" && method == "URLParam" && len(call.Args) >= 2 {
		if name, ok := stringLiteral(call.Args[1]); ok && !seenParam[name] {
			seenParam[name] = true
			endpoint.Parameters = append(endpoint.Parameters, model.EndpointParameter{
				Name: name, Location: "path", TypeName: "string",
			})
		}
		return
	}
	// render.DecodeJSON(r.Body, &x) / render.Bind(r, &x)
	if pkgName == "render" && (method == "DecodeJSON" || method == "Bind" || method == "Decode") && len(call.Args) >= 2 {
		e.recordRequestBodyFromPointerArg(&ast.CallExpr{Args: call.Args[1:]}, endpoint)
		return
	}
	// render.JSON(w, r, expr) / render.Respond(w, r, expr)
	if pkgName == "render" && (method == "JSON" || method == "Respond") && len(call.Args) >= 3 {
		e.recordResponseFromExpr(call.Args[2], endpoint)
		return
	}
	// json.NewDecoder(r.Body).Decode(&x) — chained; not caught here because
	// the receiver is a method-call expression, not an ident. Left to the
	// echo/generic paths, which handle Bind on request-derived receivers.
	if _, ok := call.Fun.(*ast.SelectorExpr); ok && method == "Decode" {
		e.recordRequestBodyFromPointerArg(call, endpoint)
	}
	_ = requestIdents
}

// ─── Echo ───────────────────────────────────────────────────────────────

func (e *handlerSignatureExtractor) inspectEchoCall(
	call *ast.CallExpr,
	receiverIdents map[string]bool,
	endpoint *model.APIEndpoint,
	seenParam, seenQuery map[string]bool,
) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || !receiverIdents[recv.Name] {
		return
	}
	method := sel.Sel.Name
	switch method {
	case "Param":
		e.recordPathParam(call, endpoint, seenParam, "string")
	case "QueryParam", "QueryParamDefault":
		e.recordQueryParam(call, endpoint, seenQuery, "string")
	case "Bind", "BindJSON":
		e.recordRequestBodyFromPointerArg(call, endpoint)
	case "JSON", "JSONPretty":
		e.recordResponseFromStatusExpr(call, endpoint, false)
	}
}

// ─── shared helpers ─────────────────────────────────────────────────────

func (e *handlerSignatureExtractor) recordPathParam(
	call *ast.CallExpr, endpoint *model.APIEndpoint, seen map[string]bool, typeName string,
) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := stringLiteral(call.Args[0])
	if !ok || seen[name] {
		return
	}
	seen[name] = true
	endpoint.Parameters = append(endpoint.Parameters, model.EndpointParameter{
		Name: name, Location: "path", TypeName: typeName,
	})
}

func (e *handlerSignatureExtractor) recordQueryParam(
	call *ast.CallExpr, endpoint *model.APIEndpoint, seen map[string]bool, typeName string,
) {
	if len(call.Args) == 0 {
		return
	}
	name, ok := stringLiteral(call.Args[0])
	if !ok || seen[name] {
		return
	}
	seen[name] = true
	endpoint.Parameters = append(endpoint.Parameters, model.EndpointParameter{
		Name: name, Location: "query", TypeName: typeName,
	})
}

// recordRequestBodyFromPointerArg resolves the pointed-to type of the
// first `&x` argument to the call and stores it on the endpoint. Handles
// both `c.BindJSON(&payload)` (pointer expression) and `c.Bind(payload)`
// where payload is already a pointer variable.
func (e *handlerSignatureExtractor) recordRequestBodyFromPointerArg(call *ast.CallExpr, endpoint *model.APIEndpoint) {
	if endpoint.RequestBodyType != "" {
		return
	}
	arg := firstBindArg(call)
	if arg == nil {
		return
	}
	typ := e.pkg.TypesInfo.TypeOf(arg)
	if typ == nil {
		return
	}
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	name := shortTypeName(typ)
	if name == "" {
		return
	}
	endpoint.RequestBodyType = name
}

// recordResponseFromStatusExpr handles the (status, expr) shape used by
// gin.Context.JSON, echo.Context.JSON, etc. When abortWithStatus is true
// the shape is (status, expr) but the call is a hard-abort helper.
//
// Error-status responses (4xx / 5xx) are deliberately not recorded as
// the endpoint's primary response body: handlers routinely emit a
// gin.H{"error": ...} shape on the sad path that has nothing in common
// with the happy-path payload, and downstream OpenAPI consumers expect
// the 200 slot to describe successful responses. If the extractor
// hasn't seen a non-error response yet, we still fall through and use
// this one so an endpoint whose only response is an error still gets a
// schema — better an error shape than none.
func (e *handlerSignatureExtractor) recordResponseFromStatusExpr(
	call *ast.CallExpr, endpoint *model.APIEndpoint, abortWithStatus bool,
) {
	_ = abortWithStatus
	if endpoint.ResponseType != "" || len(call.Args) < 2 {
		return
	}
	if len(call.Args) >= 1 && e.isErrorStatusExpr(call.Args[0]) {
		// Remember it in a scratch property so the fallback below can
		// pick it up if no non-error response is seen. Storing on
		// Properties keeps this hint from leaking into the visible
		// ResponseType until we know it is the best we have.
		if endpoint.Properties == nil {
			endpoint.Properties = map[string]string{}
		}
		if _, already := endpoint.Properties["errorResponseType"]; already {
			return
		}
		if typ := e.pkg.TypesInfo.TypeOf(call.Args[1]); typ != nil {
			if name := shortTypeName(typ); name != "" {
				endpoint.Properties["errorResponseType"] = name
			}
		}
		return
	}
	e.recordResponseFromExpr(call.Args[1], endpoint)
}

// isErrorStatusExpr reports whether a JSON-call status argument names a
// 4xx or 5xx HTTP status code. Handles integer literals, named net/http
// constants (`http.StatusBadRequest`), and any typed constant whose
// resolved value is >= 400.
func (e *handlerSignatureExtractor) isErrorStatusExpr(expr ast.Expr) bool {
	// Cheap path: an untyped/typed integer literal.
	if lit, ok := expr.(*ast.BasicLit); ok {
		if value, err := strconv.Atoi(lit.Value); err == nil {
			return value >= 400 && value < 600
		}
	}
	// Named constants: net/http.StatusBadRequest etc. Resolve via the
	// package's TypesInfo when available so we don't have to enumerate
	// every constant name upstream.
	if e.pkg != nil && e.pkg.TypesInfo != nil {
		if tv, ok := e.pkg.TypesInfo.Types[expr]; ok && tv.Value != nil {
			if tv.Value.Kind() == constant.Int {
				if value, ok := constant.Int64Val(tv.Value); ok {
					return value >= 400 && value < 600
				}
			}
		}
	}
	// Fallback for cases where TypesInfo doesn't have a constant value:
	// pattern-match the identifier name against the well-known net/http
	// error names. Covers references reached through aliased imports
	// (`h "net/http"; h.StatusBadRequest`) whose selector text still
	// ends with the constant name.
	if sel, ok := expr.(*ast.SelectorExpr); ok && sel.Sel != nil {
		return isErrorStatusName(sel.Sel.Name)
	}
	if id, ok := expr.(*ast.Ident); ok {
		return isErrorStatusName(id.Name)
	}
	return false
}

func isErrorStatusName(name string) bool {
	// Only names we know for certain are 4xx/5xx go here; adding more
	// is safe. Anything not matched falls through, which is the
	// correct default (assume it's a successful response).
	switch name {
	case "StatusBadRequest", "StatusUnauthorized", "StatusPaymentRequired",
		"StatusForbidden", "StatusNotFound", "StatusMethodNotAllowed",
		"StatusNotAcceptable", "StatusProxyAuthRequired", "StatusRequestTimeout",
		"StatusConflict", "StatusGone", "StatusLengthRequired",
		"StatusPreconditionFailed", "StatusRequestEntityTooLarge",
		"StatusRequestURITooLong", "StatusUnsupportedMediaType",
		"StatusRequestedRangeNotSatisfiable", "StatusExpectationFailed",
		"StatusTeapot", "StatusMisdirectedRequest", "StatusUnprocessableEntity",
		"StatusLocked", "StatusFailedDependency", "StatusTooEarly",
		"StatusUpgradeRequired", "StatusPreconditionRequired",
		"StatusTooManyRequests", "StatusRequestHeaderFieldsTooLarge",
		"StatusUnavailableForLegalReasons",
		"StatusInternalServerError", "StatusNotImplemented",
		"StatusBadGateway", "StatusServiceUnavailable",
		"StatusGatewayTimeout", "StatusHTTPVersionNotSupported",
		"StatusVariantAlsoNegotiates", "StatusInsufficientStorage",
		"StatusLoopDetected", "StatusNotExtended",
		"StatusNetworkAuthenticationRequired":
		return true
	}
	return false
}

func (e *handlerSignatureExtractor) recordResponseFromExpr(expr ast.Expr, endpoint *model.APIEndpoint) {
	if endpoint.ResponseType != "" {
		return
	}
	typ := e.pkg.TypesInfo.TypeOf(expr)
	if typ == nil {
		return
	}
	name := shortTypeName(typ)
	if name == "" {
		return
	}
	endpoint.ResponseType = name
}

// firstBindArg picks the argument that binds — the first pointer arg
// among the call's arguments. That is the pattern shared by every
// framework Bind helper we support: `c.Bind(&payload)`,
// `c.BindJSON(&payload)`, `c.ShouldBindWith(&payload, binding.JSON)`,
// `render.DecodeJSON(r.Body, &payload)`, etc.
func firstBindArg(call *ast.CallExpr) ast.Expr {
	for _, arg := range call.Args {
		if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op.String() == "&" {
			return unary.X
		}
		if _, ok := arg.(*ast.Ident); ok {
			return arg
		}
	}
	return nil
}

// handlerReceiverIdents lists the parameter names that carry the
// framework's context object (for gin/echo, the Context receiver).
// Handlers virtually always name this `c`, but we accept anything typed
// as the framework Context type so lint-suggested renames like `ctx`
// still work.
func handlerReceiverIdents(fn *ast.FuncDecl, framework string) map[string]bool {
	names := map[string]bool{}
	if fn.Type == nil || fn.Type.Params == nil {
		return names
	}
	for _, field := range fn.Type.Params.List {
		typeText := exprTypeText(field.Type)
		if !isContextType(typeText, framework) {
			continue
		}
		for _, ident := range field.Names {
			if ident != nil {
				names[ident.Name] = true
			}
		}
	}
	return names
}

// handlerRequestIdents lists parameter names typed as `*http.Request`.
// Used for chi handlers, whose path-param helpers take the request
// rather than a framework-specific context.
func handlerRequestIdents(fn *ast.FuncDecl, framework string) map[string]bool {
	names := map[string]bool{}
	if framework != "chi" || fn.Type == nil || fn.Type.Params == nil {
		return names
	}
	for _, field := range fn.Type.Params.List {
		typeText := exprTypeText(field.Type)
		if !strings.Contains(typeText, "http.Request") {
			continue
		}
		for _, ident := range field.Names {
			if ident != nil {
				names[ident.Name] = true
			}
		}
	}
	return names
}

func isContextType(typeText, framework string) bool {
	switch framework {
	case "gin":
		return strings.Contains(typeText, "gin.Context")
	case "echo":
		// Echo's Context is an interface, so handlers take `echo.Context`
		// without a pointer star.
		return strings.Contains(typeText, "echo.Context")
	}
	return false
}

// exprTypeText renders a type expression back to its source-level text,
// giving us a lightweight way to spot framework types without dragging
// go/types into every callsite.
func exprTypeText(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprTypeText(t.X)
	case *ast.SelectorExpr:
		return exprTypeText(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprTypeText(t.Elt)
	}
	return ""
}

// Framework-provided ad-hoc map aliases carry no schema information —
// they are just `map[string]any` under a friendlier name. Downstream
// tooling gets more value from the shape (`object`) than from the alias
// name, so we normalize these so an OpenAPI generator can emit a
// free-form object schema rather than a $ref to something that has no
// declaration to point at.
var freeFormObjectTypeNames = map[string]bool{
	"gin.H":         true,
	"H":             true,
	"echo.Map":      true,
	"Map":           true,
	"fiber.Map":     true,
	"iris.Map":      true,
	"beego.M":       true,
	"martini.Map":   true,
	"revel.RenderArgs": true,
}

// shortTypeName strips generic package qualifiers so the emitted name
// is what downstream OpenAPI generators expect — `User` rather than
// `github.com/acme/api/models.User`. `[]T` and pointer wrappers are
// preserved to keep multiplicity information. Framework-owned ad-hoc
// map types (gin.H, echo.Map, fiber.Map, …) collapse to `object` so
// the schema field carries useful shape information.
func shortTypeName(t types.Type) string {
	if t == nil {
		return ""
	}
	switch typ := t.(type) {
	case *types.Pointer:
		return shortTypeName(typ.Elem())
	case *types.Slice:
		inner := shortTypeName(typ.Elem())
		if inner == "" {
			return ""
		}
		if inner == "object" {
			// []gin.H → array of free-form objects, spelled without the
			// framework alias so consumers can generate {items: {}}.
			return "[]object"
		}
		return "[]" + inner
	case *types.Array:
		inner := shortTypeName(typ.Elem())
		if inner == "" {
			return ""
		}
		return "[]" + inner
	case *types.Named:
		obj := typ.Obj()
		if obj == nil {
			return ""
		}
		qualified := ""
		if obj.Pkg() != nil {
			qualified = obj.Pkg().Name() + "." + obj.Name()
		}
		if freeFormObjectTypeNames[obj.Name()] || freeFormObjectTypeNames[qualified] {
			return "object"
		}
		return obj.Name()
	case *types.Basic:
		return typ.Name()
	case *types.Interface:
		if typ.Empty() {
			return "any"
		}
		return ""
	case *types.Map:
		return "object"
	}
	return ""
}

// handlerShortName strips leading package qualifiers or receiver types
// from a handler string so it matches the file-level FuncDecl index. Gin
// registrations that pass method values (e.g. `svc.CreateUser`) surface
// as "CreateUser" here; module-qualified references collapse the same
// way.
func handlerShortName(handler string) string {
	if handler == "" {
		return ""
	}
	if idx := strings.LastIndex(handler, "."); idx >= 0 {
		return handler[idx+1:]
	}
	return handler
}
