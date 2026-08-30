package osinfo

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OSInfo encapsulates detailed operating system, distribution, version, and display server information.
type OSInfo struct {
	OS            string `json:"os"`             // "linux", "windows", "darwin", etc.
	DistroID      string `json:"distro_id"`      // "xubuntu", "ubuntu", "fedora", "debian", "arch", "alpine", etc.
	DistroName    string `json:"distro_name"`    // "Xubuntu", "Ubuntu 24.04.1 LTS", "Fedora Linux 44", "Windows 11 Pro", etc.
	Version       string `json:"version"`        // "24.04", "44", "11", etc.
	DisplayServer string `json:"display_server"` // "x11", "wayland", or ""
	Formatted     string `json:"formatted"`      // e.g. "Xubuntu 24.04 (X11)", "Fedora 44 (Wayland)", "Windows 11 Pro"
	IconKey       string `json:"icon_key"`       // "xubuntu-linux", "fedora", "microsoft-windows", etc.
}

// Detect inspects the local runtime environment to determine OS details, distribution, and display server.
func Detect() OSInfo {
	switch runtime.GOOS {
	case "linux":
		return detectLinux()
	case "windows":
		return detectWindows()
	case "darwin":
		return detectDarwin()
	default:
		return OSInfo{
			OS:         runtime.GOOS,
			DistroID:   runtime.GOOS,
			DistroName: capitalize(runtime.GOOS),
			Formatted:  capitalize(runtime.GOOS),
			IconKey:    "tux",
		}
	}
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// detectLinux parses /etc/os-release, lsb-release, and desktop environment / display server variables.
func detectLinux() OSInfo {
	osReleaseData := readFirstExistingFile("/etc/os-release", "/usr/lib/os-release", "/etc/lsb-release")
	parsed := parseKeyValuePairs(osReleaseData)

	id := strings.ToLower(parsed["ID"])
	idLike := strings.ToLower(parsed["ID_LIKE"])
	name := parsed["NAME"]
	prettyName := parsed["PRETTY_NAME"]
	versionID := parsed["VERSION_ID"]

	// Detect Desktop Session / Environment
	desktopEnv := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP") + " " + os.Getenv("DESKTOP_SESSION") + " " + os.Getenv("GDMSESSION"))

	// Specifically identify Xubuntu when Ubuntu is paired with XFCE
	if id == "xubuntu" || strings.Contains(desktopEnv, "xubuntu") || (strings.Contains(id, "ubuntu") && (strings.Contains(desktopEnv, "xfce") || strings.Contains(desktopEnv, "x-cinnamon"))) {
		id = "xubuntu"
		if prettyName == "" || strings.HasPrefix(prettyName, "Ubuntu") {
			if versionID != "" {
				prettyName = "Xubuntu " + versionID
			} else {
				prettyName = "Xubuntu Linux"
			}
		}
	} else if id == "" {
		if strings.Contains(desktopEnv, "xubuntu") {
			id = "xubuntu"
			prettyName = "Xubuntu Linux"
		} else if strings.Contains(idLike, "ubuntu") {
			id = "ubuntu"
		} else if strings.Contains(idLike, "debian") {
			id = "debian"
		} else if strings.Contains(idLike, "fedora") || strings.Contains(idLike, "rhel") {
			id = "fedora"
		} else if strings.Contains(idLike, "arch") {
			id = "arch"
		} else {
			id = "linux"
		}
	}

	distroName := prettyName
	for _, noise := range []string{
		"(Container Image)",
		"(container image)",
		"Container Image",
		"container image",
		"(Workstation Edition)",
		"(Server Edition)",
	} {
		distroName = strings.TrimSpace(strings.ReplaceAll(distroName, noise, ""))
	}
	if distroName == "" {
		distroName = name
	}
	if distroName == "" {
		distroName = "Linux"
	}

	// Detect Display Server (X11 vs Wayland)
	displayServer := detectLinuxDisplayServer()

	// Map to Homarr Labs dashboard-icons keys
	iconKey := mapLinuxIconKey(id, idLike)

	// Build clean formatted title
	formatted := distroName
	if displayServer != "" {
		formatted = formatted + " (" + strings.ToUpper(displayServer) + ")"
	}

	return OSInfo{
		OS:            "linux",
		DistroID:      id,
		DistroName:    distroName,
		Version:       versionID,
		DisplayServer: displayServer,
		Formatted:     formatted,
		IconKey:       iconKey,
	}
}

// detectLinuxDisplayServer determines if the current session uses Wayland or X11.
func detectLinuxDisplayServer() string {
	sessionType := strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")))
	if sessionType == "wayland" {
		return "wayland"
	}
	if sessionType == "x11" {
		return "x11"
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return "wayland"
	}

	if os.Getenv("DISPLAY") != "" {
		return "x11"
	}

	// Fallback to checking socket paths in runtime dir
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir != "" {
		if _, err := os.Stat(filepath.Clean(filepath.Join(runtimeDir, "wayland-0"))); err == nil { // #nosec G703
			return "wayland"
		}
	}

	// Check if X11 socket exists
	if _, err := os.Stat("/tmp/.X11-unix/X0"); err == nil || os.Getenv("DISPLAY") != "" {
		return "x11"
	}

	return "x11" // Default display server assumption for Linux desktop environments
}

// MapLinuxIconKey maps a distribution ID/family to the official homarr-labs dashboard-icons identifier.
func MapLinuxIconKey(id, idLike string) string {
	return mapLinuxIconKey(id, idLike)
}

func mapLinuxIconKey(id, idLike string) string {
	id = strings.ToLower(id)
	idLike = strings.ToLower(idLike)

	switch {
	case strings.Contains(id, "xubuntu"):
		return "xubuntu-linux"
	case strings.Contains(id, "ubuntu"):
		return "ubuntu-linux"
	case strings.Contains(id, "fedora"):
		return "fedora"
	case strings.Contains(id, "debian"):
		return "debian-linux"
	case strings.Contains(id, "arch"):
		return "arch-linux"
	case strings.Contains(id, "alpine"):
		return "alpine-linux"
	case strings.Contains(id, "mint"):
		return "linux-mint"
	case strings.Contains(id, "kali"):
		return "kali-linux"
	case strings.Contains(id, "suse") || strings.Contains(id, "opensuse"):
		return "opensuse"
	case strings.Contains(id, "rocky"):
		return "rocky-linux"
	case strings.Contains(id, "alma"):
		return "alma-linux"
	case strings.Contains(id, "centos"):
		return "centos"
	case strings.Contains(id, "pop"):
		return "pop-os"
	case strings.Contains(id, "manjaro"):
		return "manjaro"
	case strings.Contains(idLike, "ubuntu"):
		return "ubuntu-linux"
	case strings.Contains(idLike, "debian"):
		return "debian-linux"
	case strings.Contains(idLike, "fedora") || strings.Contains(idLike, "rhel"):
		return "fedora"
	case strings.Contains(idLike, "arch"):
		return "arch-linux"
	default:
		return "tux"
	}
}

// detectWindows detects Windows edition and version.
func detectWindows() OSInfo {
	distroName := "Windows"
	version := ""

	// Attempt registry query via cmd on Windows
	cmd := exec.Command("cmd", "/c", "ver") // #nosec G204
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		output := strings.TrimSpace(out.String())
		if strings.Contains(output, "10.0.22") || strings.Contains(output, "10.0.26") {
			distroName = "Windows 11"
			version = "11"
		} else if strings.Contains(output, "10.0.") {
			distroName = "Windows 10"
			version = "10"
		} else if strings.Contains(output, "6.3.") {
			distroName = "Windows 8.1"
			version = "8.1"
		} else if strings.Contains(output, "6.1.") {
			distroName = "Windows 7"
			version = "7"
		}
	}

	if distroName == "Windows" {
		distroName = "Windows 11" // Modern default
		version = "11"
	}

	return OSInfo{
		OS:            "windows",
		DistroID:      "windows",
		DistroName:    distroName,
		Version:       version,
		DisplayServer: "",
		Formatted:     distroName,
		IconKey:       "microsoft-windows",
	}
}

// detectDarwin detects macOS version.
func detectDarwin() OSInfo {
	distroName := "macOS"
	version := ""

	cmd := exec.Command("sw_vers", "-productVersion")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		version = strings.TrimSpace(out.String())
		if version != "" {
			distroName = "macOS " + version
		}
	}

	return OSInfo{
		OS:            "darwin",
		DistroID:      "macos",
		DistroName:    distroName,
		Version:       version,
		DisplayServer: "",
		Formatted:     distroName,
		IconKey:       "apple",
	}
}

func readFirstExistingFile(paths ...string) string {
	for _, p := range paths {
		data, err := os.ReadFile(p) // #nosec G304
		if err == nil && len(data) > 0 {
			return string(data)
		}
	}
	return ""
}

// ParseOSRelease parses key-value pairs from os-release formatted text.
func ParseOSRelease(content string) map[string]string {
	return parseKeyValuePairs(content)
}

func parseKeyValuePairs(content string) map[string]string {
	result := make(map[string]string)
	if content == "" {
		return result
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			v = strings.Trim(v, `"'`)
			result[k] = v
		}
	}
	return result
}
