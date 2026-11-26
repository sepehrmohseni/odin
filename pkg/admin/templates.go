package admin

import (
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

type Template struct {
	templates *template.Template
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

var templateDir = "pkg/admin/templates"

func (h *AdminHandler) initTemplates() {
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		h.logger.Info("Creating templates directory")
		if err := os.MkdirAll(templateDir, 0o750); err != nil {
			h.logger.WithError(err).Fatal("Failed to create templates directory")
		}

		h.createTemplateFile("login.html", loginTemplate)
		h.createTemplateFile("dashboard.html", dashboardTemplate)
		h.createTemplateFile("add_service.html", addServiceTemplate)
		h.createTemplateFile("edit_service.html", editServiceTemplate)
	}
}

func (h *AdminHandler) createTemplateFile(name, content string) {
	path := filepath.Join(templateDir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		h.logger.Infof("Creating template: %s", name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			h.logger.WithError(err).Fatalf("Failed to create template: %s", name)
		}
	}
}

func (h *AdminHandler) renderTemplate(c echo.Context, templateName string, data map[string]interface{}) error {
	acceptHeader := c.Request().Header.Get("Accept")
	if strings.Contains(acceptHeader, "application/json") && !strings.HasPrefix(c.Path(), "/admin") {
		if data == nil {
			data = map[string]interface{}{}
		}
		return c.JSON(http.StatusOK, data)
	}

	var output strings.Builder
	if err := renderPage(&output, templateName, data); err != nil {
		// Fall back to the built-in copy of the core pages when the template files are missing.
		content := getTemplateContent(templateName)
		if content == "Template not found" {
			h.logger.WithError(err).Errorf("Failed to render template: %s", templateName)
			return c.String(http.StatusInternalServerError, "Template error")
		}
		tmpl, perr := template.New(templateName).Parse(content)
		if perr != nil {
			return c.String(http.StatusInternalServerError, "Template error")
		}
		output.Reset()
		if err := tmpl.ExecuteTemplate(&output, templateName, data); err != nil {
			return c.String(http.StatusInternalServerError, "Template execution error")
		}
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return c.HTML(http.StatusOK, output.String())
}

// pageTemplates caches one parsed template set per page.
var pageTemplates sync.Map // page name -> *pageTemplate

type pageTemplate struct {
	set   *template.Template
	entry string // template to execute
}

// renderPage renders one admin page. Each page gets its own template set: the shared layouts and
// partials, then the page itself, parsed last so its "title", "content" and "scripts" win.
// (All pages used to share one set, so every page showed whichever file defined "content" last:
// the traces page.)
func renderPage(w io.Writer, name string, data interface{}) error {
	var pt *pageTemplate
	if cached, ok := pageTemplates.Load(name); ok {
		pt, _ = cached.(*pageTemplate)
	}
	if pt == nil {
		var err error
		if pt, err = parsePage(name); err != nil {
			return err
		}
		pageTemplates.Store(name, pt)
	}
	return pt.set.ExecuteTemplate(w, pt.entry, data)
}

func parsePage(name string) (*pageTemplate, error) {
	pagePath := filepath.Join(templateDir, filepath.Base(name))
	page, err := os.ReadFile(pagePath) //nolint:gosec // a fixed set of page names inside the template directory
	if err != nil {
		return nil, err
	}

	set := template.New("").Funcs(template.FuncMap{
		"hasHook": func(hooks []string, hook string) bool {
			for _, h := range hooks {
				if h == hook {
					return true
				}
			}
			return false
		},
	})
	// Some standalone pages include a "nav" block that no file defines.
	if _, err := set.New("odin-fallbacks").Parse(`{{define "nav"}}{{end}}`); err != nil {
		return nil, err
	}

	files := []string{filepath.Join(templateDir, "base.html"), filepath.Join(templateDir, "layout.html")}
	partials, _ := filepath.Glob(filepath.Join(templateDir, "partials", "*.html"))
	files = append(files, partials...)
	if name != "service_list.html" {
		files = append(files, filepath.Join(templateDir, "service_list.html")) // used by the dashboard
	}
	files = append(files, pagePath)

	existing := files[:0]
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			existing = append(existing, f)
		}
	}
	if _, err := set.ParseFiles(existing...); err != nil {
		return nil, err
	}

	// Pages either define a template named after themselves (most), are complete documents, or
	// only fill the "content" block of layout.html (the Postman page).
	entry := name
	text := string(page)
	selfDefined := strings.Contains(text, `define "`+name+`"`)
	standalone := strings.Contains(strings.ToLower(text), "<html")
	if !selfDefined && !standalone {
		entry = "layout.html"
	}
	return &pageTemplate{set: set, entry: entry}, nil
}

// pageRenderer lets handlers that call c.Render use the same per-page template sets.
type pageRenderer struct{}

func (pageRenderer) Render(w io.Writer, name string, data interface{}, _ echo.Context) error {
	return renderPage(w, name, data)
}

func getTemplateContent(name string) string {
	switch name {
	case "login.html":
		return loginTemplate
	case "dashboard.html":
		return dashboardTemplate
	case "add_service.html":
		return addServiceTemplate
	case "edit_service.html":
		return editServiceTemplate
	case "monitoring.html":
		return "" // Will be loaded from file
	case "service_list.html":
		return `{{define "service_list.html"}}
<table class="table table-striped">
  <thead>
    <tr>
      <th>Name</th>
      <th>Base Path</th>
      <th>Targets</th>
      <th>Actions</th>
    </tr>
  </thead>
  <tbody>
    {{range .Services}}
    <tr>
      <td>{{.Name}}</td>
      <td>{{.BasePath}}</td>
      <td>{{index .Targets 0}}</td>
      <td>
        <a href="/admin/services/{{.Name}}" class="btn btn-sm btn-outline-primary">Edit</a>
        <button
          class="btn btn-sm btn-outline-danger"
          hx-delete="/admin/services/{{.Name}}"
          hx-confirm="Are you sure you want to delete this service?"
          hx-target="#service-list"
          hx-swap="innerHTML"
        >Delete</button>
      </td>
    </tr>
    {{end}}
  </tbody>
</table>
{{end}}`
	default:
		return "Template not found"
	}
}

const (
	loginTemplate = `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>Login - Odin API Gateway</title>
	<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
	<script src="https://unpkg.com/htmx.org@1.9.11"></script>
	<style>
		body {
			display: flex;
			align-items: center;
			padding-top: 40px;
			padding-bottom: 40px;
			background-color: #f5f5f5;
			height: 100vh;
		}
		.form-signin {
			width: 100%;
			max-width: 330px;
			padding: 15px;
			margin: auto;
		}
	</style>
</head>
<body class="text-center">
	<main class="form-signin">
		<form hx-post="/admin/login" hx-target="#login-message">
			<h1 class="h3 mb-3 fw-normal">Odin API Gateway</h1>
			<h2 class="h5 mb-3 fw-normal">Admin Login</h2>
			<div id="login-message"></div>
			
			<div class="form-floating mb-3">
				<input type="text" class="form-control" id="username" name="username" placeholder="Username" required>
				<label for="username">Username</label>
			</div>
			<div class="form-floating mb-3">
				<input type="password" class="form-control" id="password" name="password" placeholder="Password" required>
				<label for="password">Password</label>
			</div>
			
			<button class="w-100 btn btn-lg btn-primary" type="submit">Sign in</button>
		</form>
	</main>
	<script>
		document.addEventListener('htmx:afterSwap', function(event) {
			const redirectTo = event.detail.xhr.getResponseHeader('HX-Redirect');
			if (redirectTo) {
				window.location.href = redirectTo;
			}
		});
	</script>
</body>
</html>`

	dashboardTemplate = `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>Dashboard - Odin API Gateway</title>
	<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
	<script src="https://unpkg.com/htmx.org@1.9.11"></script>
</head>
<body>
	<div class="container py-4">
		<header class="d-flex justify-content-between align-items-center pb-3 mb-4 border-bottom">
			<h1>Odin API Gateway</h1>
			<div>
				<a href="/admin/services/new" class="btn btn-primary">Add Service</a>
				<a href="/admin/login" class="btn btn-secondary ms-2">Logout</a>
			</div>
		</header>
		
		<main>
			<h2>Service Configuration</h2>
			<p class="lead">Manage your gateway service configurations.</p>
			
			<div id="service-list" hx-get="/admin/services" hx-trigger="load">
				<div class="d-flex justify-content-center">
					<div class="spinner-border" role="status">
						<span class="visually-hidden">Loading...</span>
					</div>
				</div>
			</div>
		</main>
		
		<footer class="pt-5 my-5 text-muted border-top">
			&copy; 2023-2024 Odin API Gateway
		</footer>
	</div>
</body>
</html>`

	addServiceTemplate  = `<!-- Add Service Template would go here -->`
	editServiceTemplate = `<!-- Edit Service Template would go here -->`
)
