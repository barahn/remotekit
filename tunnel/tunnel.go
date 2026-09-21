// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package tunnel

import (
	"crypto/tls"

	"github.com/hashicorp/yamux"
)

type Config struct {
	ServerAddr  string
	PairingCode string
	CertPath    string
	KeyPath     string
	TLSConfig   *tls.Config
	YamuxConfig *yamux.Config
}

func DefaultYamuxConfig() *yamux.Config {
	conf := yamux.DefaultConfig()
	conf.EnableKeepAlive = true
	return conf
}
