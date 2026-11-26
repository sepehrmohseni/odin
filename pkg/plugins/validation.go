package plugins

import (
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// PluginValidator checks an uploaded plugin binary without running any of its code.
//
// Opening a Go plugin (plugin.Open, i.e. dlopen) runs its ELF constructors and Go init
// functions inside the gateway, so validation must not do it: an earlier version opened the
// file at upload time, before its own security checks, which let an upload execute code.
// Everything here reads the file as data. Code runs only when an administrator enables the
// plugin, which is the point where it is trusted.
type PluginValidator struct {
	requiredSymbols []string
	anyOf           bool // one of requiredSymbols is enough
}

// NewPluginValidator validates middleware plugins, which export `func New` or
// `var Middleware`.
func NewPluginValidator() *PluginValidator {
	return &PluginValidator{
		requiredSymbols: []string{"New", "Middleware"},
		anyOf:           true,
	}
}

// NewValidatorRequiring validates plugins that must export the given symbols.
func NewValidatorRequiring(symbols ...string) *PluginValidator {
	return &PluginValidator{requiredSymbols: symbols}
}

// NewHookPluginValidator validates hook plugins, which export `var Plugin`.
func NewHookPluginValidator() *PluginValidator {
	return &PluginValidator{
		requiredSymbols: []string{"Plugin"},
	}
}

// pluginNamePattern restricts plugin names and versions to safe file-name characters, so they
// can be used in paths without escaping the upload directory.
var pluginNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ValidatePluginName reports whether s is usable as a plugin name or version.
func ValidatePluginName(s string) error {
	if !pluginNamePattern.MatchString(s) || strings.Contains(s, "..") {
		return fmt.Errorf("invalid name %q: use 1-64 letters, digits, '.', '_', '+' or '-'", s)
	}
	return nil
}

// ValidatePlugin checks that pluginPath is a Go plugin for this platform, built with this Go
// release, that exports the required symbols.
func (pv *PluginValidator) ValidatePlugin(pluginPath, expectedName string) error {
	if err := pv.validateFileExists(pluginPath); err != nil {
		return err
	}
	if err := pv.validateELF(pluginPath); err != nil {
		return fmt.Errorf("not a loadable plugin: %w", err)
	}
	mainPath, err := pv.validateBuildInfo(pluginPath)
	if err != nil {
		return err
	}
	if err := pv.validateSymbols(pluginPath, mainPath); err != nil {
		return fmt.Errorf("missing required symbols: %w", err)
	}
	return nil
}

// validateFileExists checks if file exists and is not empty
func (pv *PluginValidator) validateFileExists(filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("cannot access plugin file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("plugin is not a regular file")
	}
	if info.Size() == 0 {
		return fmt.Errorf("plugin file is empty")
	}
	return nil
}

// validateELF checks for a shared object (not an executable) for this machine.
func (pv *PluginValidator) validateELF(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not an ELF file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if f.Type != elf.ET_DYN {
		return fmt.Errorf("ELF type is %s, want a shared object (ET_DYN)", f.Type)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("file is an executable, not a shared object")
		}
	}
	if want, ok := elfMachines[runtime.GOARCH]; ok && f.Machine != want {
		return fmt.Errorf("built for %s, but the gateway runs on %s", f.Machine, runtime.GOARCH)
	}
	return nil
}

var elfMachines = map[string]elf.Machine{
	"amd64":   elf.EM_X86_64,
	"arm64":   elf.EM_AARCH64,
	"386":     elf.EM_386,
	"arm":     elf.EM_ARM,
	"riscv64": elf.EM_RISCV,
	"ppc64le": elf.EM_PPC64,
	"s390x":   elf.EM_S390,
}

// validateBuildInfo checks the Go build information embedded in the binary and returns the
// plugin's main package path. Go plugins only load into a process built with the same Go
// release, so a mismatch is rejected here rather than failing later.
func (pv *PluginValidator) validateBuildInfo(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no Go build information (not built with `go build -buildmode=plugin`?): %w", err)
	}

	if extractMajorMinor(info.GoVersion) != extractMajorMinor(runtime.Version()) {
		return "", fmt.Errorf("go version mismatch: plugin built with %s, but the gateway runs %s (they must match)",
			info.GoVersion, runtime.Version())
	}

	settings := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if mode := settings["-buildmode"]; mode != "plugin" {
		return "", fmt.Errorf("built with -buildmode=%q, want plugin", mode)
	}
	if goos := settings["GOOS"]; goos != "" && goos != runtime.GOOS {
		return "", fmt.Errorf("built for GOOS=%s, but the gateway runs on %s", goos, runtime.GOOS)
	}
	if goarch := settings["GOARCH"]; goarch != "" && goarch != runtime.GOARCH {
		return "", fmt.Errorf("built for GOARCH=%s, but the gateway runs on %s", goarch, runtime.GOARCH)
	}
	return info.Path, nil
}

// validateSymbols looks for the required exported symbols in the dynamic symbol table. A
// plugin's own symbols are named "<main package path>.<Name>", or "plugin/unnamed-<hash>.<Name>"
// when it was built from loose files.
func (pv *PluginValidator) validateSymbols(path, mainPath string) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	syms, err := f.DynamicSymbols()
	if err != nil {
		return fmt.Errorf("cannot read dynamic symbols: %w", err)
	}

	found := make(map[string]bool)
	for _, s := range syms {
		if t := elf.ST_TYPE(s.Info); t != elf.STT_FUNC && t != elf.STT_OBJECT {
			continue
		}
		dot := strings.LastIndex(s.Name, ".")
		if dot < 0 {
			continue
		}
		pkg, name := s.Name[:dot], s.Name[dot+1:]
		if pkg == mainPath || strings.HasPrefix(pkg, "plugin/unnamed-") {
			found[name] = true
		}
	}

	var missing []string
	for _, name := range pv.requiredSymbols {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	if pv.anyOf && len(missing) < len(pv.requiredSymbols) {
		return nil
	}
	if len(missing) > 0 {
		sep := ", "
		if pv.anyOf {
			sep = " or "
		}
		return fmt.Errorf("plugin does not export %s", strings.Join(missing, sep))
	}
	return nil
}

// extractMajorMinor extracts major.minor version from Go version string
// e.g., "go1.25.3" -> "go1.25"
func extractMajorMinor(version string) string {
	version = strings.TrimPrefix(version, "go")
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return "go" + parts[0] + "." + parts[1]
	}
	return version
}

// ExtractPluginMetadata extracts metadata from plugin binary
func ExtractPluginMetadata(pluginPath string) (goVersion, goOS, goArch string, err error) {
	info, err := buildinfo.ReadFile(pluginPath)
	if err != nil {
		// Return defaults if we can't read build info
		return runtime.Version(), runtime.GOOS, runtime.GOARCH, nil
	}

	goVersion = info.GoVersion

	// Extract GOOS and GOARCH from build settings
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			goOS = setting.Value
		case "GOARCH":
			goArch = setting.Value
		}
	}

	// Use runtime values as fallback
	if goOS == "" {
		goOS = runtime.GOOS
	}
	if goArch == "" {
		goArch = runtime.GOARCH
	}

	return goVersion, goOS, goArch, nil
}
