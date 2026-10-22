// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package service

import (
	"errors"
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

func TestCheckNoControlChars(t *testing.T) {
	valid := []string{
		"barahn-agent",
		"Barahn Remote Access Agent",
		"/usr/local/bin/agent",
		"--config=/etc/barahn/config.json",
		"normal text with spaces and 123",
	}
	for _, s := range valid {
		if err := CheckNoControlChars(s); err != nil {
			t.Errorf("CheckNoControlChars(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"agent\nExecStartPre=/usr/bin/evil",
		"service\r\nmalicious",
		"arg\x00extra",
		"multi\nline\ndescription",
	}
	for _, s := range invalid {
		if err := CheckNoControlChars(s); !errors.Is(err, ErrInvalidParameter) {
			t.Errorf("CheckNoControlChars(%q) = %v, want ErrInvalidParameter", s, err)
		}
	}
}

func TestInstallRejectsControlCharacters(t *testing.T) {
	mgr := NewServiceManager("test\ninvalid")
	cfg := Config{
		Description: "Normal description",
		Args:        []string{"agent", "start"},
	}
	err := mgr.Install(cfg)
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("Expected ErrInvalidParameter for invalid service name, got: %v", err)
	}

	mgrValid := NewServiceManager("valid-name")
	cfgInvalidDesc := Config{
		Description: "Malicious\nDescription",
		Args:        []string{"agent", "start"},
	}
	err = mgrValid.Install(cfgInvalidDesc)
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("Expected ErrInvalidParameter for invalid description, got: %v", err)
	}

	cfgInvalidArg := Config{
		Description: "Valid description",
		Args:        []string{"agent", "start\nExecStartPre=/bin/sh"},
	}
	err = mgrValid.Install(cfgInvalidArg)
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("Expected ErrInvalidParameter for invalid arg, got: %v", err)
	}
}
