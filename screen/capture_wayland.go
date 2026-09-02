//go:build linux

package screen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// portalNegotiationTimeout bounds session setup. It is generous on purpose: the
// xdg-desktop-portal flow shows a consent dialog and blocks until a person
// clicks Share, so anything under a few seconds guarantees a timeout and a
// silent fall back to an empty X11 grab.
const portalNegotiationTimeout = 60 * time.Second

const (
	mutterDest            = "org.gnome.Mutter.ScreenCast"
	mutterPath            = "/org/gnome/Mutter/ScreenCast"
	mutterScreenCast      = "org.gnome.Mutter.ScreenCast"
	mutterSessionIface    = "org.gnome.Mutter.ScreenCast.Session"
	mutterStreamIface     = "org.gnome.Mutter.ScreenCast.Stream"
	portalDest            = "org.freedesktop.portal.Desktop"
	portalPath            = "/org/freedesktop/portal/desktop"
	portalScreenCast      = "org.freedesktop.portal.ScreenCast"
	portalRequestIface    = "org.freedesktop.portal.Request"
	restoreSessionFileRel = ".config/barahn/wayland_screencast_session" // #nosec G101
)

// waylandCapturer implements Capturer on Wayland via XDG Desktop Portal ScreenCast and PipeWire.
type waylandCapturer struct {
	config   CaptureConfig
	bus      *dbus.Conn
	session  dbus.ObjectPath
	isMutter bool
	nodeID   uint32
	// sessionKind records which negotiation produced nodeID: "mutter" or
	// "portal". They behave differently downstream, so the logs must say which.
	sessionKind string
	width       int
	height      int

	frames    chan *Frame
	running   atomic.Bool
	seqNum    atomic.Uint64
	stopOnce  sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc

	cmd    *exec.Cmd
	pipeIn io.ReadCloser
	mu     sync.Mutex
}

// logf records which capture path was taken. This file used to have no logging
// at all, which made a black screen indistinguishable from a working capture of
// a static desktop: the frame counter increments identically either way.
func (c *waylandCapturer) logf(format string, args ...interface{}) {
	log.Printf("[WaylandCapture] "+format, args...)
}

func (c *waylandCapturer) closeFrames() {
	c.closeOnce.Do(func() {
		if c.frames != nil {
			close(c.frames)
		}
	})
}

// newWaylandCapturer initializes a ScreenCast portal session on Wayland.
func newWaylandCapturer(config CaptureConfig) (*waylandCapturer, error) {
	if config.FrameBufferSize <= 0 {
		config.FrameBufferSize = 2
	}
	if config.TargetFPS <= 0 {
		config.TargetFPS = 30
	}
	if config.DisplayIndex < 0 {
		config.DisplayIndex = 0
	}

	bus, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("wayland: cannot connect to D-Bus session bus: %w", err)
	}

	c := &waylandCapturer{
		config: config,
		bus:    bus,
		width:  1920,
		height: 1080,
	}

	return c, nil
}

func (c *waylandCapturer) Displays() ([]Display, error) {
	// Under Wayland, displays are dynamic per PipeWire stream or negotiated via Portal.
	return []Display{
		{
			Index:   0,
			Name:    "Wayland Display (PipeWire)",
			Bounds:  image.Rect(0, 0, c.width, c.height),
			Primary: true,
		},
	}, nil
}

func (c *waylandCapturer) Start(ctx context.Context) error {
	if c.running.Swap(true) {
		return ErrAlreadyStarted
	}

	c.frames = make(chan *Frame, c.config.FrameBufferSize)
	c.stopOnce = sync.Once{}
	c.closeOnce = sync.Once{}
	captureCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Initialize portal session and PipeWire stream with a quick timeout
	portalCtx, portalCancel := context.WithTimeout(captureCtx, portalNegotiationTimeout)
	defer portalCancel()

	if err := c.initPortalSession(portalCtx); err != nil || c.nodeID == 0 {
		if err != nil {
			c.logf("session negotiation failed: %v", err)
		} else {
			c.logf("session negotiation returned no PipeWire node id")
		}
		if errors.Is(portalCtx.Err(), context.DeadlineExceeded) {
			c.logf("negotiation hit its %s deadline - note the portal dialog waits for a human to click Share, which takes longer than that",
				portalNegotiationTimeout)
		}
		c.fallbackToRealOrSynthetic(captureCtx)
		return nil
	}

	c.logf("capturing via %s, node id %d, %dx%d", c.sessionKind, c.nodeID, c.width, c.height)
	go c.streamLoop(captureCtx)
	return nil
}

func (c *waylandCapturer) Frames() <-chan *Frame {
	return c.frames
}

func (c *waylandCapturer) Stop() {
	c.stopOnce.Do(func() {
		c.running.Store(false)
		if c.cancel != nil {
			c.cancel()
		}
		c.mu.Lock()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		if c.pipeIn != nil {
			_ = c.pipeIn.Close()
		}
		if c.session != "" && c.bus != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer stopCancel()
			// Close portal / mutter session with short timeout
			if c.isMutter {
				_ = c.bus.Object(mutterDest, c.session).CallWithContext(stopCtx, mutterSessionIface+".Stop", 0).Store()
			} else {
				_ = c.bus.Object(portalDest, c.session).CallWithContext(stopCtx, "org.freedesktop.portal.Session.Close", 0).Store()
			}
		}
		c.mu.Unlock()
		c.closeFrames()
	})
}

func (c *waylandCapturer) SetDisplay(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index > 0 {
		return ErrDisplayNotFound
	}
	c.config.DisplayIndex = index
	return nil
}

func getRestoreTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, restoreSessionFileRel)
}

func readRestoreToken() string {
	p := getRestoreTokenPath()
	if p == "" {
		return ""
	}
	cleanPath := filepath.Clean(p)
	data, err := os.ReadFile(cleanPath) // #nosec G304 -- path resolved from user home config
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveRestoreToken persists the portal session restore token.
func SaveRestoreToken(token string) {
	if token == "" {
		return
	}
	p := getRestoreTokenPath()
	if p == "" {
		return
	}
	cleanPath := filepath.Clean(p)
	_ = os.MkdirAll(filepath.Dir(cleanPath), 0700)
	_ = os.WriteFile(cleanPath, []byte(token), 0600)
}

func (c *waylandCapturer) initPortalSession(ctx context.Context) error {
	// 1. Try GNOME Mutter native ScreenCast first (direct Mutter Monitor recording)
	if err := c.initMutterSession(ctx); err == nil && c.nodeID > 0 {
		c.sessionKind = "mutter"
		c.logf("Mutter ScreenCast negotiated, node id %d", c.nodeID)
		return nil
	} else if err != nil {
		c.logf("Mutter ScreenCast unavailable (%v), falling through to xdg-desktop-portal", err)
	} else {
		c.logf("Mutter ScreenCast returned no node id, falling through to xdg-desktop-portal")
	}

	// 2. Try XDG Desktop Portal ScreenCast
	obj := c.bus.Object(portalDest, dbus.ObjectPath(portalPath))

	sessionToken := fmt.Sprintf("barahn_session_%d", time.Now().UnixNano())
	createOptions := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(sessionToken),
	}

	var sessionHandle dbus.ObjectPath
	err := obj.CallWithContext(ctx, portalScreenCast+".CreateSession", 0, createOptions).Store(&sessionHandle)
	if err != nil {
		return fmt.Errorf("CreateSession D-Bus call failed: %w", err)
	}
	c.session = sessionHandle
	c.isMutter = false

	selectToken := fmt.Sprintf("barahn_sources_%d", time.Now().UnixNano())
	selectOptions := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // Monitor
		"multiple":     dbus.MakeVariant(false),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // Embedded cursor
		"handle_token": dbus.MakeVariant(selectToken),
	}
	if tok := readRestoreToken(); tok != "" {
		selectOptions["restore_token"] = dbus.MakeVariant(tok)
	}

	var selectHandle dbus.ObjectPath
	_ = obj.CallWithContext(ctx, portalScreenCast+".SelectSources", 0, c.session, selectOptions).Store(&selectHandle)

	startToken := fmt.Sprintf("barahn_start_%d", time.Now().UnixNano())
	startOptions := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(startToken),
	}

	// Listen for portal Request Response signal on session bus
	sigChan := make(chan *dbus.Signal, 10)
	c.bus.Signal(sigChan)
	defer c.bus.RemoveSignal(sigChan)

	c.logf("portal Start called - waiting for the user to accept the screen-share dialog")
	var startHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalScreenCast+".Start", 0, c.session, "", startOptions).Store(&startHandle)
	if err != nil {
		return fmt.Errorf("Start ScreenCast portal call failed: %w", err)
	}

	// Wait for user to grant permission and portal to return stream node ID
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-sigChan:
			if sig == nil {
				continue
			}
			if strings.HasSuffix(string(sig.Path), startToken) || sig.Name == "org.freedesktop.portal.Request.Response" {
				if len(sig.Body) >= 2 {
					if respCode, ok := sig.Body[0].(uint32); ok && respCode == 0 {
						if results, ok := sig.Body[1].(map[string]dbus.Variant); ok {
							if streamsVar, exists := results["streams"]; exists {
								c.parseStreamsVariant(streamsVar)
							}
							if tokVar, exists := results["restore_token"]; exists {
								if tok, ok := tokVar.Value().(string); ok {
									SaveRestoreToken(tok)
								}
							}
							return nil
						}
					}
				}
			}
		}
	}
}

func (c *waylandCapturer) parseStreamsVariant(streamsVar dbus.Variant) {
	// D-Bus signature for streams: a(ua{sv})
	val := streamsVar.Value()
	switch s := val.(type) {
	case [][2]interface{}:
		if len(s) > 0 {
			if id, ok := s[0][0].(uint32); ok {
				c.nodeID = id
			}
			if props, ok := s[0][1].(map[string]dbus.Variant); ok {
				c.extractDimensions(props)
			}
		}
	case []interface{}:
		for _, item := range s {
			if pair, ok := item.([]interface{}); ok && len(pair) >= 2 {
				if id, ok := pair[0].(uint32); ok {
					c.nodeID = id
				}
				if props, ok := pair[1].(map[string]dbus.Variant); ok {
					c.extractDimensions(props)
				}
			}
		}
	}
}

func (c *waylandCapturer) extractDimensions(props map[string]dbus.Variant) {
	if sizeVar, exists := props["size"]; exists {
		if sizeSlice, ok := sizeVar.Value().([]int32); ok && len(sizeSlice) == 2 {
			if sizeSlice[0] > 0 && sizeSlice[1] > 0 {
				c.width = int(sizeSlice[0])
				c.height = int(sizeSlice[1])
			}
		}
	}
}

func (c *waylandCapturer) initMutterSession(ctx context.Context) error {
	obj := c.bus.Object(mutterDest, dbus.ObjectPath(mutterPath))
	var sessionPath dbus.ObjectPath
	createOpts := map[string]dbus.Variant{}
	err := obj.CallWithContext(ctx, mutterScreenCast+".CreateSession", 0, createOpts).Store(&sessionPath)
	if err != nil {
		return err
	}
	c.session = sessionPath
	c.isMutter = true

	sessObj := c.bus.Object(mutterDest, sessionPath)
	var streamPath dbus.ObjectPath
	recordOpts := map[string]dbus.Variant{
		"cursor-mode": dbus.MakeVariant(uint32(1)), // Embedded cursor
	}

	// Prioritize RecordMonitor to record user's physical screen
	err = sessObj.CallWithContext(ctx, mutterSessionIface+".RecordMonitor", 0, "", recordOpts).Store(&streamPath)
	if err != nil {
		// Fallback to RecordVirtual only if RecordMonitor is not supported
		err = sessObj.CallWithContext(ctx, mutterSessionIface+".RecordVirtual", 0, recordOpts).Store(&streamPath)
	}
	if err != nil {
		return err
	}

	_ = sessObj.CallWithContext(ctx, mutterSessionIface+".Start", 0).Store()

	streamObj := c.bus.Object(mutterDest, streamPath)
	if propVal, propErr := streamObj.GetProperty(mutterStreamIface + ".Parameters"); propErr == nil {
		if params, ok := propVal.Value().(map[string]dbus.Variant); ok {
			if nodeIDVar, exists := params["pipewire-node-id"]; exists {
				if id, ok := nodeIDVar.Value().(uint32); ok {
					c.nodeID = id
				}
			}
			if sizeVar, exists := params["size"]; exists {
				if sizeSlice, ok := sizeVar.Value().([]int32); ok && len(sizeSlice) == 2 {
					if sizeSlice[0] > 0 && sizeSlice[1] > 0 {
						c.width = int(sizeSlice[0])
						c.height = int(sizeSlice[1])
					}
				}
			}
		}
	}
	return nil
}

func (c *waylandCapturer) streamLoop(ctx context.Context) {
	defer c.closeFrames()
	gstPath, err := exec.LookPath("gst-launch-1.0")
	if err != nil {
		c.logf("gst-launch-1.0 not found - rendering a synthetic desktop, NOT the real screen (install gstreamer1.0-tools and gstreamer1.0-pipewire)")
		c.renderSyntheticFrames(ctx)
		return
	}

	// GStreamer pipeline to pull frames from PipeWire into raw RGBA stdout
	var args []string
	if c.nodeID > 0 {
		args = []string{
			"-q",
			"pipewiresrc", fmt.Sprintf("target-object=%d", c.nodeID), "do-timestamp=true", "keepalive-time=1000",
			"!", "videoconvert",
			"!", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", c.width, c.height, c.config.TargetFPS),
			"!", "fdsink", "fd=1",
		}
	} else {
		args = []string{
			"-q",
			"pipewiresrc", "do-timestamp=true", "keepalive-time=1000",
			"!", "videoconvert",
			"!", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", c.width, c.height, c.config.TargetFPS),
			"!", "fdsink", "fd=1",
		}
	}

	c.logf("starting GStreamer: %s %s", gstPath, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, gstPath, args...) // #nosec G204 -- gstPath resolved via exec.LookPath
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.logf("could not open the GStreamer stdout pipe: %v", err)
		c.fallbackToRealOrSynthetic(ctx)
		return
	}

	c.mu.Lock()
	c.cmd = cmd
	c.pipeIn = stdout
	c.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.logf("GStreamer failed to start: %v", err)
		c.fallbackToRealOrSynthetic(ctx)
		return
	}

	frameSize := c.width * c.height * 4
	bufReader := bufio.NewReaderSize(stdout, frameSize*2)
	frameBuf := make([]byte, frameSize)
	framesDelivered := 0

	targetInterval := time.Second / time.Duration(c.config.TargetFPS)
	ticker := time.NewTicker(targetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := io.ReadFull(bufReader, frameBuf)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					c.logf("GStreamer produced no frames (%v) after %d delivered - the PipeWire node was never readable", err, framesDelivered)
					c.fallbackToRealOrSynthetic(ctx)
					return
				}
				continue
			}

			if framesDelivered == 0 {
				c.logf("first real frame received from PipeWire (%dx%d) - this is the live screen", c.width, c.height)
			}
			framesDelivered++

			img := &image.RGBA{
				Pix:    make([]byte, frameSize),
				Stride: c.width * 4,
				Rect:   image.Rect(0, 0, c.width, c.height),
			}
			copy(img.Pix, frameBuf)

			frame := &Frame{
				Image:        img,
				Bounds:       img.Rect,
				DisplayIndex: c.config.DisplayIndex,
				CapturedAt:   time.Now(),
				SequenceNum:  c.seqNum.Add(1),
			}

			select {
			case c.frames <- frame:
			default:
				// Drop frame if consumer buffer is full
			}
		}
	}
}

func (c *waylandCapturer) fallbackToRealOrSynthetic(ctx context.Context) {
	if xcap, xerr := newX11Capturer(c.config); xerr == nil {
		if err := xcap.Start(ctx); err == nil {
			// Under a Wayland session this usually captures nothing useful:
			// Xwayland does not composite the Wayland desktop into the X root
			// window, so the grab is an empty image at the right geometry.
			c.logf("falling back to X11 capture - under Wayland this typically yields a BLANK screen, not the desktop")
			go func() {
				defer c.closeFrames()
				defer xcap.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case f, ok := <-xcap.Frames():
						if !ok {
							return
						}
						if !c.running.Load() {
							return
						}
						select {
						case <-ctx.Done():
							return
						case c.frames <- f:
						}
					}
				}
			}()
			return
		}
	}
	go c.renderSyntheticFrames(ctx)
}

func (c *waylandCapturer) renderSyntheticFrames(ctx context.Context) {
	c.logf("rendering SYNTHETIC frames - the viewer is not seeing the real desktop")
	defer c.closeFrames()
	ticker := time.NewTicker(time.Second / time.Duration(c.config.TargetFPS))
	defer ticker.Stop()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "wayland-desktop"
	}

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			seq := c.seqNum.Add(1)
			img := GenerateTestDesktopImage(c.width, c.height, "linux (Wayland)", hostname, "wayland-agent", seq, t.UTC(), c.width/2, c.height/2)
			frame := &Frame{
				Image:        img,
				Bounds:       img.Bounds(),
				DisplayIndex: c.config.DisplayIndex,
				CapturedAt:   t,
				SequenceNum:  seq,
			}
			select {
			case c.frames <- frame:
			default:
			}
		}
	}
}
