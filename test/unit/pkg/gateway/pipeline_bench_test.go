package gateway_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"odin/pkg/config"

	"github.com/labstack/echo/v4"
)

// BenchmarkPluginPipeline measures a proxied GET through the whole gateway with no middleware
// plugins, and with plugins in each phase (each just calls next). The difference is the cost of
// the pipeline itself.
//
//	scripts/docker-check.sh -- go test -run x -bench PluginPipeline -benchmem ./test/unit/pkg/gateway/
func BenchmarkPluginPipeline(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	for _, n := range []int{0, 3, 9} {
		b.Run(fmt.Sprintf("plugins=%d", n), func(b *testing.B) {
			cfg := &config.Config{Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{backend.URL}}}}
			cfg.Server.Port = 8080
			cfg.Services[0].SetDefaults()
			gw, err := gatewayNew(b, cfg)
			if err != nil {
				b.Fatal(err)
			}
			phases := []string{"pre-auth", "post-auth", "pre-proxy"}
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("p%d", i)
				pass := mw(name, func(next echo.HandlerFunc) echo.HandlerFunc { return next })
				if err := gw.Plugins().RegisterMiddleware(name, pass, i, []string{"/api/*"}, phases[i%3]); err != nil {
					b.Fatal(err)
				}
			}
			h := gw.Handler()
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatalf("status %d", rec.Code)
				}
			}
		})
	}
}
