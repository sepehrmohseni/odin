package transform

// RequestTransform defines transformations to apply to requests. Values are Go templates; see
// docs/transformations.md.
type RequestTransform struct {
	// Header name to template, e.g. X-User: "{{ .X-User-Id }}". The template sees the request headers.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Query parameter to template; the template sees the current query parameters.
	QueryParams map[string]string `yaml:"queryParams,omitempty" json:"queryParams,omitempty"`
	// Template for the new body; it sees the JSON request body (or .body for other content).
	Body string `yaml:"body,omitempty" json:"body,omitempty"`
}

// ResponseTransform defines transformations to apply to responses
type ResponseTransform struct {
	// Header name to template; the template sees the response headers.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Template for the new body; it sees the JSON response plus .statusCode.
	Body string `yaml:"body,omitempty" json:"body,omitempty"`
}

// MappingRule moves the JSON value at From to To (dot paths such as $.user.name). When From is
// absent, Default (parsed as JSON when it is valid JSON, else used as a string) is set instead.
// These are the `transform:` rules of the service config.
type MappingRule struct {
	From    string
	To      string
	Default string
}
