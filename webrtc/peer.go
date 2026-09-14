package webrtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// defaultKeyFrameRequestInterval is the shortest gap between two honoured
// key-frame requests.
//
// Receivers do not send one request and wait. A browser that has lost a frame
// sends PLIs repeatedly until it sees an intra frame, so honouring each one
// would encode a burst of full refreshes -- the most expensive frame there is,
// several times over, exactly when the connection is already struggling. One
// request is served and the rest are absorbed until the next window.
const defaultKeyFrameRequestInterval = 500 * time.Millisecond

// PeerConfig configures a WebRTC PeerConnection session.
type PeerConfig struct {
	// ICEServers lists STUN/TURN server URLs (e.g. "stun:stun.l.google.com:19302" or "turn:127.0.0.1:3478").
	ICEServers []string

	// TURNUsername is optional credential username for TURN.
	TURNUsername string

	// TURNPassword is optional credential password for TURN.
	TURNPassword string
}

// DefaultPeerConfig returns a default PeerConfig using public STUN.
func DefaultPeerConfig() PeerConfig {
	return PeerConfig{
		ICEServers: []string{
			"stun:stun.l.google.com:19302",
			"stun:stun1.l.google.com:19302",
		},
	}
}

// PeerSession manages a Pion WebRTC PeerConnection for screen streaming.
type PeerSession struct {
	config PeerConfig

	pc         *webrtc.PeerConnection
	videoTrack *webrtc.TrackLocalStaticSample
	mu         sync.RWMutex
	closed     bool

	onCandidate func(candidateJSON string)

	// onKeyFrameRequest is invoked when the receiver asks for an intra frame.
	onKeyFrameRequest func()
	// keyFrameRequestInterval and lastKeyFrameRequest coalesce request bursts.
	keyFrameRequestInterval time.Duration
	lastKeyFrameRequest     time.Time
}

// NewPeerSession creates and initializes a new WebRTC PeerSession.
func NewPeerSession(config PeerConfig) (*PeerSession, error) {
	// Build Pion ICE Servers configuration
	var iceServers []webrtc.ICEServer
	for _, url := range config.ICEServers {
		server := webrtc.ICEServer{
			URLs: []string{url},
		}
		if config.TURNUsername != "" {
			server.Username = config.TURNUsername
			server.Credential = config.TURNPassword
		}
		iceServers = append(iceServers, server)
	}

	webrtcCfg := webrtc.Configuration{
		ICEServers: iceServers,
	}

	// Create MediaEngine with VP8 codec support
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("webrtc: failed to register default codecs: %w", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	pc, err := api.NewPeerConnection(webrtcCfg)
	if err != nil {
		return nil, fmt.Errorf("webrtc: failed to create PeerConnection: %w", err)
	}

	session := &PeerSession{
		config: config,
		pc:     pc,
	}

	// Register ICE candidate callback
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		session.mu.RLock()
		cb := session.onCandidate
		session.mu.RUnlock()

		if cb != nil {
			bytes, err := json.Marshal(c.ToJSON())
			if err == nil {
				cb(string(bytes))
			}
		}
	})

	return session, nil
}

// OnICECandidate sets the callback function for local ICE candidate events.
func (ps *PeerSession) OnICECandidate(cb func(candidateJSON string)) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.onCandidate = cb
}

// CreateVideoTrack creates and attaches a VP8 video track for screen sharing.
func (ps *PeerSession) CreateVideoTrack(streamID, trackID string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return errors.New("webrtc: peer session closed")
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		trackID,
		streamID,
	)
	if err != nil {
		return fmt.Errorf("webrtc: failed to create VP8 video track: %w", err)
	}

	sender, err := ps.pc.AddTrack(track)
	if err != nil {
		return fmt.Errorf("webrtc: failed to add track to PeerConnection: %w", err)
	}

	go ps.readRTCP(sender)

	ps.videoTrack = track
	return nil
}

// OnKeyFrameRequest registers a callback invoked when the receiver asks for an
// intra frame -- a browser that has lost data and cannot decode until it gets a
// fresh starting point.
//
// Without this the stream recovers only when the encoder's periodic key frame
// comes round, which at a 300-frame interval is up to ten seconds of a frozen
// or smeared picture. With it, recovery is one frame.
//
// The callback runs on the RTCP reader's goroutine and must not block: encode
// asynchronously, or set a flag the encoder reads. Requests arriving inside
// SetKeyFrameRequestInterval of an honoured one are dropped rather than queued.
func (ps *PeerSession) OnKeyFrameRequest(fn func()) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.onKeyFrameRequest = fn
}

// SetKeyFrameRequestInterval overrides how often key-frame requests are
// honoured. Zero restores the default.
func (ps *PeerSession) SetKeyFrameRequestInterval(d time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.keyFrameRequestInterval = d
}

// readRTCP consumes the receiver's RTCP feedback for as long as the sender
// lives.
//
// Reading it is not optional even when nothing acts on it: the packets are
// what drive the interceptor chain's own bookkeeping, and a sender whose RTCP
// is never read eventually stalls. This used to read and discard; now the
// packets are parsed and the ones that mean "I cannot decode, send me a fresh
// frame" are acted on.
func (ps *PeerSession) readRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		n, _, err := sender.Read(buf)
		if err != nil {
			return // the sender is closed; nothing further will arrive
		}
		packets, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			// A malformed or unrecognised report is not worth tearing the
			// reader down for -- the next one is along in a moment.
			continue
		}
		ps.dispatchRTCP(packets)
	}
}

// dispatchRTCP acts on a batch of received RTCP packets.
//
// Split out from the read loop so the decision -- which packets mean "send a
// key frame", and how bursts are coalesced -- can be tested without a live
// PeerConnection.
func (ps *PeerSession) dispatchRTCP(packets []rtcp.Packet) {
	wantsKeyFrame := false
	for _, pkt := range packets {
		switch pkt.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			wantsKeyFrame = true
		}
	}
	if !wantsKeyFrame {
		return
	}

	ps.mu.Lock()
	fn := ps.onKeyFrameRequest
	interval := ps.keyFrameRequestInterval
	if interval <= 0 {
		interval = defaultKeyFrameRequestInterval
	}
	now := time.Now()
	tooSoon := !ps.lastKeyFrameRequest.IsZero() && now.Sub(ps.lastKeyFrameRequest) < interval
	if !tooSoon {
		ps.lastKeyFrameRequest = now
	}
	ps.mu.Unlock()

	if fn == nil || tooSoon {
		return
	}
	fn()
}

// WriteVideoSample pushes an encoded frame sample (e.g., VP8 frame) to the video track.
func (ps *PeerSession) WriteVideoSample(sampleData []byte, duration time.Duration) error {
	if ps == nil {
		return errors.New("webrtc: peer session is nil")
	}
	ps.mu.RLock()
	track := ps.videoTrack
	closed := ps.closed
	ps.mu.RUnlock()

	if closed {
		return errors.New("webrtc: peer session closed")
	}
	if track == nil {
		return errors.New("webrtc: video track not initialized")
	}

	return track.WriteSample(media.Sample{
		Data:     sampleData,
		Duration: duration,
	})
}

// CreateOffer generates an SDP Offer message.
func (ps *PeerSession) CreateOffer() (string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	offer, err := ps.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("webrtc: failed to create SDP offer: %w", err)
	}

	if err := ps.pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("webrtc: failed to set local description: %w", err)
	}

	return offer.SDP, nil
}

// CreateAnswer processes a remote SDP Offer and returns an SDP Answer.
func (ps *PeerSession) CreateAnswer(offerSDP string) (string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}

	if err := ps.pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("webrtc: failed to set remote description: %w", err)
	}

	answer, err := ps.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("webrtc: failed to create SDP answer: %w", err)
	}

	if err := ps.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("webrtc: failed to set local description: %w", err)
	}

	return answer.SDP, nil
}

// SetRemoteAnswer applies a remote SDP Answer.
func (ps *PeerSession) SetRemoteAnswer(answerSDP string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}

	return ps.pc.SetRemoteDescription(answer)
}

// AddICECandidate adds a remote ICE candidate.
func (ps *PeerSession) AddICECandidate(candidateJSON string) error {
	var candidateInit webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidateInit); err != nil {
		return fmt.Errorf("webrtc: failed to parse ICE candidate JSON: %w", err)
	}

	return ps.pc.AddICECandidate(candidateInit)
}

// ConnectionState returns the current PeerConnection state.
func (ps *PeerSession) ConnectionState() webrtc.PeerConnectionState {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if ps.pc == nil {
		return webrtc.PeerConnectionStateClosed
	}
	return ps.pc.ConnectionState()
}

// Close terminates the PeerConnection.
func (ps *PeerSession) Close() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return nil
	}
	ps.closed = true

	if ps.pc != nil {
		return ps.pc.Close()
	}
	return nil
}
