package heartbeat

import (
	"context"
	"sync"
	"time"
)

const (
	DefaultInterval      = 10 * time.Second
	DefaultMissThreshold = 3
)

// ChirpMessage represents a periodic heartbeat frame sent from an agent.
type ChirpMessage struct {
	AgentID   string    `json:"agent_id"`
	Timestamp time.Time `json:"timestamp"`
}

// ChirpTicker manages sending period keepalive signals with exponential backoff on failure.
type ChirpTicker struct {
	interval    time.Duration
	maxInterval time.Duration
	sendFunc    func(ctx context.Context) error
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

func NewChirpTicker(interval time.Duration, sendFunc func(ctx context.Context) error) *ChirpTicker {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &ChirpTicker{
		interval:    interval,
		maxInterval: 2 * time.Minute,
		sendFunc:    sendFunc,
		stopChan:    make(chan struct{}),
	}
}

func (ct *ChirpTicker) Start(ctx context.Context) {
	ct.wg.Add(1)
	go func() {
		defer ct.wg.Done()

		currentInterval := ct.interval
		ticker := time.NewTicker(currentInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ct.stopChan:
				return
			case <-ticker.C:
				err := ct.sendFunc(ctx)
				if err != nil {
					// Exponential backoff
					currentInterval = currentInterval * 2
					if currentInterval > ct.maxInterval {
						currentInterval = ct.maxInterval
					}
					ticker.Reset(currentInterval)
				} else if currentInterval != ct.interval {
					// Reset interval on success
					currentInterval = ct.interval
					ticker.Reset(currentInterval)
				}
			}
		}
	}()
}

func (ct *ChirpTicker) Stop() {
	close(ct.stopChan)
	ct.wg.Wait()
}

// ServerMonitor checks for offline agents based on missed chirps.
type ServerMonitor struct {
	checkInterval time.Duration
	missThreshold int
	offlineFunc   func(ctx context.Context, agentID string) error
}

func NewServerMonitor(missThreshold int, offlineFunc func(ctx context.Context, agentID string) error) *ServerMonitor {
	if missThreshold <= 0 {
		missThreshold = DefaultMissThreshold
	}
	return &ServerMonitor{
		checkInterval: DefaultInterval,
		missThreshold: missThreshold,
		offlineFunc:   offlineFunc,
	}
}

// IsOffline checks if a last chirp timestamp exceeds the allowed window.
func (sm *ServerMonitor) IsOffline(lastChirp *time.Time) bool {
	if lastChirp == nil {
		return true
	}
	maxAllowedSilence := time.Duration(sm.missThreshold) * sm.checkInterval
	return time.Since(*lastChirp) > maxAllowedSilence
}
