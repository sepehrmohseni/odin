package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"odin/pkg/plugins"

	"github.com/labstack/echo/v4"
)

// MiddlewareAPIHandler serves the Middleware page: the live middleware plugin chain (see
// plugins.PhaseMiddleware) and changes to it. Changes apply on the next request; with MongoDB
// they are also saved to the plugin's record, so they survive a restart.
type MiddlewareAPIHandler struct {
	manager *plugins.PluginManager
	repo    *plugins.PluginRepository // nil without MongoDB
}

// NewMiddlewareAPIHandler creates the handler; repo may be nil.
func NewMiddlewareAPIHandler(manager *plugins.PluginManager, repo *plugins.PluginRepository) *MiddlewareAPIHandler {
	return &MiddlewareAPIHandler{manager: manager, repo: repo}
}

// RegisterMiddlewareAPIRoutes mounts the API on the authenticated admin group. (It existed but
// was never mounted, so the Middleware page failed to load. A "compile" stub and bulk
// enable/disable endpoints, which only flipped database flags, are gone.)
func (h *MiddlewareAPIHandler) RegisterMiddlewareAPIRoutes(adminGroup *echo.Group) {
	adminGroup.GET("/api/middleware/chain", h.getMiddlewareChain)
	adminGroup.GET("/api/middleware/chain/stats", h.getMiddlewareStats)
	adminGroup.POST("/api/middleware/chain/reorder", h.reorderMiddlewareChain)
	adminGroup.POST("/api/middleware/reload-all", h.reloadAllMiddleware)

	adminGroup.POST("/api/middleware/:name/register", h.registerMiddleware)
	adminGroup.DELETE("/api/middleware/:name/unregister", h.unregisterMiddleware)
	adminGroup.PUT("/api/middleware/:name/priority", h.updateMiddlewarePriority)
	adminGroup.PUT("/api/middleware/:name/routes", h.updateMiddlewareRoutes)
	adminGroup.PUT("/api/middleware/:name/phase", h.updateMiddlewarePhase)
	adminGroup.POST("/api/middleware/:name/test", h.testMiddleware)
	adminGroup.GET("/api/middleware/:name/health", h.getMiddlewareHealth)
}

func dbContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (h *MiddlewareAPIHandler) record(name string) *plugins.PluginRecord {
	if h.repo == nil {
		return nil
	}
	ctx, cancel := dbContext()
	defer cancel()
	rec, err := h.repo.GetPlugin(ctx, name)
	if err != nil {
		return nil
	}
	return rec
}

func (h *MiddlewareAPIHandler) entry(name string) (plugins.MiddlewareEntry, bool) {
	for _, e := range h.manager.GetMiddlewareChain() {
		if e.Name == name {
			return e, true
		}
	}
	return plugins.MiddlewareEntry{}, false
}

func badRequest(c echo.Context, format string, args ...interface{}) error {
	return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func (h *MiddlewareAPIHandler) getMiddlewareChain(c echo.Context) error {
	type item struct {
		Name        string   `json:"name"`
		Priority    int      `json:"priority"`
		Routes      []string `json:"routes"`
		Phase       string   `json:"phase"`
		Version     string   `json:"version,omitempty"`
		Description string   `json:"description,omitempty"`
		Enabled     bool     `json:"enabled"`
		Loaded      bool     `json:"loaded"`
	}
	chain := h.manager.GetMiddlewareChain()
	out := make([]item, 0, len(chain))
	for _, e := range chain {
		it := item{Name: e.Name, Priority: e.Priority, Routes: e.Routes, Phase: e.Phase, Enabled: true, Loaded: true}
		if e.Middleware != nil {
			it.Version = e.Middleware.Version()
		}
		if rec := h.record(e.Name); rec != nil {
			it.Description = rec.Description
			it.Enabled = rec.Enabled
		}
		out = append(out, it)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"chain": out, "totalCount": len(out)})
}

func (h *MiddlewareAPIHandler) getMiddlewareStats(c echo.Context) error {
	chain := h.manager.GetMiddlewareChain()
	byPhase := map[string]int{}
	global, specific, sum := 0, 0, 0
	for _, e := range chain {
		byPhase[e.Phase]++
		if len(e.Routes) == 1 && e.Routes[0] == "*" {
			global++
		} else {
			specific++
		}
		sum += e.Priority
	}
	avg := 0.0
	if len(chain) > 0 {
		avg = float64(sum) / float64(len(chain))
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"totalMiddlewares":  len(h.manager.ListMiddlewares()),
		"activeInChain":     len(chain),
		"globalMiddlewares": global,
		"routeSpecific":     specific,
		"byPhase":           byPhase,
		"averagePriority":   avg,
	})
}

func (h *MiddlewareAPIHandler) reorderMiddlewareChain(c echo.Context) error {
	var req struct {
		Order []struct {
			Name     string `json:"name"`
			Priority int    `json:"priority"`
		} `json:"order"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	for _, it := range req.Order {
		if it.Priority < 0 || it.Priority > 1000 {
			return badRequest(c, "priority of %s must be between 0 and 1000", it.Name)
		}
	}
	for _, it := range req.Order {
		if err := h.manager.UpdateMiddlewarePriority(it.Name, it.Priority); err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		h.persistPriority(c, it.Name, it.Priority)
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Middleware chain reordered"})
}

func (h *MiddlewareAPIHandler) persistPriority(c echo.Context, name string, priority int) {
	if h.repo == nil {
		return
	}
	ctx, cancel := dbContext()
	defer cancel()
	if err := h.repo.UpdatePluginPriority(ctx, name, priority); err != nil {
		c.Logger().Errorf("Failed to save the priority of %s: %v", name, err)
	}
}

// registerMiddleware puts a loaded middleware (back) into the chain.
func (h *MiddlewareAPIHandler) registerMiddleware(c echo.Context) error {
	name := c.Param("name")
	var req struct {
		Priority int      `json:"priority"`
		Routes   []string `json:"routes"`
		Phase    string   `json:"phase"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	phase, err := plugins.NormalizePhase(req.Phase)
	if err != nil {
		return badRequest(c, "%v", err)
	}
	if len(req.Routes) == 0 {
		req.Routes = []string{"*"}
	}
	mw, ok := h.manager.GetMiddleware(name)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("middleware %s is not loaded; enable its plugin first", name)})
	}
	if err := h.manager.RegisterMiddleware(name, mw, req.Priority, req.Routes, string(phase)); err != nil {
		return badRequest(c, "%v", err)
	}
	if rec := h.record(name); rec != nil {
		rec.Priority, rec.AppliedTo, rec.Phase = req.Priority, req.Routes, string(phase)
		ctx, cancel := dbContext()
		defer cancel()
		if err := h.repo.UpdatePlugin(ctx, rec); err != nil {
			c.Logger().Errorf("Failed to save the registration of %s: %v", name, err)
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"message": fmt.Sprintf("Middleware %s registered", name)})
}

// unregisterMiddleware takes a middleware out of the chain until it is registered again or the
// gateway restarts; disable its plugin to keep it out.
func (h *MiddlewareAPIHandler) unregisterMiddleware(c echo.Context) error {
	name := c.Param("name")
	if _, ok := h.entry(name); !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("middleware %s is not in the chain", name)})
	}
	if err := h.manager.UnregisterMiddleware(name); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": fmt.Sprintf("Middleware %s removed from the chain", name)})
}

func (h *MiddlewareAPIHandler) updateMiddlewarePriority(c echo.Context) error {
	name := c.Param("name")
	var req struct {
		Priority int `json:"priority"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	if req.Priority < 0 || req.Priority > 1000 {
		return badRequest(c, "priority must be between 0 and 1000")
	}
	if err := h.manager.UpdateMiddlewarePriority(name, req.Priority); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	h.persistPriority(c, name, req.Priority)
	return c.JSON(http.StatusOK, map[string]string{"message": fmt.Sprintf("Priority updated to %d", req.Priority)})
}

func (h *MiddlewareAPIHandler) updateMiddlewareRoutes(c echo.Context) error {
	name := c.Param("name")
	var req struct {
		Routes []string `json:"routes"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	if len(req.Routes) == 0 {
		return badRequest(c, "give at least one route pattern; * matches every service route")
	}
	if err := h.manager.UpdateMiddlewareRoutes(name, req.Routes); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	if h.repo != nil {
		ctx, cancel := dbContext()
		defer cancel()
		if err := h.repo.UpdatePluginRoutes(ctx, name, req.Routes); err != nil {
			c.Logger().Errorf("Failed to save the routes of %s: %v", name, err)
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Routes updated"})
}

// updateMiddlewarePhase moves a middleware to another phase of the pipeline. (It used to only
// change the database record, so nothing changed until a restart, and it accepted phase names
// the pipeline does not have.)
func (h *MiddlewareAPIHandler) updateMiddlewarePhase(c echo.Context) error {
	name := c.Param("name")
	var req struct {
		Phase string `json:"phase"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	phase, err := plugins.NormalizePhase(req.Phase)
	if err != nil {
		return badRequest(c, "%v", err)
	}
	e, ok := h.entry(name)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("middleware %s is not in the chain", name)})
	}
	if err := h.manager.RegisterMiddleware(name, e.Middleware, e.Priority, e.Routes, string(phase)); err != nil {
		return badRequest(c, "%v", err)
	}
	if rec := h.record(name); rec != nil {
		rec.Phase = string(phase)
		ctx, cancel := dbContext()
		defer cancel()
		if err := h.repo.UpdatePlugin(ctx, rec); err != nil {
			c.Logger().Errorf("Failed to save the phase of %s: %v", name, err)
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"message": fmt.Sprintf("Phase set to %s", phase)})
}

// reloadAllMiddleware loads the enabled middleware plugin records again (MongoDB only).
func (h *MiddlewareAPIHandler) reloadAllMiddleware(c echo.Context) error {
	if h.repo == nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "plugin records need MongoDB"})
	}
	ctx, cancel := dbContext()
	defer cancel()
	records, err := h.repo.GetMiddlewarePlugins(ctx, true)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	reloaded, failed := 0, 0
	for _, rec := range records {
		_ = h.manager.UnloadRecord(rec.Name)
		if err := h.manager.LoadRecord(rec); err != nil {
			c.Logger().Errorf("Failed to reload middleware %s: %v", rec.Name, err)
			failed++
		} else {
			reloaded++
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"reloaded": reloaded, "errors": failed, "totalPlugins": len(records)})
}

// testMiddleware runs one middleware on a synthetic request, outside the pipeline.
func (h *MiddlewareAPIHandler) testMiddleware(c echo.Context) error {
	name := c.Param("name")
	var req struct {
		Method  string                 `json:"method"`
		Path    string                 `json:"path"`
		Headers map[string]interface{} `json:"headers"`
		Body    interface{}            `json:"body"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid request: %v", err)
	}
	if _, ok := h.manager.GetMiddleware(name); !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("middleware %s is not loaded", name)})
	}
	tester := h.manager.GetTester()
	if tester == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "middleware tester not available"})
	}
	result, err := tester.TestMiddleware(name, map[string]interface{}{
		"method": req.Method, "path": req.Path, "headers": req.Headers, "body": req.Body,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"result": result})
}

func (h *MiddlewareAPIHandler) getMiddlewareHealth(c echo.Context) error {
	name := c.Param("name")
	mw, ok := h.manager.GetMiddleware(name)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("middleware %s is not loaded", name)})
	}
	resp := map[string]interface{}{"name": mw.Name(), "version": mw.Version()}
	if tester := h.manager.GetTester(); tester != nil {
		if hs := tester.GetHealthStatus(name); hs != nil {
			resp["status"] = hs.Status
			resp["lastCheck"] = hs.LastCheck
			resp["responseTime"] = hs.ResponseTime.String()
			resp["errorRate"] = hs.ErrorRate
			resp["message"] = hs.Message
		}
		if m := tester.GetMetrics(name); m != nil {
			resp["metrics"] = m.GetStats()
		}
	}
	return c.JSON(http.StatusOK, resp)
}
