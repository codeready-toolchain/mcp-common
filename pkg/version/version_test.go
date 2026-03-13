package version_test

import (
	"testing"

	"github.com/codeready-toolchain/mcp-common/pkg/version"

	"github.com/stretchr/testify/assert"
)

func TestDefaultVersionValues(t *testing.T) {
	t.Run("Commit has default value", func(t *testing.T) {
		// given/when
		commit := version.Commit

		// then
		assert.Equal(t, "unknown", commit)
	})

	t.Run("BuildTime has default value", func(t *testing.T) {
		// given/when
		buildTime := version.BuildTime

		// then
		assert.Equal(t, "unknown", buildTime)
	})
}

func TestVersionValuesCanBeOverridden(t *testing.T) {
	// given
	originalCommit := version.Commit
	originalBuildTime := version.BuildTime
	defer func() {
		version.Commit = originalCommit
		version.BuildTime = originalBuildTime
	}()

	// when
	version.Commit = "abc123"
	version.BuildTime = "2025-01-01T00:00:00Z"

	// then
	assert.Equal(t, "abc123", version.Commit)
	assert.Equal(t, "2025-01-01T00:00:00Z", version.BuildTime)
}
