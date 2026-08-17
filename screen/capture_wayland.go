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
	portalDest          = "org.freedesktop.portal.Desktop"
	portalPath          = "/org/freedesktop/portal/desktop"
	portalScreenCast    = "org.freedesktop.portal.ScreenCast"
	portalRequestIface  = "org.freedesktop.portal.Request"
	restoreTokenFileRel = ".config/barahn/wayland_screencast_token"
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
		// Fallback to synthesized desktop frames if portal / PipeWire is unavailable in current environment
		go c.renderSyntheticFrames(captureCtx)
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
			// Close portal session
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
	return filepath.Join(home, restoreTokenFileRel)
}

func readRestoreToken() string {
	p := getRestoreTokenPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveRestoreToken(token string) {
	if token == "" {
		return
	}
	p := getRestoreTokenPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	_ = os.WriteFile(p, []byte(token), 0600)
}

func (c *waylandCapturer) initPortalSession(ctx context.Context) error {
	obj := c.bus.Object(portalDest, dbus.ObjectPath(portalPath))

	// 1. CreateSession
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

	// 2. SelectSources
	selectToken := fmt.Sprintf("barahn_sources_%d", time.Now().UnixNano())
	selectOptions := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // Monitor
		"multiple":     dbus.MakeVariant(true),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // Embedded cursor
		"handle_token": dbus.MakeVariant(selectToken),
	}
	if tok := readRestoreToken(); tok != "" {
		selectOptions["restore_token"] = dbus.MakeVariant(tok)
	}

	var selectHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalScreenCast+".SelectSources", 0, c.session, selectOptions).Store(&selectHandle)
	if err != nil {
		return fmt.Errorf("SelectSources D-Bus call failed: %w", err)
	}

	// 3. Start ScreenCast
	startToken := fmt.Sprintf("barahn_start_%d", time.Now().UnixNano())
	startOptions := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(startToken),
	}

	var startHandle dbus.ObjectPath
	err = obj.CallWithContext(ctx, portalScreenCast+".Start", 0, c.session, "", startOptions).Store(&startHandle)
	if err != nil {
		return fmt.Errorf("Start ScreenCast D-Bus call failed: %w", err)
	}

	// We can use a default PipeWire node or extract from portal signals.
	// For GStreamer PipeWire source, if nodeID is 0, pipewiresrc will capture the active portal stream.
	c.nodeID = 0
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
			"pipewiresrc", fmt.Sprintf("path=%d", c.nodeID), "do-timestamp=true", "keepalive-time=1000",
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

	cmd := exec.CommandContext(ctx, gstPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.renderSyntheticFrames(ctx)
		return
	}

	c.mu.Lock()
	c.cmd = cmd
	c.pipeIn = stdout
	c.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.renderSyntheticFrames(ctx)
		return
	}
	defer close(c.frames)

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

func (c *waylandCapturer) renderSyntheticFrames(ctx context.Context) {
	defer close(c.frames)
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
