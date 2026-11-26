// Package wasm runs WebAssembly middleware plugins in the request pipeline.
//
// A plugin is a WebAssembly module (any language that compiles to wasm32, for example Go with
// GOOS=wasip1 and -buildmode=c-shared, Rust, or TinyGo) that exports:
//
//	memory
//	odin_alloc(size i32) i32              returns where the host may write size bytes of input
//	odin_handle(ptr i32, size i32) i64    handles the JSON input at ptr; returns the JSON output's
//	                                      address in the high 32 bits and its length in the low 32
//
// The input and output are described by Input and Output. A plugin runs in a sandbox: no
// file system, no network, no environment; it may import env.log(ptr, len i32) to write to the
// gateway's log, and WASI for its language runtime (clocks, random numbers).
package wasm

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Defaults for Options.
const (
	DefaultMaxMemoryPages = 512 // 32 MiB per instance
	DefaultMaxInstances   = 4
	DefaultTimeout        = 100 * time.Millisecond
)

// Options configure a Runtime.
type Options struct {
	// Directory that relative plugin paths are resolved against.
	PluginDir string
	// Memory limit of each plugin instance, in 64 KiB pages.
	MaxMemoryPages int
	// Instances per plugin, which is how many requests one plugin handles at a time.
	MaxInstances int
}

// Runtime compiles and runs plugins. Plugins compiled by one Runtime share its host functions
// and limits; Close releases all of them.
type Runtime struct {
	opts   Options
	logger *logrus.Logger
	r      wazero.Runtime
}

type pluginNameKey struct{}

// NewRuntime creates a runtime. Function calls are interrupted when their context ends, so a
// plugin timeout is enforced even inside an endless loop. (The previous runtime did not set
// this, so timeouts were not enforced: AUDIT SEC-28.)
func NewRuntime(opts Options, logger *logrus.Logger) (*Runtime, error) {
	if opts.MaxMemoryPages <= 0 {
		opts.MaxMemoryPages = DefaultMaxMemoryPages
	}
	if opts.MaxMemoryPages > 65536 {
		opts.MaxMemoryPages = 65536
	}
	if opts.MaxInstances <= 0 {
		opts.MaxInstances = DefaultMaxInstances
	}
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(uint32(opts.MaxMemoryPages))) //nolint:gosec // bounded above

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("instantiating WASI: %w", err)
	}
	rt := &Runtime{opts: opts, logger: logger, r: r}
	_, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, ptr, size uint32) {
			msg, ok := m.Memory().Read(ptr, size)
			if !ok {
				return
			}
			name, _ := ctx.Value(pluginNameKey{}).(string)
			rt.logger.WithField("wasm_plugin", name).Info(string(msg))
		}).
		Export("log").
		Instantiate(ctx)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("instantiating host functions: %w", err)
	}
	return rt, nil
}

// Load compiles the plugin at cfg.Path and checks its exports. Its instances are created on
// demand, up to MaxInstances.
func (rt *Runtime) Load(cfg PluginConfig) (*Plugin, error) {
	path := cfg.Path
	if !filepath.IsAbs(path) && rt.opts.PluginDir != "" {
		path = filepath.Join(rt.opts.PluginDir, path)
	}
	code, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %s: %w", cfg.Name, err)
	}
	ctx := context.Background()
	compiled, err := rt.r.CompileModule(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("wasm plugin %s: compiling: %w", cfg.Name, err)
	}
	exports := compiled.ExportedFunctions()
	for _, name := range []string{"odin_alloc", "odin_handle"} {
		if _, ok := exports[name]; !ok {
			_ = compiled.Close(ctx)
			return nil, fmt.Errorf("wasm plugin %s: does not export %s (see the plugin ABI in docs/plugins.md)", cfg.Name, name)
		}
	}
	if _, ok := compiled.ExportedMemories()["memory"]; !ok {
		_ = compiled.Close(ctx)
		return nil, fmt.Errorf("wasm plugin %s: does not export its memory", cfg.Name)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	p := &Plugin{
		cfg:      cfg,
		rt:       rt,
		compiled: compiled,
		idle:     make(chan api.Module, rt.opts.MaxInstances),
		slots:    make(chan struct{}, rt.opts.MaxInstances),
	}
	// Instantiate one now, so a module that fails to start is reported at startup.
	m, err := p.instantiate()
	if err != nil {
		_ = compiled.Close(ctx)
		return nil, err
	}
	p.idle <- m
	return p, nil
}

// Close releases every plugin and the runtime.
func (rt *Runtime) Close() error {
	return rt.r.Close(context.Background())
}

// moduleConfig gives each instance real clocks and randomness (wazero's defaults are fixed, for
// reproducibility) and nothing else: no file system, arguments or environment.
func moduleConfig() wazero.ModuleConfig {
	return wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		WithRandSource(rand.Reader)
}
