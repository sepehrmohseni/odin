package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// MaxBodyBytes is the most request body a plugin with ReadBody receives.
const MaxBodyBytes = 1 << 20

// PluginConfig describes one plugin to Load.
type PluginConfig struct {
	Name    string
	Version string
	Path    string
	// Handed to the plugin in every Input.
	Config map[string]interface{}
	// Per request; the call is interrupted when it is exceeded (default 100ms).
	Timeout time.Duration
	// Service names the plugin applies to; empty means every service its routes match.
	Services []string
	// Hand the request body (up to MaxBodyBytes) to the plugin.
	ReadBody bool
	// On a plugin error or timeout, continue with the request instead of answering 500.
	FailOpen bool
}

// Input is the JSON the plugin receives.
type Input struct {
	Service string                 `json:"service,omitempty"`
	User    string                 `json:"user,omitempty"`
	Request Request                `json:"request"`
	Config  map[string]interface{} `json:"config,omitempty"`
}

// Request is the client's request as the plugin sees it.
type Request struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query,omitempty"`
	Headers    map[string]string `json:"headers"`
	RemoteAddr string            `json:"remoteAddr"`
	Body       string            `json:"body,omitempty"`
}

// Output is the JSON the plugin returns. Action "respond" answers the client with Status,
// Headers and Body and stops the request; anything else continues it, with SetHeaders and
// RemoveHeaders applied to the request and ResponseHeaders added to the response.
type Output struct {
	Action          string            `json:"action"`
	Status          int               `json:"status,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	SetHeaders      map[string]string `json:"setHeaders,omitempty"`
	RemoveHeaders   []string          `json:"removeHeaders,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
}

// Plugin is a loaded WebAssembly plugin. It implements plugins.Middleware, so it runs in a
// phase of the request pipeline like a Go plugin.
type Plugin struct {
	cfg      PluginConfig
	rt       *Runtime
	compiled wazero.CompiledModule
	idle     chan api.Module // instances ready for a request
	slots    chan struct{}   // one per request being handled
}

// Name returns the plugin's name.
func (p *Plugin) Name() string { return p.cfg.Name }

// Version returns the configured version, or "wasm".
func (p *Plugin) Version() string {
	if p.cfg.Version != "" {
		return p.cfg.Version
	}
	return "wasm"
}

// Initialize is a no-op: the configuration is handed to the plugin with every request.
func (p *Plugin) Initialize(map[string]interface{}) error { return nil }

// Cleanup closes the idle instances and the compiled module.
func (p *Plugin) Cleanup() error {
	ctx := context.Background()
	for {
		select {
		case m := <-p.idle:
			_ = m.Close(ctx)
		default:
			return p.compiled.Close(ctx)
		}
	}
}

// Handle runs the plugin for each request.
func (p *Plugin) Handle(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if len(p.cfg.Services) > 0 {
			svc, _ := c.Get("service_name").(string)
			if !contains(p.cfg.Services, svc) {
				return next(c)
			}
		}
		out, err := p.run(c)
		if err != nil {
			p.rt.logger.WithError(err).WithField("wasm_plugin", p.cfg.Name).Error("WASM plugin failed")
			if p.cfg.FailOpen {
				return next(c)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "Middleware plugin failed")
		}
		if out.Action == "respond" {
			for k, v := range out.Headers {
				c.Response().Header().Set(k, v)
			}
			status := out.Status
			if status < 100 || status > 999 {
				status = http.StatusOK
			}
			ct := c.Response().Header().Get(echo.HeaderContentType)
			if ct == "" {
				ct = "text/plain; charset=utf-8"
			}
			return c.Blob(status, ct, []byte(out.Body))
		}
		for k, v := range out.SetHeaders {
			c.Request().Header.Set(k, v)
		}
		for _, k := range out.RemoveHeaders {
			c.Request().Header.Del(k)
		}
		if len(out.ResponseHeaders) > 0 {
			c.Response().Before(func() {
				for k, v := range out.ResponseHeaders {
					c.Response().Header().Set(k, v)
				}
			})
		}
		return next(c)
	}
}

func (p *Plugin) run(c echo.Context) (*Output, error) {
	in, err := p.input(c)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, errors.New("input too large")
	}

	ctx, cancel := context.WithTimeout(c.Request().Context(), p.cfg.Timeout)
	defer cancel()
	ctx = context.WithValue(ctx, pluginNameKey{}, p.cfg.Name)

	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("all %d instances busy: %w", cap(p.slots), ctx.Err())
	}
	defer func() { <-p.slots }()

	var m api.Module
	select {
	case m = <-p.idle:
	default:
		if m, err = p.instantiate(); err != nil {
			return nil, err
		}
	}

	out, err := call(ctx, m, data)
	if err != nil || m.IsClosed() {
		// A trapped or interrupted instance may be in any state; start the next request on a
		// fresh one.
		_ = m.Close(context.Background())
		return nil, err
	}
	select {
	case p.idle <- m:
	default:
		_ = m.Close(context.Background())
	}
	return out, nil
}

func (p *Plugin) instantiate() (api.Module, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := p.rt.r.InstantiateModule(ctx, p.compiled, moduleConfig())
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %s: starting an instance: %w", p.cfg.Name, err)
	}
	return m, nil
}

func call(ctx context.Context, m api.Module, data []byte) (*Output, error) {
	results, err := m.ExportedFunction("odin_alloc").Call(ctx, uint64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("odin_alloc: %w", err)
	}
	ptr := uint32(results[0]) //nolint:gosec // a wasm32 address
	if len(data) > 0 && !m.Memory().Write(ptr, data) {
		return nil, fmt.Errorf("odin_alloc returned %d, outside the plugin's memory", ptr)
	}
	results, err = m.ExportedFunction("odin_handle").Call(ctx, uint64(ptr), uint64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("odin_handle: %w", err)
	}
	outPtr, outLen := uint32(results[0]>>32), uint32(results[0]) //nolint:gosec // packed wasm32 address and length
	out := &Output{}
	if outLen == 0 {
		return out, nil
	}
	raw, ok := m.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, fmt.Errorf("odin_handle returned %d bytes at %d, outside the plugin's memory", outLen, outPtr)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("odin_handle returned invalid JSON: %w", err)
	}
	return out, nil
}

func (p *Plugin) input(c echo.Context) (*Input, error) {
	r := c.Request()
	headers := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		headers[k] = strings.Join(v, ", ")
	}
	in := &Input{
		Request: Request{
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			Headers:    headers,
			RemoteAddr: c.RealIP(),
		},
		Config: p.cfg.Config,
	}
	in.Service, _ = c.Get("service_name").(string)
	in.User, _ = c.Get("user_id").(string)
	if p.cfg.ReadBody && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("reading the request body: %w", err)
		}
		// The upstream still gets the whole body: what was read, then the rest.
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		if len(body) > MaxBodyBytes {
			body = body[:MaxBodyBytes]
		}
		in.Request.Body = string(body)
	}
	return in, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
