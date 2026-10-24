// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package stream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barahn/remotekit/clipboard"
)

// dataSessionBothClipboards creates a loopback session with fake memory clipboards on both ends.
func dataSessionBothClipboards(t *testing.T, setup func(*Runner, *Viewer), permissions ...string) (*Runner, *Viewer, *clipboard.MemoryClipboard, *clipboard.MemoryClipboard) {
	t.Helper()
	rClip := clipboard.NewMemoryClipboard()
	vClip := clipboard.NewMemoryClipboard()

	r := New(Config{ID: "agent-1"})
	r.clip = rClip
	r.RequireDataChannel(true)
	r.Grant(permissions...)

	v := NewViewer(ViewerConfig{ID: "viewer-1", AgentID: "agent-1"})
	v.clip = vClip

	if setup != nil {
		setup(r, v)
	}
	session(t, r, v)
	waitChannels(t, v)
	return r, v, rClip, vClip
}

// 1. Files <= 25 MB copied on one side arrive intact on the other clipboard through staging.
func TestCopiedFilesUnderTheLimitArriveOnTheOtherClipboard(t *testing.T) {
	// Create temporary files to copy
	srcDir := t.TempDir()
	file1Path := filepath.Join(srcDir, "test_doc.txt")
	file1Content := []byte("Hello remote clipboard files! Content of test_doc.txt")
	if err := os.WriteFile(file1Path, file1Content, 0600); err != nil {
		t.Fatal(err)
	}

	w, setup := watchClipboard()
	r, v, rClip, vClip := dataSessionBothClipboards(t, setup, PermissionClipboard, PermissionFileTransfer)

	// Host to Viewer: copy file on Host clipboard
	hostList := clipboard.FileList{Files: []string{file1Path}, Cut: false}
	if err := rClip.SetFiles(context.Background(), hostList); err != nil {
		t.Fatal(err)
	}

	// Verify Runner emits ClipboardFromHost event
	ev := w.event(t)
	if ev.Direction != ClipboardFromHost || ev.TooLarge || len(ev.Files) != 1 || ev.Files[0] != "test_doc.txt" {
		t.Fatalf("unexpected host copy event: %+v", ev)
	}

	// Wait for file to arrive on Viewer's clipboard
	deadline := time.Now().Add(5 * time.Second)
	var viewerFiles clipboard.FileList
	for time.Now().Before(deadline) {
		vf, err := vClip.GetFiles(context.Background())
		if err == nil && len(vf.Files) > 0 {
			viewerFiles = vf
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(viewerFiles.Files) == 0 {
		t.Fatal("host copied file never arrived on viewer clipboard")
	}

	// Verify the file was staged and content matches
	stagedFile := viewerFiles.Files[0]
	vStagingDir := v.ClipStagingDir()
	if vStagingDir == "" || !strings.HasPrefix(stagedFile, vStagingDir) {
		t.Fatalf("staged file %q is not within viewer staging dir %q", stagedFile, vStagingDir)
	}
	data, err := os.ReadFile(stagedFile)
	if err != nil {
		t.Fatalf("failed to read received file: %v", err)
	}
	if string(data) != string(file1Content) {
		t.Fatalf("received file content = %q, want %q", string(data), string(file1Content))
	}

	// Viewer to Host: copy file from Viewer to Host
	viewerSrc := filepath.Join(srcDir, "viewer_file.bin")
	viewerContent := []byte("viewer binary payload 123456789")
	if err := os.WriteFile(viewerSrc, viewerContent, 0600); err != nil {
		t.Fatal(err)
	}

	viewerList := clipboard.FileList{Files: []string{viewerSrc}, Cut: true}
	if err := vClip.SetFiles(context.Background(), viewerList); err != nil {
		t.Fatal(err)
	}

	// Verify host clipboard receives the file from viewer
	deadline = time.Now().Add(5 * time.Second)
	var hostReceived clipboard.FileList
	for time.Now().Before(deadline) {
		hf, err := rClip.GetFiles(context.Background())
		if err == nil && len(hf.Files) > 0 && hf.Cut == true {
			hostReceived = hf
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(hostReceived.Files) == 0 {
		t.Fatal("viewer copied file never arrived on host clipboard")
	}

	rStagingDir := r.ClipStagingDir()
	if rStagingDir == "" || !strings.HasPrefix(hostReceived.Files[0], rStagingDir) {
		t.Fatalf("staged file %q is not within host staging dir %q", hostReceived.Files[0], rStagingDir)
	}
	hData, err := os.ReadFile(hostReceived.Files[0])
	if err != nil {
		t.Fatalf("failed to read host staged file: %v", err)
	}
	if string(hData) != string(viewerContent) {
		t.Fatalf("host received content = %q, want %q", string(hData), string(viewerContent))
	}
}

// 2. Files over 25 MB emit a ClipboardEvent with TooLarge=true and are NOT transmitted across the wire.
func TestCopiedFilesOverTheLimitAreReportedNotSent(t *testing.T) {
	srcDir := t.TempDir()
	bigFilePath := filepath.Join(srcDir, "huge_archive.iso")
	f, err := os.Create(bigFilePath)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to 26 MB (sparse file)
	targetSize := int64(26 * 1024 * 1024)
	if err := f.Truncate(targetSize); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	w, setup := watchClipboard()
	_, _, rClip, vClip := dataSessionBothClipboards(t, setup, PermissionClipboard, PermissionFileTransfer)

	// Copy the big file on the host clipboard
	if err := rClip.SetFiles(context.Background(), clipboard.FileList{Files: []string{bigFilePath}}); err != nil {
		t.Fatal(err)
	}

	// Event must be reported with TooLarge=true
	ev := w.event(t)
	if ev.Direction != ClipboardFromHost || !ev.TooLarge || int64(ev.Size) != targetSize {
		t.Fatalf("expected over-limit event, got: %+v", ev)
	}

	// Wait to verify viewer clipboard received nothing
	time.Sleep(200 * time.Millisecond)
	vf, err := vClip.GetFiles(context.Background())
	if err == nil && len(vf.Files) > 0 {
		t.Fatalf("viewer received files when size exceeded limit: %v", vf.Files)
	}
}

// 3. File clipboard requires BOTH PermissionClipboard AND PermissionFileTransfer.
func TestFileClipboardNeedsBothPermissions(t *testing.T) {
	srcDir := t.TempDir()
	testFilePath := filepath.Join(srcDir, "note.txt")
	if err := os.WriteFile(testFilePath, []byte("permission test"), 0600); err != nil {
		t.Fatal(err)
	}

	// Case 1: Only PermissionClipboard granted
	{
		r1, _, rClip1, vClip1 := dataSessionBothClipboards(t, nil, PermissionClipboard)
		_ = rClip1.SetFiles(context.Background(), clipboard.FileList{Files: []string{testFilePath}})
		time.Sleep(300 * time.Millisecond)
		vf, _ := vClip1.GetFiles(context.Background())
		if len(vf.Files) > 0 {
			t.Fatal("files transferred with only PermissionClipboard granted")
		}
		_ = r1 // keep active
	}

	// Case 2: Only PermissionFileTransfer granted
	{
		r2, _, rClip2, vClip2 := dataSessionBothClipboards(t, nil, PermissionFileTransfer)
		_ = rClip2.SetFiles(context.Background(), clipboard.FileList{Files: []string{testFilePath}})
		time.Sleep(300 * time.Millisecond)
		vf, _ := vClip2.GetFiles(context.Background())
		if len(vf.Files) > 0 {
			t.Fatal("files transferred with only PermissionFileTransfer granted")
		}
		_ = r2
	}

	// Case 3: Both granted dynamically
	{
		r3, _, rClip3, vClip3 := dataSessionBothClipboards(t, nil, PermissionClipboard)
		_ = rClip3.SetFiles(context.Background(), clipboard.FileList{Files: []string{testFilePath}})
		time.Sleep(200 * time.Millisecond)

		// Grant the second required permission
		r3.Grant(PermissionFileTransfer)
		// Trigger file copy again
		_ = rClip3.SetFiles(context.Background(), clipboard.FileList{Files: []string{testFilePath}, Cut: true})

		deadline := time.Now().Add(5 * time.Second)
		var got clipboard.FileList
		for time.Now().Before(deadline) {
			vf, err := vClip3.GetFiles(context.Background())
			if err == nil && len(vf.Files) > 0 {
				got = vf
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(got.Files) == 0 {
			t.Fatal("files not transferred after both permissions granted")
		}
	}
}

// 4. Staging folder exists during the session and is cleaned up when session ends.
func TestTheStagingFolderGoesWithTheSession(t *testing.T) {
	srcDir := t.TempDir()
	testFilePath := filepath.Join(srcDir, "staged_sample.txt")
	content := []byte("clean staging folder test")
	if err := os.WriteFile(testFilePath, content, 0600); err != nil {
		t.Fatal(err)
	}

	rClip := clipboard.NewMemoryClipboard()
	vClip := clipboard.NewMemoryClipboard()

	r := New(Config{ID: "agent-staging"})
	r.clip = rClip
	r.RequireDataChannel(true)
	r.Grant(PermissionClipboard, PermissionFileTransfer)

	v := NewViewer(ViewerConfig{ID: "viewer-staging", AgentID: "agent-staging"})
	v.clip = vClip

	agentEnd, viewerEnd := memPipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = r.Serve(ctx, agentEnd); done <- struct{}{} }()
	go func() { _ = v.Serve(ctx, viewerEnd); done <- struct{}{} }()

	waitChannels(t, v)

	// Copy file from host to viewer
	if err := rClip.SetFiles(context.Background(), clipboard.FileList{Files: []string{testFilePath}}); err != nil {
		t.Fatal(err)
	}

	// Wait for file on viewer clipboard
	deadline := time.Now().Add(5 * time.Second)
	var stagedFile string
	for time.Now().Before(deadline) {
		vf, err := vClip.GetFiles(context.Background())
		if err == nil && len(vf.Files) > 0 {
			stagedFile = vf.Files[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stagedFile == "" {
		t.Fatal("file was not transferred to viewer")
	}

	vStagingDir := v.ClipStagingDir()
	if vStagingDir == "" {
		t.Fatal("viewer staging directory is empty")
	}

	// Verify staging folder exists on disk during session
	if fi, err := os.Stat(vStagingDir); err != nil || !fi.IsDir() {
		t.Fatalf("staging directory %q does not exist during session: %v", vStagingDir, err)
	}
	if _, err := os.Stat(stagedFile); err != nil {
		t.Fatalf("staged file %q does not exist during session: %v", stagedFile, err)
	}

	// End the session
	cancel()
	<-done
	<-done

	// Verify staging folder was deleted on session cleanup
	if _, err := os.Stat(vStagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging directory %q still exists after session ended: %v", vStagingDir, err)
	}
}
