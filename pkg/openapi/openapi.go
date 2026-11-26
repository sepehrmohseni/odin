// Package openapi describes the gateway's routes as an OpenAPI 3.0 document and serves it.
package openapi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"odin/pkg/service"

	"gopkg.in/yaml.v3"
)

// Spec represents an OpenAPI 3.0 specification
type Spec struct {
	OpenAPI    string              `json:"openapi" yaml:"openapi"`
	Info       Info                `json:"info" yaml:"info"`
	Servers    []Server            `json:"servers,omitempty" yaml:"servers,omitempty"`
	Tags       []Tag               `json:"tags,omitempty" yaml:"tags,omitempty"`
	Paths      map[string]PathItem `json:"paths" yaml:"paths"`
	Components Components          `json:"components,omitempty" yaml:"components,omitempty"`
}

// Info represents API information
type Info struct {
	Title       string   `json:"title" yaml:"title"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Version     string   `json:"version" yaml:"version"`
	Contact     *Contact `json:"contact,omitempty" yaml:"contact,omitempty"`
}

// Contact represents contact information
type Contact struct {
	Name  string `json:"name,omitempty" yaml:"name,omitempty"`
	Email string `json:"email,omitempty" yaml:"email,omitempty"`
	URL   string `json:"url,omitempty" yaml:"url,omitempty"`
}

// Server represents an API server
type Server struct {
	URL         string `json:"url" yaml:"url"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Tag groups operations; the generator makes one per service.
type Tag struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// PathItem represents operations available on a single path
type PathItem struct {
	Get        *Operation  `json:"get,omitempty" yaml:"get,omitempty"`
	Post       *Operation  `json:"post,omitempty" yaml:"post,omitempty"`
	Put        *Operation  `json:"put,omitempty" yaml:"put,omitempty"`
	Delete     *Operation  `json:"delete,omitempty" yaml:"delete,omitempty"`
	Patch      *Operation  `json:"patch,omitempty" yaml:"patch,omitempty"`
	Options    *Operation  `json:"options,omitempty" yaml:"options,omitempty"`
	Parameters []Parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

// Operation represents an API operation
type Operation struct {
	Summary     string                `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	OperationID string                `json:"operationId,omitempty" yaml:"operationId,omitempty"`
	Tags        []string              `json:"tags,omitempty" yaml:"tags,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty" yaml:"requestBody,omitempty"`
	Responses   map[string]Response   `json:"responses" yaml:"responses"`
	Security    []map[string][]string `json:"security,omitempty" yaml:"security,omitempty"`
}

// Parameter represents an operation parameter
type Parameter struct {
	Name        string  `json:"name" yaml:"name"`
	In          string  `json:"in" yaml:"in"` // query, header, path, cookie
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool    `json:"required,omitempty" yaml:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// RequestBody represents a request body
type RequestBody struct {
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool                 `json:"required,omitempty" yaml:"required,omitempty"`
	Content     map[string]MediaType `json:"content" yaml:"content"`
}

// Response represents an API response
type Response struct {
	Description string               `json:"description" yaml:"description"`
	Headers     map[string]Header    `json:"headers,omitempty" yaml:"headers,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty" yaml:"content,omitempty"`
}

// Header describes a response header.
type Header struct {
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Schema      *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// MediaType represents a media type
type MediaType struct {
	Schema *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// Schema represents a data schema
type Schema struct {
	Ref        string             `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	Type       string             `json:"type,omitempty" yaml:"type,omitempty"`
	Properties map[string]*Schema `json:"properties,omitempty" yaml:"properties,omitempty"`
	Items      *Schema            `json:"items,omitempty" yaml:"items,omitempty"`
	Required   []string           `json:"required,omitempty" yaml:"required,omitempty"`
	Example    interface{}        `json:"example,omitempty" yaml:"example,omitempty"`
}

// Components holds reusable objects
type Components struct {
	Schemas         map[string]*Schema        `json:"schemas,omitempty" yaml:"schemas,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}

// SecurityScheme defines a security scheme
type SecurityScheme struct {
	Type         string `json:"type" yaml:"type"` // apiKey, http, oauth2, openIdConnect
	Scheme       string `json:"scheme,omitempty" yaml:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty" yaml:"bearerFormat,omitempty"`
	In           string `json:"in,omitempty" yaml:"in,omitempty"`
	Name         string `json:"name,omitempty" yaml:"name,omitempty"`
}

// Info for the generated document; empty fields get defaults.
type Options struct {
	Title       string
	Version     string
	Description string
}

// proxiedMethods are the methods documented for an HTTP service. The gateway forwards any
// method; these are the ones API clients and tools use.
var proxiedMethods = []string{"get", "post", "put", "patch", "delete"}

// Generate describes what the gateway knows about each service: its base path and everything
// below it, the methods it forwards, whether a JWT is required, and the responses the gateway
// itself can produce (401, 429, 502, 503, 504). It does not know an upstream's own operations
// or schemas and does not invent them (it used to make up list/create/get/update/delete
// operations with limit and offset parameters for every service): the upstream's answer is the
// "default" response. Targets are never included, since they are often internal addresses.
// gRPC services are left out: they are not described by OpenAPI.
func Generate(opts Options, services []*service.Config) *Spec {
	if opts.Title == "" {
		opts.Title = "Odin API Gateway"
	}
	if opts.Version == "" {
		opts.Version = "1.0.0"
	}
	spec := &Spec{
		OpenAPI: "3.0.3",
		Info:    Info{Title: opts.Title, Version: opts.Version, Description: opts.Description},
		Paths:   make(map[string]PathItem),
		Components: Components{
			Schemas: map[string]*Schema{
				"Error": {
					Type:     "object",
					Required: []string{"message"},
					Properties: map[string]*Schema{
						"message": {Type: "string"},
						"details": {Type: "string"},
					},
				},
			},
			SecuritySchemes: map[string]SecurityScheme{},
		},
	}

	// Sorted, so the document is the same on every start.
	sorted := append([]*service.Config(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, svc := range sorted {
		switch svc.Protocol {
		case "grpc":
			continue
		case "graphql":
			base := normalizeBase(svc.BasePath)
			item := PathItem{}
			for _, m := range []string{"get", "post"} {
				setOperation(&item, m, operation(svc, m, "", "GraphQL endpoint of "+svc.Name))
			}
			item.Post.RequestBody = &RequestBody{Required: true, Content: map[string]MediaType{
				"application/json": {Schema: &Schema{Type: "object", Required: []string{"query"}, Properties: map[string]*Schema{
					"query":         {Type: "string"},
					"operationName": {Type: "string"},
					"variables":     {Type: "object"},
				}}},
			}}
			spec.Paths[base] = item
		default:
			base := normalizeBase(svc.BasePath)
			root := PathItem{}
			sub := PathItem{Parameters: []Parameter{{
				Name:        "path",
				In:          "path",
				Required:    true,
				Description: "Any path below the base path. It may contain '/'; the gateway forwards it as is.",
				Schema:      &Schema{Type: "string"},
			}}}
			for _, m := range proxiedMethods {
				setOperation(&root, m, operation(svc, m, "", "Forwarded to "+svc.Name+"."))
				setOperation(&sub, m, operation(svc, m, "Path", "Forwarded to "+svc.Name+"."))
			}
			spec.Paths[base] = root
			spec.Paths[strings.TrimSuffix(base, "/")+"/{path}"] = sub
		}

		spec.Tags = append(spec.Tags, Tag{Name: svc.Name, Description: describe(svc)})
		if svc.Authentication {
			spec.Components.SecuritySchemes["bearerAuth"] = SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}
		}
	}
	return spec
}

func describe(svc *service.Config) string {
	var parts []string
	if svc.StripBasePath {
		parts = append(parts, fmt.Sprintf("The upstream receives the path without %s.", svc.BasePath))
	}
	if svc.Authentication {
		parts = append(parts, "Requires a JWT bearer token.")
	}
	if svc.RateLimit > 0 {
		parts = append(parts, fmt.Sprintf("Limited to %d requests per %s per client.", svc.RateLimit, svc.RateLimitWindow))
	}
	return strings.Join(parts, " ")
}

func operation(svc *service.Config, method, suffix, description string) *Operation {
	op := &Operation{
		Summary:     fmt.Sprintf("%s %s", strings.ToUpper(method), svc.Name),
		Description: description,
		OperationID: method + toCamelCase(svc.Name) + suffix,
		Tags:        []string{svc.Name},
		Responses: map[string]Response{
			"default": {Description: "The upstream service's response, passed through."},
			"502":     errorResponse("The upstream could not be reached or failed."),
			"503":     errorResponse("No healthy target, or the circuit breaker is open."),
			"504":     errorResponse("The upstream did not answer in time."),
		},
	}
	if svc.Authentication {
		op.Security = []map[string][]string{{"bearerAuth": {}}}
		op.Responses["401"] = errorResponse("Missing, invalid or expired token.")
	}
	if svc.RateLimit > 0 {
		r := errorResponse("Rate limit exceeded.")
		r.Headers = map[string]Header{"Retry-After": {Description: "Seconds until the next request is allowed.", Schema: &Schema{Type: "integer"}}}
		op.Responses["429"] = r
	}
	return op
}

func errorResponse(description string) Response {
	return Response{Description: description, Content: map[string]MediaType{
		"application/json": {Schema: &Schema{Ref: "#/components/schemas/Error"}},
	}}
}

func normalizeBase(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

func setOperation(item *PathItem, method string, op *Operation) {
	switch method {
	case "get":
		item.Get = op
	case "post":
		item.Post = op
	case "put":
		item.Put = op
	case "delete":
		item.Delete = op
	case "patch":
		item.Patch = op
	case "options":
		item.Options = op
	}
}

// JSON returns the document as indented JSON.
func (s *Spec) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

// YAML returns the document as YAML.
func (s *Spec) YAML() ([]byte, error) {
	return yaml.Marshal(s)
}

func toCamelCase(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(s)
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, "")
}
