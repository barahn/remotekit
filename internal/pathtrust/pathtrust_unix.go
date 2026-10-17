// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

//go:build !windows

// Package pathtrust refuses to let a privileged process consume configuration
// from locations that an unprivileged local user can modify.
package pathtrust

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// maxFileSize bounds how much of a trusted file is read.
const maxFileSize = 1 << 20

func privileged() bool {
	return os.Geteuid() == 0
}

func ownerOf(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint32(st.Uid), true
}

// checkChain verifies every component from p up to the filesystem root. Each
// directory must be owned by root and must not be writable by group or other
// (a sticky, root-owned directory such as /tmp is accepted), and any symlink
// encountered must itself be owned by root.
func checkChain(p string) error {
	for cur := p; ; cur = filepath.Dir(cur) {
		li, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if li.Mode()&os.ModeSymlink != 0 {
			if uid, ok := ownerOf(li); !ok || uid != 0 {
				return fmt.Errorf("untrusted symlink %q: not owned by root", cur)
			}
		}
		di, err := os.Stat(cur)
		if err != nil {
			return err
		}
		if uid, ok := ownerOf(di); !ok || uid != 0 {
			return fmt.Errorf("untrusted directory %q: not owned by root", cur)
		}
		if di.Mode().Perm()&0o022 != 0 && di.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("untrusted directory %q: writable by group or others", cur)
		}
		if next := filepath.Dir(cur); next == cur {
			return nil
		}
	}
}

func checkDirs(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := checkChain(abs); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	return checkChain(resolved)
}

// VerifyPath checks that path can be trusted by a root process. An existing
// file must be a regular, root-owned file that is not a symlink and is not
// writable by group or others; a missing file is acceptable provided its parent
// directory chain is trusted. It is a no-op for unprivileged processes.
func VerifyPath(path string) error {
	if !privileged() {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); errors.Is(err, os.ErrNotExist) {
		return checkDirs(filepath.Dir(abs))
	}
	f, err := openTrusted(abs)
	if err != nil {
		return err
	}
	return f.Close()
}

func openTrusted(abs string) (*os.File, error) {
	if err := checkDirs(filepath.Dir(abs)); err != nil {
		return nil, fmt.Errorf("untrusted credential path %q: %w", abs, err)
	}
	f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- ancestors verified above, symlinks refused
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	uid, ok := ownerOf(fi)
	switch {
	case !fi.Mode().IsRegular():
		err = fmt.Errorf("untrusted credential file %q: not a regular file", abs)
	case !ok || uid != 0:
		err = fmt.Errorf("untrusted credential file %q: not owned by root", abs)
	case fi.Mode().Perm()&0o022 != 0:
		err = fmt.Errorf("untrusted credential file %q: writable by group or others", abs)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadFile reads path. When the process runs as root, the file and every
// directory above it must be trusted (see VerifyPath); otherwise it behaves
// like os.ReadFile.
func ReadFile(path string) ([]byte, error) {
	if !privileged() {
		return os.ReadFile(path) // #nosec G304 -- unprivileged process reads only what it could already read
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := openTrusted(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileSize))
}
