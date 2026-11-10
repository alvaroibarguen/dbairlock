package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/alvaroibarguen/dbairlock/internal/httpapi/gen"
	"github.com/alvaroibarguen/dbairlock/internal/platform/httpx"
)

// The generated decoders use encoding/json, which ignores unknown members, so
// a body such as {"instance.public_url": "..."} sent to PATCH /api/settings
// (without the "values" wrapper) would succeed and change nothing. To fail
// closed, every request is also validated against api/openapi.yaml (embedded
// in package gen) and refused with 400 bad_request if its shape is wrong: an
// unknown or misspelled member, a value of the wrong type, a missing
// required member, a body that is not JSON, or a body sent to an operation
// that declares none.
//
// Value rules (ranges, lengths, patterns, enums, formats) are in the contract
// too, but the modules own them: they answer 422 validation_failed with a
// message per field that the dashboard shows next to the input. A request
// that breaks only value rules therefore reaches the handler, which refuses
// it with that 422.
//
// Validation runs after the authenticate middleware, so a caller without
// access gets 401 or 403 whatever the body. The generated code decodes the
// body before any strict middleware runs, so bufferBody keeps a copy of it
// for the validator.

// validationOptions are the openapi3filter options for every request.
var validationOptions = &openapi3filter.Options{
	// Collect every problem, so a value-rule failure (left to the modules)
	// cannot hide a shape failure after it.
	MultiError: true,
	// Never rewrite the body with schema defaults: an omitted member must
	// stay omitted (a PATCH leaves it unchanged).
	SkipSettingDefaults: true,
	// A body sent to an operation that declares none is refused.
	RejectWhenRequestBodyNotSpecified: true,
	// Access is the authenticate middleware's job (auth.go, access.go).
	AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
}

// valueKeywords are the schema keywords whose failures the modules report
// (with 422); a failure of any other keyword is a malformed request.
var valueKeywords = map[string]bool{
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minLength": true, "maxLength": true, "pattern": true,
	"format": true, "enum": true, "minItems": true, "maxItems": true,
	"uniqueItems": true, "minProperties": true, "maxProperties": true,
}

// contractRoute is one operation of the contract.
type contractRoute struct {
	route      *routers.Route
	pathParams []string
}

// contractRoutes loads the embedded contract once and indexes its operations
// by the pattern the generated code registers them under
// ("PATCH /api/settings").
var contractRoutes = sync.OnceValues(func() (map[string]contractRoute, error) {
	doc, err := gen.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded contract: %w", err)
	}
	spec := validationSpec(doc)
	routes := make(map[string]contractRoute)
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			var names []string
			for _, params := range []openapi3.Parameters{item.Parameters, op.Parameters} {
				for _, p := range params {
					if p.Value != nil && p.Value.In == openapi3.ParameterInPath {
						names = append(names, p.Value.Name)
					}
				}
			}
			routes[method+" "+path] = contractRoute{
				route:      &routers.Route{Spec: spec, Path: path, PathItem: item, Method: method, Operation: op},
				pathParams: names,
			}
		}
	}
	return routes, nil
})

// validationSpec is the document the validator is given. For an OpenAPI 3.1
// document openapi3filter compiles each request schema with a JSON Schema
// 2020-12 compiler, on every request (about 0.6 ms and 0.5 MB each), and for
// schemas that use none of the keywords only that compiler knows, it then
// validates with kin-openapi's own validator anyway. When the contract uses
// none of those keywords anywhere, the validator is told the document is
// OpenAPI 3.0, which selects its own validator directly. The only 3.0-only
// keyword, nullable, is not used either (3.1 nullability is a "null" type,
// which both understand). If a keyword in either set is ever added, the
// document is used as it is: slower, never less strict.
func validationSpec(doc *openapi3.T) *openapi3.T {
	if !doc.IsOpenAPI31OrLater() || uses31OnlyKeywords(doc) {
		return doc
	}
	legacy := *doc
	legacy.OpenAPI = "3.0.3"
	return &legacy
}

// uses31OnlyKeywords reports whether any schema in doc uses a keyword whose
// meaning differs between the two validators (the list kin-openapi checks
// before falling back to its own validator, plus nullable).
func uses31OnlyKeywords(doc *openapi3.T) bool {
	found := errors.New("found")
	err := doc.WalkSchemas(func(_ string, ref *openapi3.SchemaRef) error {
		s := ref.Value
		if s.Nullable || s.Const != nil || len(s.PrefixItems) != 0 || s.Contains != nil ||
			len(s.PatternProperties) != 0 || len(s.DependentSchemas) != 0 || s.PropertyNames != nil ||
			s.UnevaluatedItems.Has != nil || s.UnevaluatedItems.Schema != nil ||
			s.UnevaluatedProperties.Has != nil || s.UnevaluatedProperties.Schema != nil ||
			s.If != nil || s.Then != nil || s.Else != nil ||
			len(s.DependentRequired) != 0 || len(s.Defs) != 0 || s.ContentSchema != nil {
			return found
		}
		return nil
	})
	return err != nil
}

// bufferBody is a generated-server middleware: it runs after routing, before
// the body is decoded, and reads the whole body (within the body limit) so it
// can be decoded once by the handler and once by the validator.
func bufferBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			data, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				requestError(w, r, err)
				return
			}
			r.ContentLength = int64(len(data))
			r.Body = io.NopCloser(bytes.NewReader(data))
			r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
		}
		next.ServeHTTP(w, r)
	})
}

// validateContract is a strict middleware; with authenticate outside it, it
// runs once access is established.
func validateContract(routes map[string]contractRoute) gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
			if err := validateRequest(ctx, routes, r); err != nil {
				return nil, err
			}
			return f(ctx, w, r, req)
		}
	}
}

// validateRequest checks r against the contract. It returns nil, an
// *httpx.Error, or an internal error.
func validateRequest(ctx context.Context, routes map[string]contractRoute, r *http.Request) error {
	cr, ok := routes[r.Pattern]
	if !ok {
		// Every generated route comes from the contract; refuse rather
		// than skip validation if one does not.
		return fmt.Errorf("no contract operation for route %q", r.Pattern)
	}
	// The handler has consumed r.Body; validate a copy with a fresh body.
	vr := r.Clone(ctx)
	vr.Body = http.NoBody
	if r.GetBody != nil {
		body, err := r.GetBody()
		if err != nil {
			return err
		}
		vr.Body = body
	}
	params := make(map[string]string, len(cr.pathParams))
	for _, name := range cr.pathParams {
		params[name] = r.PathValue(name)
	}
	err := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:    vr,
		PathParams: params,
		Route:      cr.route,
		Options:    validationOptions,
	})
	if err == nil {
		return nil
	}
	var problems []httpx.FieldError
	collectProblems(err, nil, &problems)
	if len(problems) == 0 {
		return nil // value rules only: the module answers 422
	}
	msg := "The request is invalid: " + problems[0].Field + " " + problems[0].Message + "."
	if len(problems) > 1 {
		msg = fmt.Sprintf("The request is invalid: %s %s (and %d more problems).",
			problems[0].Field, problems[0].Message, len(problems)-1)
	}
	he := httpx.NewError(http.StatusBadRequest, "bad_request", msg)
	he.Fields = problems
	return he
}

// collectProblems appends every shape problem in a validation error to out.
// re is the request error being walked (nil at the top). A problem's Field
// names where it is ("body /values/x", "query parameter \"limit\"") and its
// Message what is wrong, without echoing a secret's value.
func collectProblems(err error, re *openapi3filter.RequestError, out *[]httpx.FieldError) {
	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		for _, e := range multi {
			collectProblems(e, re, out)
		}
		return
	}
	var req *openapi3filter.RequestError
	if errors.As(err, &req) && req != re {
		if req.Err == nil {
			reason := req.Reason
			if reason == "" {
				reason = "is not valid"
			}
			*out = append(*out, httpx.FieldError{Field: location(req, nil), Message: reason})
			return
		}
		collectProblems(req.Err, req, out)
		return
	}
	var se *openapi3.SchemaError
	if errors.As(err, &se) {
		// A schema error with nested causes (JSON Schema 2020 validation)
		// is judged by its causes.
		var nested openapi3.MultiError
		if se.Origin != nil && errors.As(se.Origin, &nested) {
			collectProblems(nested, re, out)
			return
		}
		if valueKeywords[se.SchemaField] {
			return
		}
		reason := se.Reason
		if reason == "" || isSecretSchema(se.Schema) {
			reason = "is not valid"
		}
		*out = append(*out, httpx.FieldError{Field: location(re, se), Message: reason})
		return
	}
	var reason string
	switch {
	case errors.Is(err, openapi3filter.ErrInvalidRequired):
		reason = "is required"
	case errors.Is(err, openapi3filter.ErrInvalidEmptyValue):
		reason = "must not be empty"
	case re != nil && re.Reason != "":
		reason = re.Reason + ": " + err.Error()
	default:
		reason = err.Error()
	}
	*out = append(*out, httpx.FieldError{Field: location(re, nil), Message: reason})
}

// location names the part of the request a problem is in.
func location(re *openapi3filter.RequestError, se *openapi3.SchemaError) string {
	where := "body"
	if re != nil && re.Parameter != nil {
		where = fmt.Sprintf("%s parameter %q", re.Parameter.In, re.Parameter.Name)
	}
	if se != nil {
		if ptr := se.JSONPointer(); len(ptr) > 0 {
			where += " /" + strings.Join(ptr, "/")
		}
	}
	return where
}

// secretExtension marks a schema property as a secret in api/openapi.yaml.
const secretExtension = "x-dbairlock-secret" //nolint:gosec // an extension name, not a credential

// isSecretSchema reports whether a schema describes a secret, whose value
// must never appear in an error message.
func isSecretSchema(s *openapi3.Schema) bool {
	if s == nil {
		return false
	}
	v, ok := s.Extensions[secretExtension]
	return s.WriteOnly || (ok && fmt.Sprint(v) == "true")
}
