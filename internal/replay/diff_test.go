package replay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffJSON_Identical(t *testing.T) {
	a := json.RawMessage(`{"key": "value"}`)
	b := json.RawMessage(`{"key": "value"}`)

	result, err := DiffJSON(a, b)
	require.NoError(t, err)
	assert.Equal(t, 0, result.DiffBytes)
}

func TestDiffJSON_Different(t *testing.T) {
	a := json.RawMessage(`{"key": "old"}`)
	b := json.RawMessage(`{"key": "new"}`)

	result, err := DiffJSON(a, b)
	require.NoError(t, err)
	assert.Greater(t, result.DiffBytes, 0)
	var hasDiff bool
	for _, line := range result.Lines {
		if len(line) > 0 && (line[0] == '-' || line[0] == '+') {
			hasDiff = true
		}
	}
	assert.True(t, hasDiff)
}

func TestDiffJSON_NilBoth(t *testing.T) {
	result, err := DiffJSON(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.DiffBytes)
}

func TestDiffJSON_OneNil(t *testing.T) {
	a := json.RawMessage(`{"key": "value"}`)
	result, err := DiffJSON(a, nil)
	require.NoError(t, err)
	assert.Greater(t, result.DiffBytes, 0)
}

func TestParseArgs_Minimal(t *testing.T) {
	cfg, err := parseArgs([]string{"run-123"})
	require.NoError(t, err)
	assert.Equal(t, "run-123", cfg.RunID)
	assert.Equal(t, "localhost:50051", cfg.Server)
}

func TestParseArgs_WithBreakAt(t *testing.T) {
	cfg, err := parseArgs([]string{"run-123", "--break-at", "step2"})
	require.NoError(t, err)
	assert.Equal(t, "run-123", cfg.RunID)
	assert.Equal(t, "step2", cfg.BreakAt)
}

func TestParseArgs_WithForkFrom(t *testing.T) {
	cfg, err := parseArgs([]string{"run-123", "--fork-from", "step2"})
	require.NoError(t, err)
	assert.Equal(t, "step2", cfg.ForkFrom)
}

func TestParseArgs_WithServer(t *testing.T) {
	cfg, err := parseArgs([]string{"run-123", "--server", "remote:50051"})
	require.NoError(t, err)
	assert.Equal(t, "remote:50051", cfg.Server)
}

func TestParseArgs_AllFlags(t *testing.T) {
	cfg, err := parseArgs([]string{"run-123", "--break-at", "s1", "--fork-from", "s2", "--server", "host:1234"})
	require.NoError(t, err)
	assert.Equal(t, "run-123", cfg.RunID)
	assert.Equal(t, "s1", cfg.BreakAt)
	assert.Equal(t, "s2", cfg.ForkFrom)
	assert.Equal(t, "host:1234", cfg.Server)
}

func TestParseArgs_NoRunID(t *testing.T) {
	_, err := parseArgs([]string{})
	assert.Error(t, err)
}

func TestParseArgs_BreakAtNoValue(t *testing.T) {
	_, err := parseArgs([]string{"run-123", "--break-at"})
	assert.Error(t, err)
}

func TestParseArgs_UnexpectedArg(t *testing.T) {
	_, err := parseArgs([]string{"run-123", "extra"})
	assert.Error(t, err)
}
