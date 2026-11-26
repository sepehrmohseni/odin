package plugins

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"plugin"
	"reflect"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// HookType represents the type of plugin hook
type HookType string

const (
	PreRequestHook   HookType = "pre-request"
	PostRequestHook  HookType = "post-request"
	PreResponseHook  HookType = "pre-response"
	PostResponseHook HookType = "post-response"
)

// PluginContext contains request/response context for plugins
type PluginContext struct {
	RequestID   string
	ServiceName string
	Path        string
	Method      string
	Headers     map[string][]string
	Body        []byte
	UserID      string
	Metadata    map[string]interface{}
}

// Plugin interface that all plugins must implement
type Plugin interface {
	// Name returns the plugin name
	Name() string

	// Version returns the plugin version
	Version() string

	// Initialize initializes the plugin with configuration
	Initialize(config map[string]interface{}) error

	// PreRequest is called before forwarding the request
	PreRequest(ctx context.Context, pluginCtx *PluginContext) error

	// PostRequest is called after receiving the response
	PostRequest(ctx context.Context, pluginCtx *PluginContext) error

	// PreResponse is called before sending response to client
	PreResponse(ctx context.Context, pluginCtx *PluginContext) error

	// PostResponse is called after sending response to client
	PostResponse(ctx context.Context, pluginCtx *PluginContext) error

	// Cleanup is called when the plugin is being unloaded
	Cleanup() error
}

// Middleware interface for Traefik-style middleware plugins
type Middleware interface {
	// Name returns the middleware name
	Name() string

	// Version returns the middleware version
	Version() string

	// Initialize initializes the middleware with configuration
	Initialize(config map[string]interface{}) error

	// Handle wraps the next handler in the chain
	Handle(next echo.HandlerFunc) echo.HandlerFunc

	// Cleanup is called when the middleware is being unloaded
	Cleanup() error
}

// MiddlewareChain represents an ordered list of middleware
type MiddlewareChain struct {
	Middlewares []MiddlewareEntry
	mu          sync.RWMutex
}

// MiddlewareEntry represents a middleware with its metadata
type MiddlewareEntry struct {
	Name       string
	Middleware Middleware
	Priority   int
	Routes     []string // Routes this middleware applies to (supports wildcards)
	Phase      string   // Middleware execution phase
}

// PluginManager manages all loaded plugins
type PluginManager struct {
	plugins         map[string]Plugin
	middlewares     map[string]Middleware
	hooks           map[HookType][]Plugin
	middlewareChain *MiddlewareChain
	tester          *MiddlewareTester
	rollback        *MiddlewareRollback
	logger          *logrus.Logger
	mu              sync.RWMutex

	// allowedDirs, when set, are the only directories plugin binaries are loaded from.
	allowedDirs []string
}

// SetAllowedDirs restricts LoadPlugin and LoadMiddleware to binaries inside dirs. Paths stored
// in the database or sent to the admin API could otherwise point at any file on the host.
func (pm *PluginManager) SetAllowedDirs(dirs ...string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.allowedDirs = pm.allowedDirs[:0]
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if abs, err := filepath.Abs(d); err == nil {
			pm.allowedDirs = append(pm.allowedDirs, filepath.Clean(abs))
		}
	}
}

// CheckPluginPath reports whether path may be loaded under the configured allowed directories.
func (pm *PluginManager) CheckPluginPath(path string) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.checkPathLocked(path)
}

func (pm *PluginManager) checkPathLocked(path string) error {
	if len(pm.allowedDirs) == 0 {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid plugin path %q: %w", path, err)
	}
	// Resolve symlinks so a link inside an allowed directory cannot point outside it.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	for _, dir := range pm.allowedDirs {
		if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolvedDir
		}
		if rel, err := filepath.Rel(dir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
			return nil
		}
	}
	return fmt.Errorf("plugin path %q is outside the allowed plugin directories %v", path, pm.allowedDirs)
}

// NewPluginManager creates a new plugin manager
func NewPluginManager(logger *logrus.Logger) *PluginManager {
	pm := &PluginManager{
		plugins:     make(map[string]Plugin),
		middlewares: make(map[string]Middleware),
		hooks: map[HookType][]Plugin{
			PreRequestHook:   {},
			PostRequestHook:  {},
			PreResponseHook:  {},
			PostResponseHook: {},
		},
		middlewareChain: &MiddlewareChain{
			Middlewares: []MiddlewareEntry{},
		},
		logger: logger,
	}

	// Initialize tester with the plugin manager
	pm.tester = NewMiddlewareTester(pm, logger)

	return pm
}

// createPluginWrapper creates a wrapper for plugins that may have interface compatibility issues
func (pm *PluginManager) createPluginWrapper(sym interface{}) Plugin {
	// Handle the common double-pointer issue with Go plugins
	v := reflect.ValueOf(sym)

	// If it's a pointer to a pointer, dereference it
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}

	if !v.IsValid() {
		return nil
	}

	// Get the underlying value
	actualPlugin := v.Interface()

	// Try direct type assertion first
	if plugin, ok := actualPlugin.(Plugin); ok {
		return plugin
	}

	// If that fails, create a reflection-based wrapper
	return pm.createReflectionWrapper(actualPlugin)
}

// createReflectionWrapper creates a wrapper using reflection for interface compatibility
func (pm *PluginManager) createReflectionWrapper(obj interface{}) Plugin {
	v := reflect.ValueOf(obj)
	t := reflect.TypeOf(obj)

	// For struct values, we need to get the pointer type to check methods
	if t.Kind() == reflect.Struct {
		ptrType := reflect.PointerTo(t)
		t = ptrType
	}

	// Check if the object has all required methods
	requiredMethods := []string{"Name", "Version", "Initialize", "PreRequest", "PostRequest", "PreResponse", "PostResponse", "Cleanup"}

	for _, methodName := range requiredMethods {
		if _, found := t.MethodByName(methodName); !found {
			pm.logger.WithFields(logrus.Fields{
				"type":           t.String(),
				"missing_method": methodName,
			}).Debug("Plugin missing required method")
			return nil
		}
	}

	// If we have a struct value but need pointer methods, get the address
	if v.Kind() == reflect.Struct {
		if v.CanAddr() {
			v = v.Addr()
		} else {
			// Create a new addressable copy
			newV := reflect.New(v.Type())
			newV.Elem().Set(v)
			v = newV
		}
	}

	// Create a wrapper that implements Plugin interface
	return &pluginWrapper{
		obj:    obj,
		value:  v,
		logger: pm.logger,
	}
}

// pluginWrapper wraps a plugin object and implements the Plugin interface using reflection
type pluginWrapper struct {
	obj    interface{}
	value  reflect.Value
	logger *logrus.Logger
}

func (w *pluginWrapper) Name() string {
	result := w.value.MethodByName("Name").Call(nil)
	return result[0].String()
}

func (w *pluginWrapper) Version() string {
	result := w.value.MethodByName("Version").Call(nil)
	return result[0].String()
}

func (w *pluginWrapper) Initialize(config map[string]interface{}) error {
	args := []reflect.Value{reflect.ValueOf(config)}
	result := w.value.MethodByName("Initialize").Call(args)
	return resultError(result)
}

func (w *pluginWrapper) PreRequest(ctx context.Context, pluginCtx *PluginContext) error {
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(pluginCtx)}
	result := w.value.MethodByName("PreRequest").Call(args)
	return resultError(result)
}

func (w *pluginWrapper) PostRequest(ctx context.Context, pluginCtx *PluginContext) error {
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(pluginCtx)}
	result := w.value.MethodByName("PostRequest").Call(args)
	return resultError(result)
}

func (w *pluginWrapper) PreResponse(ctx context.Context, pluginCtx *PluginContext) error {
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(pluginCtx)}
	result := w.value.MethodByName("PreResponse").Call(args)
	return resultError(result)
}

func (w *pluginWrapper) PostResponse(ctx context.Context, pluginCtx *PluginContext) error {
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(pluginCtx)}
	result := w.value.MethodByName("PostResponse").Call(args)
	return resultError(result)
}

func (w *pluginWrapper) Cleanup() error {
	result := w.value.MethodByName("Cleanup").Call(nil)
	return resultError(result)
}

// LoadPlugin opens a hook plugin (exporting `var Plugin`), initializes it and registers it for
// the given hooks (pre-request, post-request, pre-response, post-response). Plugin code runs
// outside the manager's lock, so a slow Initialize does not stall requests.
func (pm *PluginManager) LoadPlugin(name, path string, config map[string]interface{}, hooks []string) error {
	if err := pm.CheckPluginPath(path); err != nil {
		return err
	}
	pm.mu.RLock()
	_, exists := pm.plugins[name]
	pm.mu.RUnlock()
	if exists {
		return fmt.Errorf("plugin %s is already loaded", name)
	}
	for _, h := range hooks {
		switch HookType(h) {
		case PreRequestHook, PostRequestHook, PreResponseHook, PostResponseHook:
		default:
			return fmt.Errorf("plugin %s: unknown hook %q", name, h)
		}
	}

	p, err := plugin.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open plugin %s: %w", name, err)
	}
	sym, err := p.Lookup("Plugin")
	if err != nil {
		return fmt.Errorf("plugin %s does not export Plugin symbol: %w", name, err)
	}
	pluginInstance, ok := sym.(Plugin)
	if !ok {
		if ptr, isPtr := sym.(*Plugin); isPtr && ptr != nil && *ptr != nil {
			pluginInstance = *ptr
		} else if wrapper := pm.createPluginWrapper(sym); wrapper != nil {
			pm.logger.WithField("plugin", name).Info("Created compatibility wrapper for plugin")
			pluginInstance = wrapper
		} else {
			return fmt.Errorf("plugin %s does not implement Plugin interface (got type %T)", name, sym)
		}
	}
	if err := safeCall(func() error { return pluginInstance.Initialize(config) }); err != nil {
		return fmt.Errorf("failed to initialize plugin %s: %w", name, err)
	}
	return pm.RegisterPlugin(name, pluginInstance, hooks)
}

// RegisterPlugin adds an initialized hook plugin, for example one compiled into a program that
// embeds the gateway. LoadPlugin uses it after opening a shared object.
func (pm *PluginManager) RegisterPlugin(name string, p Plugin, hooks []string) error {
	for _, h := range hooks {
		switch HookType(h) {
		case PreRequestHook, PostRequestHook, PreResponseHook, PostResponseHook:
		default:
			return fmt.Errorf("plugin %s: unknown hook %q", name, h)
		}
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, exists := pm.plugins[name]; exists {
		return fmt.Errorf("plugin %s is already loaded", name)
	}
	pm.plugins[name] = p
	for _, h := range hooks {
		// Copy-on-write: requests iterate the previous slice while it is replaced.
		list := append(append([]Plugin(nil), pm.hooks[HookType(h)]...), p)
		pm.hooks[HookType(h)] = list
	}

	pm.logger.WithFields(logrus.Fields{
		"plugin": name,
		"hooks":  hooks,
	}).Info("Plugin loaded successfully")
	return nil
}

// UnloadPlugin removes a hook plugin from every hook and calls its Cleanup. (Go cannot unload
// the shared object itself; its code stays mapped until the process exits.)
func (pm *PluginManager) UnloadPlugin(name string) error {
	pm.mu.Lock()
	p, exists := pm.plugins[name]
	if exists {
		delete(pm.plugins, name)
		for hookType, list := range pm.hooks {
			out := make([]Plugin, 0, len(list))
			for _, q := range list {
				if q != p {
					out = append(out, q)
				}
			}
			pm.hooks[hookType] = out
		}
	}
	pm.mu.Unlock()

	if !exists {
		return fmt.Errorf("plugin %s not found", name)
	}
	if err := safeCall(p.Cleanup); err != nil {
		pm.logger.WithError(err).Warnf("Plugin %s cleanup failed", name)
	}
	pm.logger.WithField("plugin", name).Info("Plugin unloaded")
	return nil
}

// ExecuteHook runs one hook of every plugin registered for it, in load order, stopping at the
// first error. An *echo.HTTPError from a plugin is returned unchanged, so a plugin can reject a
// request with its own status (it used to be wrapped and so became a 500).
func (pm *PluginManager) ExecuteHook(hookType HookType, ctx context.Context, pluginCtx *PluginContext) error {
	pm.mu.RLock()
	plugins := pm.hooks[hookType]
	pm.mu.RUnlock()

	for _, p := range plugins {
		err := safeCall(func() error {
			switch hookType {
			case PreRequestHook:
				return p.PreRequest(ctx, pluginCtx)
			case PostRequestHook:
				return p.PostRequest(ctx, pluginCtx)
			case PreResponseHook:
				return p.PreResponse(ctx, pluginCtx)
			case PostResponseHook:
				return p.PostResponse(ctx, pluginCtx)
			}
			return nil
		})
		if err == nil {
			continue
		}
		pm.logger.WithError(err).WithFields(logrus.Fields{
			"plugin": p.Name(),
			"hook":   hookType,
		}).Error("Plugin hook execution failed")
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return he
		}
		return fmt.Errorf("plugin %s hook %s failed: %w", p.Name(), hookType, err)
	}
	return nil
}

// safeCall runs plugin code, turning a panic into an error.
func safeCall(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panicked: %v", r)
		}
	}()
	return fn()
}

// ListPlugins returns a list of loaded plugin names
func (pm *PluginManager) ListPlugins() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	names := make([]string, 0, len(pm.plugins))
	for name := range pm.plugins {
		names = append(names, name)
	}
	return names
}

// GetPlugin returns a plugin by name
func (pm *PluginManager) GetPlugin(name string) (Plugin, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	plugin, exists := pm.plugins[name]
	return plugin, exists
}

// PluginMiddleware runs the hook plugins for every request:
//
//   - pre-request: before routing; an error stops the request (an *echo.HTTPError keeps its
//     status);
//   - post-request and pre-response: once the response status is known, right before the
//     response headers are written;
//   - post-response: after the response has been written.
//
// Plugins can pass data between hooks of the same request through PluginContext.Metadata.
//
// PluginContext carries the method, path and request headers, and once known the service
// name, the authenticated user and the response status (Metadata["status"]). post-request and
// pre-response used to never run.
func (pm *PluginManager) PluginMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			pluginCtx := &PluginContext{
				RequestID: c.Request().Header.Get(echo.HeaderXRequestID),
				Path:      c.Request().URL.Path,
				Method:    c.Request().Method,
				Headers:   c.Request().Header,
				Metadata:  make(map[string]interface{}),
			}
			fill := func() {
				if s, ok := c.Get("service_name").(string); ok {
					pluginCtx.ServiceName = s
				}
				if u, ok := c.Get("user_id").(string); ok {
					pluginCtx.UserID = u
				}
			}

			if err := pm.ExecuteHook(PreRequestHook, ctx, pluginCtx); err != nil {
				return err
			}

			c.Response().Before(func() {
				fill()
				pluginCtx.Metadata["status"] = c.Response().Status
				for _, h := range []HookType{PostRequestHook, PreResponseHook} {
					if err := pm.ExecuteHook(h, ctx, pluginCtx); err != nil {
						pm.logger.WithError(err).Warnf("%s hook failed", h)
					}
				}
			})

			err := next(c)

			fill()
			pluginCtx.Metadata["status"] = c.Response().Status
			if hookErr := pm.ExecuteHook(PostResponseHook, ctx, pluginCtx); hookErr != nil {
				pm.logger.WithError(hookErr).Error("Post-response hook failed")
			}
			return err
		}
	}
}

// LoadMiddleware opens a middleware plugin (exporting New or Middleware, see resolveMiddleware)
// and keeps it under name. RegisterMiddleware (or LoadMiddlewareWithChain) puts it on the
// request path.
func (pm *PluginManager) LoadMiddleware(name, binaryPath string, config map[string]interface{}) error {
	if err := pm.CheckPluginPath(binaryPath); err != nil {
		return err
	}
	pm.mu.RLock()
	_, exists := pm.middlewares[name]
	pm.mu.RUnlock()
	if exists {
		return fmt.Errorf("middleware %s is already loaded", name)
	}

	p, err := plugin.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("failed to open middleware plugin: %w", err)
	}
	var middleware Middleware
	if err := safeCall(func() error {
		var rerr error
		middleware, rerr = resolveMiddleware(p, config)
		return rerr
	}); err != nil {
		return err
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, exists := pm.middlewares[name]; exists {
		return fmt.Errorf("middleware %s is already loaded", name)
	}
	pm.middlewares[name] = middleware
	pm.logger.Infof("Middleware %s (v%s) loaded successfully", middleware.Name(), middleware.Version())
	return nil
}

// UnloadMiddleware takes a middleware off the request path and calls its Cleanup.
func (pm *PluginManager) UnloadMiddleware(name string) error {
	pm.mu.RLock()
	_, exists := pm.middlewares[name]
	pm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("middleware %s is not loaded", name)
	}
	return pm.UnregisterMiddleware(name)
}

// GetMiddleware returns a middleware by name
func (pm *PluginManager) GetMiddleware(name string) (Middleware, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	middleware, exists := pm.middlewares[name]
	return middleware, exists
}

// ListMiddlewares returns a list of loaded middleware names
func (pm *PluginManager) ListMiddlewares() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	names := make([]string, 0, len(pm.middlewares))
	for name := range pm.middlewares {
		names = append(names, name)
	}
	return names
}

// GetMiddlewareHandler returns an Echo middleware handler for a loaded middleware plugin
func (pm *PluginManager) GetMiddlewareHandler(name string) (echo.MiddlewareFunc, error) {
	middleware, exists := pm.GetMiddleware(name)
	if !exists {
		return nil, fmt.Errorf("middleware %s not found", name)
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return middleware.Handle(next)
	}, nil
}

// ApplyMiddlewares applies all loaded middlewares to an Echo group or router
func (pm *PluginManager) ApplyMiddlewares(e *echo.Echo, routes []string) error {
	pm.mu.RLock()
	middlewareNames := make([]string, 0, len(pm.middlewares))
	for name := range pm.middlewares {
		middlewareNames = append(middlewareNames, name)
	}
	pm.mu.RUnlock()

	for _, name := range middlewareNames {
		middleware, exists := pm.GetMiddleware(name)
		if !exists {
			continue
		}

		// If specific routes are provided, apply only to those routes
		if len(routes) > 0 {
			for _, route := range routes {
				group := e.Group(route)
				group.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
					return middleware.Handle(next)
				})
			}
		} else {
			// Apply globally
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return middleware.Handle(next)
			})
		}

		pm.logger.Infof("Applied middleware %s to routes: %v", name, routes)
	}

	return nil
}

// GetTester returns the middleware tester
func (pm *PluginManager) GetTester() *MiddlewareTester {
	return pm.tester
}

// SetRollback sets the rollback manager (called after repository is available)
func (pm *PluginManager) SetRollback(rollback *MiddlewareRollback) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rollback = rollback
}

// GetRollback returns the rollback manager
func (pm *PluginManager) GetRollback() *MiddlewareRollback {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.rollback
}

// resultError converts the single return value of a reflected `error`-returning call.
func resultError(result []reflect.Value) error {
	if len(result) == 0 || result[0].IsNil() {
		return nil
	}
	if err, ok := result[0].Interface().(error); ok {
		return err
	}
	return fmt.Errorf("plugin method returned %T, want error", result[0].Interface())
}
