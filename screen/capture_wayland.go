//go:build linux

package screen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

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
	config  CaptureConfig
	bus     *dbus.Conn
	session dbus.ObjectPath
	nodeID  uint32
	width   int
	height  int

	frames   chan *Frame
	running  atomic.Bool
	seqNum   atomic.Uint64
	stopOnce sync.Once
	cancel   context.CancelFunc

	cmd    *exec.Cmd
	pipeIn io.ReadCloser
	mu     sync.Mutex
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
	captureCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Initialize portal session and PipeWire stream
	if err := c.initPortalSession(captureCtx); err != nil {
		c.fallbackToRealOrSynthetic(captureCtx)
		return nil
	}

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
			// Close portal / mutter session
			_ = c.bus.Object(portalDest, c.session).Call("org.freedesktop.portal.Session.Close", 0).Store()
		}
		c.mu.Unlock()
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
		return nil
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

	var startHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalScreenCast+".Start", 0, c.session, "", startOptions).Store(&startHandle)
	if err != nil {
		return fmt.Errorf("Start ScreenCast portal call failed: %w", err)
	}

	// Wait for user to grant permission and portal to return stream node ID
	timeout := time.After(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return nil
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
	gstPath, err := exec.LookPath("gst-launch-1.0")
	if err != nil {
		// Fall back to synthetic desktop renderer if gst-launch-1.0 is not installed
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

	cmd := exec.CommandContext(ctx, gstPath, args...) // #nosec G204 -- gstPath resolved via exec.LookPath
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.fallbackToRealOrSynthetic(ctx)
		return
	}

	c.mu.Lock()
	c.cmd = cmd
	c.pipeIn = stdout
	c.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.fallbackToRealOrSynthetic(ctx)
		return
	}

	frameSize := c.width * c.height * 4
	bufReader := bufio.NewReaderSize(stdout, frameSize*2)
	frameBuf := make([]byte, frameSize)

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
					c.fallbackToRealOrSynthetic(ctx)
					return
				}
				continue
			}

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
		_ = xcap.Start(ctx)
		go func() {
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
					default:
					}
				}
			}
		}()
		return
	}
	c.renderSyntheticFrames(ctx)
}


func (c *waylandCapturer) renderSyntheticFrames(ctx context.Context) {
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
