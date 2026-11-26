package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envReference matches ${NAME} and ${NAME:-default}. A bare $ is left alone, so values such as
// bcrypt hashes and passwords containing $ are not affected.
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// literalDollarBrace stands in for an escaped "$${" while references are expanded.
const literalDollarBrace = "\x00odin-literal-dollar-brace\x00"

// ExpandEnv replaces ${NAME} and ${NAME:-default} in a config file with environment variables,
// so secrets can come from the environment (Docker, Kubernetes Secrets) instead of the file.
// ${NAME} with NAME unset is an error, which catches a missing secret at startup; with a
// default, an empty value also uses the default. Write $${ for a literal "${".
func ExpandEnv(data []byte) ([]byte, error) {
	var missing []string
	lines := strings.SplitAfter(string(data), "\n")
	for i, line := range lines {
		// Comment lines are left as they are: they often explain the ${NAME} syntax.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines[i] = expandLine(line, &missing)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("environment variables not set: %s (use ${NAME:-default} for optional ones)",
			strings.Join(missing, ", "))
	}
	return []byte(strings.Join(lines, "")), nil
}

func expandLine(line string, missing *[]string) string {
	src := strings.ReplaceAll(line, "$${", literalDollarBrace)
	out := envReference.ReplaceAllStringFunc(src, func(ref string) string {
		m := envReference.FindStringSubmatch(ref)
		name, hasDefault, def := m[1], m[2] != "", m[3]
		if v, ok := os.LookupEnv(name); ok && (v != "" || !hasDefault) {
			return v
		}
		if hasDefault {
			return def
		}
		*missing = append(*missing, name)
		return ref
	})
	return strings.ReplaceAll(out, literalDollarBrace, "${")
}
