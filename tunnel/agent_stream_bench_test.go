package tunnel

import (
	"strings"
	"testing"
)

var benchmarkServerAddrs = []string{
	"http://localhost:8080",
	"https://example.com:443",
	"ws://already-ws.com",
}

func BenchmarkAgentStreamURLReplace(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, addr := range benchmarkServerAddrs {
			wsURL := strings.Replace(addr, "http://", "ws://", 1)
			wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
			_ = wsURL
		}
	}
}

func BenchmarkAgentStreamURLHasPrefix(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, addr := range benchmarkServerAddrs {
			wsURL := addr
			if strings.HasPrefix(wsURL, "https://") {
				wsURL = "wss://" + wsURL[8:]
			} else if strings.HasPrefix(wsURL, "http://") {
				wsURL = "ws://" + wsURL[7:]
			}
			_ = wsURL
		}
	}
}
