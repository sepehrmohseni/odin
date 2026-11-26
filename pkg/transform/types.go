package transform

// RequestTransform defines transformations to apply to requests. Values are Go templates; see
// docs/transformations.md.
type RequestTransform struct {
	Headers     map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	QueryParams map[string]string `yaml:"queryParams,omitempty" json:"queryParams,omitempty"`
	Body        string            `yaml:"body,omitempty" json:"body,omitempty"`
}

// ResponseTransform defines transformations to apply to responses
type ResponseTransform struct {
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body    string            `yaml:"body,omitempty" json:"body,omitempty"`
}

// MappingRule moves the JSON value at From to To (dot paths such as $.user.name). When From is
// absent, Default (parsed as JSON when it is valid JSON, else used as a string) is set instead.
// These are the `transform:` rules of the service config.
type MappingRule struct {
	From    string
	To      string
	Default string
}
