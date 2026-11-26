package openapi

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"odin/pkg/config"
	"odin/pkg/service"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Swagger UI is loaded from jsDelivr, pinned to one release and checked with Subresource
// Integrity, so the gateway binary does not carry 2 MB of JavaScript. The hashes are of
// swagger-ui-dist 5.36.0 as published on npm (registry integrity sha512-Lt6/Kb0g...).
const (
	swaggerUIBase   = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.36.0/"
	swaggerUIJSSRI  = "sha384-fUpRgC19yPykTqzdrUbdAg8NTiu16NEKf9U12J+IJWCg8/1mEI8f2+PEIz60Wyd+"
	swaggerUICSSSRI = "sha384-z/AtSaOHGn5MJ5auAHXUypohywlIWLBZo1jeGmhdpVIbCnFhxAH92cwdV41oyINc"

	// DefaultUIPath is where the UI is served when openapi.uiPath is empty.
	DefaultUIPath = "/openapi"
)

// Document is the OpenAPI document in both encodings.
type Document struct {
	JSON []byte
	YAML []byte
}

// Build produces the document for cfg: generated from services, or, with autoGenerate set to
// false, read from cfg.OutputPath (JSON or YAML). A generated document is also written to
// OutputPath when one is set.
func Build(cfg config.OpenAPIConfig, services []*service.Config, logger *logrus.Logger) (*Document, error) {
	if cfg.AutoGenerate != nil && !*cfg.AutoGenerate {
		if cfg.OutputPath == "" {
			return nil, fmt.Errorf("openapi.autoGenerate is false, so openapi.outputPath must name the document to serve")
		}
		return load(cfg.OutputPath)
	}

	spec := Generate(Options{Title: cfg.Title, Version: cfg.Version, Description: cfg.Description}, services)
	doc := &Document{}
	var err error
	if doc.JSON, err = spec.JSON(); err != nil {
		return nil, err
	}
	if doc.YAML, err = spec.YAML(); err != nil {
		return nil, err
	}
	if cfg.OutputPath != "" {
		data := doc.JSON
		if isYAML(cfg.OutputPath) {
			data = doc.YAML
		}
		// The document is public (it is served to anyone), so it is world-readable. A failed
		// write is logged, not fatal: in the containers the config directory is read-only.
		if err := os.WriteFile(cfg.OutputPath, data, 0o644); err != nil { //nolint:gosec // public document
			logger.WithError(err).WithField("path", cfg.OutputPath).Warn("Could not write the OpenAPI document")
		}
	}
	return doc, nil
}

func load(path string) (*Document, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading openapi.outputPath: %w", err)
	}
	// JSON is YAML, so one decoder reads both.
	var v interface{}
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	m, ok := v.(map[string]interface{})
	if !ok || (m["openapi"] == nil && m["swagger"] == nil) {
		return nil, fmt.Errorf("%s is not an OpenAPI document (no openapi or swagger field)", path)
	}
	doc := &Document{}
	if doc.JSON, err = json.MarshalIndent(m, "", "  "); err != nil {
		return nil, fmt.Errorf("converting %s to JSON: %w", path, err)
	}
	if doc.YAML, err = yaml.Marshal(m); err != nil {
		return nil, err
	}
	return doc, nil
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// Register serves the document at /openapi.json and /openapi.yaml, and with ui the Swagger UI
// page at uiPath (its start-up script at uiPath + "/init.js").
func Register(e *echo.Echo, doc *Document, title string, ui bool, uiPath string) {
	e.GET("/openapi.json", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "application/json", doc.JSON)
	})
	e.GET("/openapi.yaml", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "application/yaml", doc.YAML)
	})
	if !ui {
		return
	}
	if uiPath == "" {
		uiPath = DefaultUIPath
	}
	uiPath = "/" + strings.Trim(uiPath, "/")
	if title == "" {
		title = "Odin API Gateway"
	}
	page := []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<link rel="stylesheet" href="` + swaggerUIBase + `swagger-ui.css" integrity="` + swaggerUICSSSRI + `" crossorigin="anonymous">
</head>
<body>
<div id="swagger-ui"></div>
<script src="` + swaggerUIBase + `swagger-ui-bundle.js" integrity="` + swaggerUIJSSRI + `" crossorigin="anonymous"></script>
<script src="` + html.EscapeString(uiPath) + `/init.js"></script>
</body>
</html>
`)
	initJS := []byte(`window.ui = SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });` + "\n")
	csp := "default-src 'none'; script-src 'self' https://cdn.jsdelivr.net; " +
		"style-src 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data:; connect-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

	e.GET(uiPath, func(c echo.Context) error {
		c.Response().Header().Set("Content-Security-Policy", csp)
		return c.HTMLBlob(http.StatusOK, page)
	})
	e.GET(uiPath+"/init.js", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "text/javascript; charset=utf-8", initJS)
	})
}
