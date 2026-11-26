package admin

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"odin/pkg/config"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

func (h *AdminHandler) handleListServices(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()
	return h.renderServiceList(c)
}

// renderServiceList writes the service table. Callers hold configMu. Every value is escaped:
// service names and targets come from admin input and must not be able to inject markup.
func (h *AdminHandler) renderServiceList(c echo.Context) error {
	if len(h.config.Services) == 0 {
		return c.HTML(http.StatusOK, `<div class="alert alert-info">No services configured. Add a new service to get started.</div>`)
	}

	out := `<table class="table table-striped mt-4">
		<thead>
			<tr>
				<th>Name</th>
				<th>Base Path</th>
				<th>Targets</th>
				<th>Authentication</th>
				<th>Actions</th>
			</tr>
		</thead>
		<tbody>
	`

	for _, svc := range h.config.Services {
		targets := ""
		if len(svc.Targets) > 0 {
			targets = svc.Targets[0]
		}
		if len(svc.Targets) > 1 {
			targets += " +" + fmt.Sprint(len(svc.Targets)-1)
		}

		authStatus := "Required"
		if !svc.Authentication {
			authStatus = "Not Required"
		}

		out += fmt.Sprintf(`
			<tr>
				<td>%s</td>
				<td>%s</td>
				<td>%s</td>
				<td>%s</td>
				<td>
					<a href="/admin/services/%s" class="btn btn-sm btn-outline-primary">Edit</a>
					<button class="btn btn-sm btn-outline-danger" 
							hx-delete="/admin/services/%s" 
							hx-confirm="Are you sure you want to delete this service?"
							hx-target="#service-list"
							hx-swap="innerHTML">Delete</button>
				</td>
			</tr>
		`, html.EscapeString(svc.Name), html.EscapeString(svc.BasePath), html.EscapeString(targets), authStatus,
			html.EscapeString(url.PathEscape(svc.Name)), html.EscapeString(url.PathEscape(svc.Name)))
	}

	out += `
		</tbody>
	</table>
	`

	return c.HTML(http.StatusOK, out)
}

func (h *AdminHandler) handleNewService(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()
	data := map[string]interface{}{
		"AvailableServices": h.config.Services,
	}
	return h.renderTemplate(c, "add_service.html", data)
}

func (h *AdminHandler) handleEditService(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()
	name := c.Param("name")

	var svc *config.ServiceConfig
	for _, s := range h.config.Services {
		if s.Name == name {
			svc = &s
			break
		}
	}

	if svc == nil {
		return c.HTML(http.StatusNotFound, `<div class="alert alert-danger">Service not found</div>`)
	}

	availableServices := make([]config.ServiceConfig, 0)
	for _, s := range h.config.Services {
		if s.Name != name {
			availableServices = append(availableServices, s)
		}
	}

	data := map[string]interface{}{
		"Service":           svc,
		"AvailableServices": availableServices,
		"StripBasePathYes":  svc.StripBasePath,
		"StripBasePathNo":   !svc.StripBasePath,
		"AuthYes":           svc.Authentication,
		"AuthNo":            !svc.Authentication,
		"LoadBalancingRR":   svc.LoadBalancing == "round-robin" || svc.LoadBalancing == "",
		"LoadBalancingRand": svc.LoadBalancing == "random",
	}

	return h.renderTemplate(c, "edit_service.html", data)
}

func (h *AdminHandler) handleAddService(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	name := strings.TrimSpace(c.FormValue("name"))
	basePath := strings.TrimSpace(c.FormValue("basePath"))
	if err := validateServiceName(name); err != nil {
		return alertHTML(c, http.StatusBadRequest, err.Error())
	}
	if err := validateBasePath(basePath); err != nil {
		return alertHTML(c, http.StatusBadRequest, err.Error())
	}

	for _, svc := range h.config.Services {
		if svc.Name == name {
			return c.HTML(http.StatusBadRequest, `<div class="alert alert-danger">A service with this name already exists</div>`)
		}
	}

	targets := parseMultilineInput(c.FormValue("targets"))
	if err := validateTargets(targets); err != nil {
		return alertHTML(c, http.StatusBadRequest, err.Error())
	}

	enableAggregation := c.FormValue("enableAggregation") == "on"
	aggregationConfig := parseAggregationConfig(c, enableAggregation)

	newSvc := config.ServiceConfig{
		Name:           name,
		BasePath:       basePath,
		Targets:        targets,
		StripBasePath:  c.FormValue("stripBasePath") == "true",
		Authentication: c.FormValue("authentication") == "true",
		LoadBalancing:  c.FormValue("loadBalancing"),
		Aggregation:    aggregationConfig,
		RetryCount:     1,
		RetryDelay:     100 * time.Millisecond,
		Timeout:        5 * time.Second,
	}

	h.config.Services = append(h.config.Services, newSvc)

	if err := h.saveConfig(); err != nil {
		h.config.Services = h.config.Services[:len(h.config.Services)-1]
		return alertHTML(c, http.StatusInternalServerError, "Failed to save configuration: "+err.Error())
	}

	return c.HTML(http.StatusOK, `
		<div class="alert alert-success">
			Service added. Restart the gateway to start routing it.
			<a href="/admin/dashboard">Return to dashboard</a>
		</div>
	`)
}

func (h *AdminHandler) handleUpdateService(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	name := c.Param("name")

	var svcIndex = -1
	for i, svc := range h.config.Services {
		if svc.Name == name {
			svcIndex = i
			break
		}
	}

	if svcIndex == -1 {
		return c.HTML(http.StatusNotFound, `<div class="alert alert-danger">Service not found</div>`)
	}
	if src := h.config.Services[svcIndex].Source; src != "" {
		return alertHTML(c, http.StatusConflict, "This service is defined in "+src+"; edit that file instead.")
	}

	basePath := strings.TrimSpace(c.FormValue("basePath"))
	if err := validateBasePath(basePath); err != nil {
		return alertHTML(c, http.StatusBadRequest, err.Error())
	}
	targets := parseMultilineInput(c.FormValue("targets"))
	if err := validateTargets(targets); err != nil {
		return alertHTML(c, http.StatusBadRequest, err.Error())
	}

	// Parse timeout
	timeoutStr := c.FormValue("timeout")
	timeout, err := strconv.Atoi(timeoutStr)
	if err != nil || timeout <= 0 {
		return c.HTML(http.StatusBadRequest, `<div class="alert alert-danger">Invalid timeout value</div>`)
	}

	// Parse retry count
	retryCountStr := c.FormValue("retryCount")
	retryCount, err := strconv.Atoi(retryCountStr)
	if err != nil || retryCount < 0 {
		return c.HTML(http.StatusBadRequest, `<div class="alert alert-danger">Invalid retry count value</div>`)
	}

	// Update service configuration
	previous := h.config.Services[svcIndex]
	h.config.Services[svcIndex].BasePath = basePath
	h.config.Services[svcIndex].Targets = targets
	h.config.Services[svcIndex].StripBasePath = c.FormValue("stripBasePath") == "true"
	h.config.Services[svcIndex].Authentication = c.FormValue("authentication") == "true"
	h.config.Services[svcIndex].LoadBalancing = c.FormValue("loadBalancing")
	h.config.Services[svcIndex].Timeout = time.Duration(timeout) * time.Second
	h.config.Services[svcIndex].RetryCount = retryCount

	// Save configuration
	if err := h.saveConfig(); err != nil {
		h.config.Services[svcIndex] = previous
		return c.HTML(http.StatusInternalServerError, `<div class="alert alert-danger">Failed to save configuration</div>`)
	}

	h.logger.WithFields(logrus.Fields{
		"service": name,
		"action":  "update",
	}).Info("Service updated")

	return c.HTML(http.StatusOK, `<div class="alert alert-success">Service updated. Restart the gateway to apply the change.</div>`)
}

func (h *AdminHandler) handleDeleteService(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	name := c.Param("name")

	index := -1
	for i, svc := range h.config.Services {
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return c.HTML(http.StatusNotFound, `<div class="alert alert-danger">Service not found</div>`)
	}
	if src := h.config.Services[index].Source; src != "" {
		return alertHTML(c, http.StatusConflict, "This service is defined in "+src+"; edit that file instead.")
	}

	previous := h.config.Services
	h.config.Services = append(append([]config.ServiceConfig{}, previous[:index]...), previous[index+1:]...)

	if err := h.saveConfig(); err != nil {
		h.config.Services = previous
		return alertHTML(c, http.StatusInternalServerError, "Failed to save configuration: "+err.Error())
	}

	return h.renderServiceList(c)
}

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("service name must be 1-64 letters, digits, '.', '_' or '-'")
	}
	return nil
}

func validateBasePath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\n\"'<>") {
		return fmt.Errorf("base path must start with '/' and contain no spaces or quotes")
	}
	return nil
}

func validateTargets(targets []string) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one target URL is required")
	}
	for _, t := range targets {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("invalid target %q: use http(s)://host[:port]", t)
		}
	}
	return nil
}

// alertHTML renders an error alert with the message escaped.
func alertHTML(c echo.Context, status int, message string) error {
	return c.HTML(status, `<div class="alert alert-danger">`+html.EscapeString(message)+`</div>`)
}

func parseMultilineInput(input string) []string {
	lines := strings.Split(input, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func parseAggregationConfig(c echo.Context, enabled bool) *config.AggregationConfig {
	if !enabled {
		return nil
	}

	aggregationConfig := &config.AggregationConfig{
		Dependencies: []config.DependencyConfig{},
	}

	depCount := 0
	for i := 0; ; i++ {
		depServiceName := c.FormValue(fmt.Sprintf("dependencies[%d].service", i))
		if depServiceName == "" {
			break
		}
		depCount++
	}

	dependencies := make([]config.DependencyConfig, depCount)
	for i := 0; i < depCount; i++ {
		depServiceName := c.FormValue(fmt.Sprintf("dependencies[%d].service", i))
		depPath := c.FormValue(fmt.Sprintf("dependencies[%d].path", i))

		dependency := config.DependencyConfig{
			Service:          depServiceName,
			Path:             depPath,
			ParameterMapping: []config.MappingConfig{},
			ResultMapping:    []config.MappingConfig{},
		}

		for j := 0; ; j++ {
			fromPath := c.FormValue(fmt.Sprintf("dependencies[%d].paramMapping[%d].from", i, j))
			toParam := c.FormValue(fmt.Sprintf("dependencies[%d].paramMapping[%d].to", i, j))

			if fromPath == "" || toParam == "" {
				break
			}

			dependency.ParameterMapping = append(dependency.ParameterMapping, config.MappingConfig{
				From: fromPath,
				To:   toParam,
			})
		}

		for j := 0; ; j++ {
			fromPath := c.FormValue(fmt.Sprintf("dependencies[%d].resultMapping[%d].from", i, j))
			toPath := c.FormValue(fmt.Sprintf("dependencies[%d].resultMapping[%d].to", i, j))

			if fromPath == "" || toPath == "" {
				break
			}

			dependency.ResultMapping = append(dependency.ResultMapping, config.MappingConfig{
				From: fromPath,
				To:   toPath,
			})
		}

		dependencies[i] = dependency
	}

	aggregationConfig.Dependencies = dependencies
	return aggregationConfig
}

// handleIntegrationsPostman renders the Postman integration page
func (h *AdminHandler) handleIntegrationsPostman(c echo.Context) error {
	return h.renderTemplate(c, "integrations_postman.html", nil)
}
