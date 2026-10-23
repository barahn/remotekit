// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// watch runs w until the test ends and collects what it reports.
type watched struct {
	mu        sync.Mutex
	changes   []string
	concealed int
}

func (s *watched) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.changes...), s.concealed
}

func watch(t *testing.T, w *Watcher) *watched {
	t.Helper()
	s := &watched{}
	w.OnConcealed(func() {
		s.mu.Lock()
		s.concealed++
		s.mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Start(ctx, func(text string) {
		s.mu.Lock()
		s.changes = append(s.changes, text)
		s.mu.Unlock()
	})
	return s
}

// What a password manager copies is marked as concealed, and the watcher
// never hands it on: it reports that something was held back instead.
func TestWatcherNeverHandsOnConcealedContent(t *testing.T) {
	cb := NewMemoryClipboard()
	w := NewWatcher(cb, 10*time.Millisecond)
	s := watch(t, w)
	time.Sleep(30 * time.Millisecond)

	_ = cb.SetConcealedText(context.Background(), "hunter2")
	time.Sleep(60 * time.Millisecond)
	if changes, concealed := s.snapshot(); len(changes) != 0 || concealed != 1 {
		t.Fatalf("after a concealed copy: changes %q, concealed %d; want none handed on, one reported", changes, concealed)
	}

	_ = cb.SetText(context.Background(), "an ordinary copy")
	time.Sleep(60 * time.Millisecond)
	if changes, concealed := s.snapshot(); len(changes) != 1 || changes[0] != "an ordinary copy" || concealed != 1 {
		t.Fatalf("after an ordinary copy: changes %q, concealed %d", changes, concealed)
	}
}

// flaky fails its first reads, as a clipboard tool that is not ready yet.
type flaky struct {
	*MemoryClipboard
	mu    sync.Mutex
	fails int
}

func (f *flaky) GetText(ctx context.Context) (string, error) {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return "", errors.New("not ready")
	}
	f.mu.Unlock()
	return f.MemoryClipboard.GetText(ctx)
}

// Whatever was on the clipboard before the watcher could first read it is the
// baseline, never a change: a clipboard that cannot be read at the start must
// not have its old content sent once it can.
func TestWatcherNeverSendsWhatWasThereBeforeItsFirstRead(t *testing.T) {
	cb := &flaky{MemoryClipboard: NewMemoryClipboard(), fails: 3}
	_ = cb.SetText(context.Background(), "copied before the session")
	w := NewWatcher(cb, 10*time.Millisecond)
	s := watch(t, w)
	time.Sleep(100 * time.Millisecond)
	if changes, _ := s.snapshot(); len(changes) != 0 {
		t.Fatalf("the watcher sent %q, which was there before it could read", changes)
	}
	_ = cb.SetText(context.Background(), "copied during the session")
	time.Sleep(60 * time.Millisecond)
	if changes, _ := s.snapshot(); len(changes) != 1 || changes[0] != "copied during the session" {
		t.Fatalf("changes %q, want the copy made during the session", changes)
	}
}

// KeePassXC, KDE's Klipper and others mark a secret on X11 and Wayland with an
// x-kde-passwordManagerHint target whose content is "secret".
func TestThePasswordManagerHintMarksX11Content(t *testing.T) {
	read := func(value string, err error) func(string) ([]byte, error) {
		return func(target string) ([]byte, error) {
			if target != kdePasswordHint {
				t.Errorf("read target %q", target)
			}
			return []byte(value), err
		}
	}
	for _, tc := range []struct {
		targets []string
		read    func(string) ([]byte, error)
		want    bool
	}{
		{[]string{"UTF8_STRING", "TARGETS", kdePasswordHint}, read("secret", nil), true},
		{[]string{"UTF8_STRING", kdePasswordHint}, read("secret\n", nil), true},
		{[]string{"UTF8_STRING", kdePasswordHint}, read("", nil), false},
		{[]string{"UTF8_STRING", kdePasswordHint}, read("", errors.New("refused")), true},
		{[]string{"UTF8_STRING", "text/plain"}, read("secret", nil), false},
	} {
		if got := concealedByTargets(tc.targets, tc.read); got != tc.want {
			t.Errorf("targets %v: concealed %v, want %v", tc.targets, got, tc.want)
		}
	}
}

// On Windows a password manager either adds a format whose presence says
// "keep this out of monitors and history", or sets CanIncludeInClipboardHistory
// to zero.
func TestWindowsFormatsMarkConcealedContent(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, m := range names {
				if m == n {
					return true
				}
			}
			return false
		}
	}
	dword := func(v uint32, ok bool) func(string) (uint32, bool) {
		return func(string) (uint32, bool) { return v, ok }
	}
	for _, tc := range []struct {
		name  string
		has   func(string) bool
		dword func(string) (uint32, bool)
		want  bool
	}{
		{"exclude", has(winExcludeFormat), dword(0, false), true},
		{"viewer ignore", has(winViewerIgnoreFormat), dword(0, false), true},
		{"history off", has(winHistoryFormat), dword(0, true), true},
		{"history on", has(winHistoryFormat), dword(1, true), false},
		{"history unreadable", has(winHistoryFormat), dword(0, false), false},
		{"plain text", has(), dword(0, false), false},
	} {
		if got := concealedByWindowsFormats(tc.has, tc.dword); got != tc.want {
			t.Errorf("%s: concealed %v, want %v", tc.name, got, tc.want)
		}
	}
}

// On macOS (Darwin) password managers add specific pasteboard types like
// org.nspasteboard.ConcealedType, org.nspasteboard.AutoGeneratedType,
// org.nspasteboard.TransientType, or com.agilebits.onepassword.
func TestDarwinTypesMarkConcealedContent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []string
		want  bool
	}{
		{"concealed type", []string{"public.utf8-plain-text", darwinConcealedType}, true},
		{"autogenerated type", []string{darwinAutoGeneratedType, "public.utf8-plain-text"}, true},
		{"transient type", []string{"NSStringPboardType", darwinTransientType}, true},
		{"1password type", []string{darwinOnePasswordType}, true},
		{"ordinary text", []string{"public.utf8-plain-text", "NSStringPboardType"}, false},
		{"empty types", []string{}, false},
	} {
		if got := concealedByDarwinTypes(tc.types); got != tc.want {
			t.Errorf("%s: concealed %v, want %v", tc.name, got, tc.want)
		}
	}
}
