package admin

import (
	"context"
	"net/http"
	"time"

	"odin/pkg/config"
	"odin/pkg/mongodb"

	"github.com/labstack/echo/v4"
)

// storedServiceRequest is a service as the stored-services API takes it.
type storedServiceRequest struct {
	Name           string            `json:"name"`
	BasePath       string            `json:"basePath"`
	Targets        []string          `json:"targets"`
	StripBasePath  bool              `json:"stripBasePath"`
	Timeout        string            `json:"timeout"`
	RetryCount     int               `json:"retryCount"`
	RetryDelay     string            `json:"retryDelay"`
	Authentication bool              `json:"authentication"`
	LoadBalancing  string            `json:"loadBalancing"`
	Headers        map[string]string `json:"headers"`
	Protocol       string            `json:"protocol"`
}

// storedServiceView is a stored service as the API returns it.
type storedServiceView struct {
	Name           string            `json:"name"`
	BasePath       string            `json:"basePath"`
	Targets        []string          `json:"targets"`
	StripBasePath  bool              `json:"stripBasePath"`
	Timeout        string            `json:"timeout"`
	RetryCount     int               `json:"retryCount"`
	RetryDelay     string            `json:"retryDelay"`
	Authentication bool              `json:"authentication"`
	LoadBalancing  string            `json:"loadBalancing"`
	Headers        map[string]string `json:"headers,omitempty"`
	Protocol       string            `json:"protocol"`
	Served         bool              `json:"served"` // the running gateway routes it
}

// SetStoredServices enables /admin/api/mongodb/services, for the services kept in MongoDB
// (what odin-migrate writes). served lists the stored services the gateway loaded at startup
// (mongodb.loadServices), which the dashboard shows next to the config file's.
func (h *AdminHandler) SetStoredServices(adapter *mongodb.ServiceAdapter, served []config.ServiceConfig) {
	h.storedAdapter = adapter
	h.storedServed = served
}

func (h *AdminHandler) registerStoredServiceRoutes(g *echo.Group) {
	if h.storedAdapter == nil {
		return
	}
	g.GET("/api/mongodb/services", h.listStoredServices)
	g.GET("/api/mongodb/services/:name", h.getStoredService)
	g.POST("/api/mongodb/services", h.createStoredService)
	g.PUT("/api/mongodb/services/:name", h.updateStoredService)
	g.DELETE("/api/mongodb/services/:name", h.deleteStoredService)
}

func storedCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (h *AdminHandler) servedNames() map[string]bool {
	m := map[string]bool{}
	for _, s := range h.storedServed {
		m[s.Name] = true
	}
	return m
}

func viewOf(s config.ServiceConfig, served bool) storedServiceView {
	return storedServiceView{
		Name: s.Name, BasePath: s.BasePath, Targets: s.Targets, StripBasePath: s.StripBasePath,
		Timeout: s.Timeout.String(), RetryCount: s.RetryCount, RetryDelay: s.RetryDelay.String(),
		Authentication: s.Authentication, LoadBalancing: s.LoadBalancing, Headers: s.Headers,
		Protocol: s.Protocol, Served: served,
	}
}

func (h *AdminHandler) listStoredServices(c echo.Context) error {
	ctx, cancel := storedCtx()
	defer cancel()
	list, err := h.storedAdapter.LoadServices(ctx)
	if err != nil {
		h.logger.WithError(err).Error("Failed to list stored services")
		return echo.NewHTTPError(http.StatusServiceUnavailable, "MongoDB is not available")
	}
	served := h.servedNames()
	out := make([]storedServiceView, 0, len(list))
	for _, s := range list {
		out = append(out, viewOf(s, served[s.Name]))
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"services": out})
}

func (h *AdminHandler) getStoredService(c echo.Context) error {
	ctx, cancel := storedCtx()
	defer cancel()
	s, err := h.storedAdapter.GetService(ctx, c.Param("name"))
	if err != nil || s == nil {
		return echo.NewHTTPError(http.StatusNotFound, "No such stored service")
	}
	return c.JSON(http.StatusOK, viewOf(*s, h.servedNames()[s.Name]))
}

// parseStored validates a request with the same rules as the admin's service form.
func parseStored(req *storedServiceRequest) (*config.ServiceConfig, error) {
	if err := validateServiceName(req.Name); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := validateBasePath(req.BasePath); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := validateTargets(req.Targets); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	svc := &config.ServiceConfig{
		Name: req.Name, BasePath: req.BasePath, Targets: req.Targets, StripBasePath: req.StripBasePath,
		RetryCount: req.RetryCount, Authentication: req.Authentication, LoadBalancing: req.LoadBalancing,
		Headers: req.Headers, Protocol: req.Protocol,
	}
	for _, d := range []struct {
		in  string
		out *time.Duration
	}{{req.Timeout, &svc.Timeout}, {req.RetryDelay, &svc.RetryDelay}} {
		if d.in == "" {
			continue
		}
		v, err := time.ParseDuration(d.in)
		if err != nil || v < 0 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid duration "+d.in)
		}
		*d.out = v
	}
	svc.SetDefaults()
	return svc, nil
}

const appliesOnRestart = "Saved in MongoDB. The gateway serves it after a restart (with mongodb.loadServices)."

func (h *AdminHandler) createStoredService(c echo.Context) error {
	var req storedServiceRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
	}
	svc, err := parseStored(&req)
	if err != nil {
		return err
	}
	ctx, cancel := storedCtx()
	defer cancel()
	if existing, _ := h.storedAdapter.GetService(ctx, svc.Name); existing != nil {
		return echo.NewHTTPError(http.StatusConflict, "A stored service with this name exists")
	}
	if err := h.storedAdapter.SaveService(ctx, svc); err != nil {
		h.logger.WithError(err).Error("Failed to save stored service")
		return echo.NewHTTPError(http.StatusServiceUnavailable, "MongoDB is not available")
	}
	return c.JSON(http.StatusCreated, map[string]string{"message": appliesOnRestart, "service": svc.Name})
}

func (h *AdminHandler) updateStoredService(c echo.Context) error {
	var req storedServiceRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
	}
	name := c.Param("name")
	if req.Name == "" {
		req.Name = name
	}
	if req.Name != name {
		return echo.NewHTTPError(http.StatusBadRequest, "The name in the path and the body differ")
	}
	svc, err := parseStored(&req)
	if err != nil {
		return err
	}
	ctx, cancel := storedCtx()
	defer cancel()
	if err := h.storedAdapter.UpdateService(ctx, name, svc); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "No such stored service")
	}
	return c.JSON(http.StatusOK, map[string]string{"message": appliesOnRestart})
}

func (h *AdminHandler) deleteStoredService(c echo.Context) error {
	ctx, cancel := storedCtx()
	defer cancel()
	if err := h.storedAdapter.DeleteService(ctx, c.Param("name")); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "No such stored service")
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Deleted from MongoDB. The gateway stops serving it after a restart."})
}
