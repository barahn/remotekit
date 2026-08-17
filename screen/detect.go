package screen

import (
	"os"
	"strings"
)

// DisplayServer represents the underlying display server environment.
type DisplayServer string

const (
	DisplayServerX11      DisplayServer = "x11"
	DisplayServerWayland  DisplayServer = "wayland"
	DisplayServerHeadless DisplayServer = "headless"
)

// DetectDisplayServer inspects environment variables to determine whether
// the agent is running under X11, Wayland, or in a headless/containerized environment.
func DetectDisplayServer() DisplayServer {
	sessionType := strings.ToLower(os.Getenv("XDG_SESSION_TYPE"))
	switch sessionType {
	case "wayland":
		return DisplayServerWayland
	case "x11":
		return DisplayServerX11
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return DisplayServerWayland
	}

	if os.Getenv("DISPLAY") != "" {
		return DisplayServerX11
	}

	return DisplayServerHeadless
}
