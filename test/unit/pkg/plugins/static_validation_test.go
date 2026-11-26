package plugins_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"odin/pkg/plugins"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A plugin whose init writes a marker file named by $ODIN_PLUGIN_MARKER. If validation ever
// loaded the plugin, the marker would appear.
const pluginSource = `package main

import "os"

func init() {
	if p := os.Getenv("ODIN_PLUGIN_MARKER"); p != "" {
		_ = os.WriteFile(p, []byte("init ran"), 0o600)
	}
}

func New(cfg map[string]interface{}) (interface{}, error) { return nil, nil }

var Plugin = struct{}{}
`

// buildTestPlugin compiles pluginSource with -buildmode=plugin, or skips when this machine
// cannot build plugins (no go command or no C compiler for cgo).
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not available")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(pluginSource), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/testplugin\n\ngo 1.26\n"), 0o600))

	out := filepath.Join(dir, "testplugin.so")
	cmd := exec.Command(goBin, "build", "-buildmode=plugin", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOFLAGS=-mod=mod")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build Go plugins here: %v\n%s", err, output)
	}
	return out
}

func TestValidatorAcceptsRealPluginWithoutLoadingIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Go plugins are ELF on Linux only")
	}
	path := buildTestPlugin(t)
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("ODIN_PLUGIN_MARKER", marker)

	require.NoError(t, plugins.NewPluginValidator().ValidatePlugin(path, "testplugin"))
	require.NoError(t, plugins.NewHookPluginValidator().ValidatePlugin(path, "testplugin"))

	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "validation must not run the plugin's init")
}

func TestValidatorRejectsNonPlugins(t *testing.T) {
	dir := t.TempDir()

	text := filepath.Join(dir, "text.so")
	require.NoError(t, os.WriteFile(text, []byte("#!/bin/sh\necho hi\n"), 0o600))
	assert.Error(t, plugins.NewPluginValidator().ValidatePlugin(text, "x"))

	fakeELF := filepath.Join(dir, "fake.so")
	require.NoError(t, os.WriteFile(fakeELF, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 2048)...), 0o600))
	assert.Error(t, plugins.NewPluginValidator().ValidatePlugin(fakeELF, "x"),
		"an ELF magic number alone is not a plugin")

	empty := filepath.Join(dir, "empty.so")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	assert.Error(t, plugins.NewPluginValidator().ValidatePlugin(empty, "x"))

	// An executable (this test binary) is not a shared object plugin.
	self, err := os.Executable()
	require.NoError(t, err)
	assert.Error(t, plugins.NewPluginValidator().ValidatePlugin(self, "x"))
}

func TestValidatorRequiresSymbols(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Go plugins are ELF on Linux only")
	}
	path := buildTestPlugin(t)
	v := plugins.NewPluginValidator()
	require.NoError(t, v.ValidatePlugin(path, "testplugin"))
	// The test plugin exports New and Plugin, but nothing called Missing.
	assert.ErrorContains(t, plugins.NewValidatorRequiring("Missing").ValidatePlugin(path, "testplugin"), "Missing")
}

func TestValidatePluginName(t *testing.T) {
	for _, ok := range []string{"auth", "rate-limit", "my_plugin.v2", "1.0.0", "v1.2.3+build"} {
		assert.NoError(t, plugins.ValidatePluginName(ok), ok)
	}
	for _, bad := range []string{"", "..", "../..", "a/b", `a\b`, ".hidden", "a..b", "/abs", "has space", "x<y"} {
		assert.Error(t, plugins.ValidatePluginName(bad), bad)
	}
}

func TestPluginManagerRestrictsLoadPaths(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	pm := plugins.NewPluginManager(logrus.New())
	pm.SetAllowedDirs(allowed)

	assert.NoError(t, pm.CheckPluginPath(filepath.Join(allowed, "p.so")))
	assert.Error(t, pm.CheckPluginPath(filepath.Join(outside, "p.so")))
	assert.Error(t, pm.CheckPluginPath(filepath.Join(allowed, "..", filepath.Base(outside), "p.so")))
	assert.Error(t, pm.CheckPluginPath("/usr/lib/libc.so.6"))

	target := filepath.Join(outside, "evil.so")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	link := filepath.Join(allowed, "link.so")
	require.NoError(t, os.Symlink(target, link))
	assert.Error(t, pm.CheckPluginPath(link), "a symlink out of the allowed directory is rejected")

	err := pm.LoadPlugin("x", filepath.Join(outside, "p.so"), nil, nil)
	assert.ErrorContains(t, err, "outside the allowed plugin directories")
}
