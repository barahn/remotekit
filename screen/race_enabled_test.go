// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build race

package screen

// raceDetectorEnabled reports whether the binary was built with -race.
// See requireNoUpstreamRace in capture_test.go.
const raceDetectorEnabled = true
