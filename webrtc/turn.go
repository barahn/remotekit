package webrtc

import (
	"fmt"
	"net"
	"sync"

	"github.com/pion/turn/v5"
)

// TURNConfig holds settings for the embedded STUN/TURN relay server.
type TURNConfig struct {
	// PublicIP is the public IP or hostname of the Barahn Control Plane server.
	PublicIP string

	// Port is the UDP port for STUN/TURN listening. Default: 3478.
	Port int

	// Realm is the authentication realm (e.g., "barahn.local").
	Realm string

	// Username for TURN authentication.
	Username string

	// Password for TURN authentication.
	Password string
}

// DefaultTURNConfig returns a TURNConfig with sensible local defaults.
func DefaultTURNConfig() TURNConfig {
	return TURNConfig{
		PublicIP: "127.0.0.1",
		Port:     3478,
		Realm:    "barahn",
		Username: "barahn",
		Password: "turnpassword", // #nosec G101 -- default fallback for local dev
	}
}

// TURNServer manages an embedded pion/turn STUN/TURN server instance.
type TURNServer struct {
	config TURNConfig
	server *turn.Server
	mu     sync.Mutex
}

// NewTURNServer creates and starts an embedded STUN/TURN server.
func NewTURNServer(config TURNConfig) (*TURNServer, error) {
	if config.Port <= 0 {
		config.Port = 3478
	}
	if config.Realm == "" {
		config.Realm = "barahn"
	}

	udpListener, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", config.Port))
	if err != nil {
		return nil, fmt.Errorf("webrtc: failed to listen UDP for TURN on port %d: %w", config.Port, err)
	}

	publicIP := config.PublicIP
	if publicIP == "" {
		publicIP = "127.0.0.1"
	}

	// Create user authentication map
	usersMap := map[string][]byte{
		config.Username: turn.GenerateAuthKey(config.Username, config.Realm, config.Password),
	}

	server, err := turn.NewServer(turn.ServerConfig{
		Realm: config.Realm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if key, ok := usersMap[ra.Username]; ok {
				return ra.Username, key, true
			}
			return "", nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: udpListener,
				RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP(publicIP),
					Address:      "0.0.0.0",
				},
			},
		},
	})

	if err != nil {
		_ = udpListener.Close()
		return nil, fmt.Errorf("webrtc: failed to initialize TURN server: %w", err)
	}

	return &TURNServer{
		config: config,
		server: server,
	}, nil
}

// Config returns the active TURNConfig.
func (ts *TURNServer) Config() TURNConfig {
	return ts.config
}

// Close gracefully shuts down the TURN server.
func (ts *TURNServer) Close() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.server != nil {
		err := ts.server.Close()
		ts.server = nil
		return err
	}
	return nil
}
