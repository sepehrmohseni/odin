package config

import (
	"os"
	"testing"

	"odin/pkg/config"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("ODIN_TEST_SECRET", "s3cr3t")
	t.Setenv("ODIN_TEST_EMPTY", "")

	in := "# Comments such as ${NAME} are left alone\n" +
		"  # ${ALSO_NOT_A_REFERENCE}\n" +
		"a: ${ODIN_TEST_SECRET}\n" +
		"b: ${ODIN_TEST_UNSET:-fallback}\n" +
		"c: ${ODIN_TEST_EMPTY:-used-when-empty}\n" +
		"d: $2a$10$hash.with.dollars\n" +
		"e: $${NOT_EXPANDED}\n"
	want := "# Comments such as ${NAME} are left alone\n" +
		"  # ${ALSO_NOT_A_REFERENCE}\n" +
		"a: s3cr3t\n" +
		"b: fallback\n" +
		"c: used-when-empty\n" +
		"d: $2a$10$hash.with.dollars\n" +
		"e: ${NOT_EXPANDED}\n"

	out, err := config.ExpandEnv([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, want, string(out))

	_, err = config.ExpandEnv([]byte("x: ${ODIN_TEST_DEFINITELY_UNSET}"))
	assert.ErrorContains(t, err, "ODIN_TEST_DEFINITELY_UNSET", "a missing secret is a startup error")
}

func TestLoadExpandsEnvironment(t *testing.T) {
	t.Setenv("ODIN_TEST_PORT", "9191")
	tmp, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = tmp.WriteString("server:\n  port: ${ODIN_TEST_PORT}\nauth:\n  jwtSecret: ${ODIN_TEST_JWT:-dev-only}\n")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())

	cfg, err := config.Load(tmp.Name(), logrus.New())
	require.NoError(t, err)
	assert.Equal(t, 9191, cfg.Server.Port)
	assert.Equal(t, "dev-only", cfg.Auth.JWTSecret)
}
