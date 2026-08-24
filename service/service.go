package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrServiceNotInstalled is returned when a service action is called on a non-existent service.
	ErrServiceNotInstalled = errors.New("service is not installed")
	// ErrServiceAlreadyInstalled is returned when attempting to install an already registered service.
	ErrServiceAlreadyInstalled = errors.New("service is already installed")
	// ErrUnsupportedPlatform is returned on platforms where background service management is not implemented.
	ErrUnsupportedPlatform = errors.New("service management is not supported on this platform")
)

// State represents the runtime state of a background service.
type State string

const (
	StateUnknown State = "Unknown"
	StateStopped State = "Stopped"
	StateRunning State = "Running"
	StatePaused  State = "Paused"
)

// Status contains the current runtime information of a service.
type Status struct {
	Installed   bool
	State       State
	PID         int
	Description string
}

func (s Status) String() string {
	if !s.Installed {
		return "Not Installed"
	}
	if s.PID > 0 {
		return fmt.Sprintf("%s (PID: %d)", s.State, s.PID)
	}
	return string(s.State)
}

// Config defines the parameters required to install and run an OS service.
type Config struct {
	Name        string
	DisplayName string
	Description string
	ExecPath    string
	Args        []string
	AutoStart   bool
	WorkDir     string
}

// ServiceManager defines the lifecycle interface for managing OS-level background services.
type ServiceManager interface {
	// Install registers the service with the operating system init system (SCM, systemd, launchd).
	Install(cfg Config) error
	// Uninstall removes the registered service from the operating system.
	Uninstall() error
	// Start starts the registered background service.
	Start() error
	// Stop gracefully stops the running background service.
	Stop() error
	// Status queries the current service status.
	Status() (Status, error)
	// Run runs the service daemon loop, blocking until the service is stopped by the OS.
	Run(ctx context.Context, runner func(ctx context.Context) error) error
}

// BuildServiceArgs constructs the standardized command-line arguments for the agent daemon service.
func BuildServiceArgs(serverURL, configPath string, insecureSkipVerify bool) []string {
	args := []string{"agent", "start", "--no-tray", "--silent"}
	if serverURL != "" && serverURL != "http://localhost:8443" && serverURL != "https://localhost:8443" {
		args = append(args, "--server", serverURL)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	if insecureSkipVerify {
		args = append(args, "--insecure-skip-verify")
	}
	return args
}

// ParseArgsString formats an arguments slice into a single command line string safely.
func ParseArgsString(args []string) string {
	var quoted []string
	for _, arg := range args {
		if strings.Contains(arg, " ") {
			quoted = append(quoted, fmt.Sprintf(`"%s"`, arg))
		} else {
			quoted = append(quoted, arg)
		}
	}
	return strings.Join(quoted, " ")
}
