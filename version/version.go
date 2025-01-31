package version

import (
	"fmt"
	"runtime/debug"
)

// getOrDefault returns the value for key from map m, or def if key doesn't exist
func getOrDefault(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// Info returns version information from the built binary
func Info() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "No build information available"
	}

	// Initialize a settings map
	settings := make(map[string]string)
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}

	return fmt.Sprintf(
		"Version:    %s\n"+
			"Go Version: %s\n"+
			"Git Commit: %s\n"+
			"Built:      %s\n"+
			"Modified:   %s",
		info.Main.Version,
		info.GoVersion,
		getOrDefault(settings, "vcs.revision", "unknown"),
		getOrDefault(settings, "vcs.time", "unknown"),
		getOrDefault(settings, "vcs.modified", "unknown"),
	)
}

// Version returns just the version string
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}
