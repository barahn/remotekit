// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package service

import (
	"reflect"
	"testing"
)

func TestBuildServiceArgs(t *testing.T) {
	tests := []struct {
		name               string
		serverURL          string
		configPath         string
		insecureSkipVerify bool
		expected           []string
	}{
		{
			name:               "Default local server",
			serverURL:          "http://localhost:8443",
			configPath:         "/etc/barahn/agent.pem",
			insecureSkipVerify: false,
			expected:           []string{"agent", "start", "--no-tray", "--silent", "--config", "/etc/barahn/agent.pem"},
		},
		{
			name:               "Remote server with insecure flag",
			serverURL:          "https://remote.server.internal:8443",
			configPath:         "C:\\ProgramData\\Barahn\\agent.pem",
			insecureSkipVerify: true,
			expected: []string{
				"agent", "start", "--no-tray", "--silent",
				"--server", "https://remote.server.internal:8443",
				"--config", "C:\\ProgramData\\Barahn\\agent.pem",
				"--insecure-skip-verify",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildServiceArgs(tt.serverURL, tt.configPath, tt.insecureSkipVerify)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("BuildServiceArgs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseArgsString(t *testing.T) {
	args := []string{"agent", "start", "--config", "C:\\Program Files\\Barahn\\agent.pem", "--insecure"}
	expected := `agent start --config "C:\Program Files\Barahn\agent.pem" --insecure`

	parsed := ParseArgsString(args)
	if parsed != expected {
		t.Errorf("ParseArgsString() = %s, want %s", parsed, expected)
	}
}

func TestStatusFormatting(t *testing.T) {
	s1 := Status{Installed: false}
	if s1.String() != "Not Installed" {
		t.Errorf("Expected 'Not Installed', got '%s'", s1.String())
	}

	s2 := Status{Installed: true, State: StateRunning, PID: 1234}
	if s2.String() != "Running (PID: 1234)" {
		t.Errorf("Expected 'Running (PID: 1234)', got '%s'", s2.String())
	}

	s3 := Status{Installed: true, State: StateStopped}
	if s3.String() != "Stopped" {
		t.Errorf("Expected 'Stopped', got '%s'", s3.String())
	}
}

func TestNewServiceManager(t *testing.T) {
	mgr := NewServiceManager("test-service")
	if mgr == nil {
		t.Fatal("Expected NewServiceManager to return non-nil manager")
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantError bool
	}{
		{
			name: "Valid standard config",
			cfg: Config{
				Name:        "barahn-agent",
				DisplayName: "Barahn Remote Agent",
				Description: "Barahn Agent Daemon",
				ExecPath:    "/usr/local/bin/barahn-agent",
				WorkDir:     "/etc/barahn",
				Args:        []string{"start", "--config", "/etc/barahn/agent.pem"},
			},
			wantError: false,
		},
		{
			name: "Injection in Description via newline",
			cfg: Config{
				Name:        "barahn-agent",
				Description: "Barahn Agent\nExecStartPre=/usr/bin/evil",
				ExecPath:    "/usr/local/bin/barahn-agent",
			},
			wantError: true,
		},
		{
			name: "Injection in ExecPath via carriage return newline",
			cfg: Config{
				Name:     "barahn-agent",
				ExecPath: "/usr/local/bin/agent\r\nExecStartPost=/usr/bin/evil",
			},
			wantError: true,
		},
		{
			name: "Injection in WorkDir via newline",
			cfg: Config{
				Name:     "barahn-agent",
				WorkDir:  "/etc/barahn\nRestart=no",
				ExecPath: "/usr/local/bin/barahn-agent",
			},
			wantError: true,
		},
		{
			name: "Injection in Args via newline",
			cfg: Config{
				Name:     "barahn-agent",
				ExecPath: "/usr/local/bin/barahn-agent",
				Args:     []string{"start", "--opt\nExecStartPre=/tmp/malicious"},
			},
			wantError: true,
		},
		{
			name: "Injection in Name with path traversal",
			cfg: Config{
				Name:     "../../etc/cron.d/evil",
				ExecPath: "/usr/local/bin/barahn-agent",
			},
			wantError: true,
		},
		{
			name: "Null byte injection in Description",
			cfg: Config{
				Name:        "barahn-agent",
				Description: "Agent\x00Injection",
				ExecPath:    "/usr/local/bin/barahn-agent",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.cfg)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateConfig() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

