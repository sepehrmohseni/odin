package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"odin/pkg/ai"

	"github.com/labstack/echo/v4"
)

// aiOn is read by the templates (navigation, dashboard panel).
var aiOn atomic.Bool

// SetAnalyzer enables the Anomalies page and the /admin/api/ai endpoints.
func (h *AdminHandler) SetAnalyzer(a *ai.Analyzer) {
	h.analyzer = a
	aiOn.Store(a != nil)
}

func (h *AdminHandler) registerAIRoutes(g *echo.Group) {
	if h.analyzer == nil {
		return
	}
	g.GET("/anomalies", func(c echo.Context) error {
		s := h.analyzer.Settings()
		return h.renderTemplate(c, "anomalies.html", map[string]interface{}{"Settings": s, "Every": everyText(s.Interval)})
	})
	g.GET("/api/ai/anomalies", h.listAnomalies)
	g.POST("/api/ai/anomalies/:id/resolve", func(c echo.Context) error { return h.markAnomaly(c, false) })
	g.POST("/api/ai/anomalies/:id/false-positive", func(c echo.Context) error { return h.markAnomaly(c, true) })
	g.GET("/api/ai/baselines", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{"baselines": h.analyzer.Baselines()})
	})
	g.GET("/api/ai/stats", h.anomalyStats)
}

// listAnomalies: ?service=&type=&severity= (minimum) &open=1 &limit=
func (h *AdminHandler) listAnomalies(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	list := h.analyzer.Anomalies(ai.Filter{
		Service:  c.QueryParam("service"),
		Type:     c.QueryParam("type"),
		Severity: c.QueryParam("severity"),
		Open:     c.QueryParam("open") == "1" || c.QueryParam("open") == "true",
		Limit:    limit,
	})
	if list == nil {
		list = []ai.Anomaly{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"anomalies": list})
}

func (h *AdminHandler) markAnomaly(c echo.Context, falsePositive bool) error {
	if !h.analyzer.Mark(c.Param("id"), falsePositive) {
		return echo.NewHTTPError(http.StatusNotFound, "No such anomaly")
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Updated"})
}

func (h *AdminHandler) anomalyStats(c echo.Context) error {
	all := h.analyzer.Anomalies(ai.Filter{})
	open, bySeverity, byType := 0, map[string]int{}, map[string]int{}
	for _, a := range all {
		if !a.Resolved && !a.FalsePositive {
			open++
			bySeverity[a.Severity]++
		}
		byType[a.Type]++
	}
	learning := 0
	baselines := h.analyzer.Baselines()
	for _, b := range baselines {
		if !b.Ready {
			learning++
		}
	}
	s := h.analyzer.Settings()
	return c.JSON(http.StatusOK, map[string]interface{}{
		"total":           len(all),
		"open":            open,
		"openBySeverity":  bySeverity,
		"byType":          byType,
		"services":        len(baselines),
		"learning":        learning,
		"intervalSeconds": s.Interval.Seconds(),
		"threshold":       s.Threshold,
		"minSamples":      s.MinSamples,
	})
}

// everyText writes a window length as "minute", "5 minutes" or "30s".
func everyText(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "minute"
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}
