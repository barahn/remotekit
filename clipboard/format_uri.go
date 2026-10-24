// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"net/url"
	"strings"
)

// Target constants for X11 clipboard selections.
const (
	TargetURIList          = "text/uri-list"
	TargetGnomeCopiedFiles = "x-special/gnome-copied-files"
)

// EncodeURIList encodes a list of local file paths as a text/uri-list (RFC 2483).
// Each file path is converted to a file:// URI and separated by CRLF (\r\n).
func EncodeURIList(files []string) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range files {
		u := url.URL{
			Scheme: "file",
			Path:   f,
		}
		b.WriteString(u.String())
		b.WriteString("\r\n")
	}
	return b.String()
}

// DecodeURIList decodes a text/uri-list string into a slice of local file paths.
// Lines starting with '#' (comments) and empty lines are ignored.
func DecodeURIList(data string) []string {
	var files []string
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			continue
		}
		if u.Scheme == "file" || u.Scheme == "" {
			path := u.Path
			if path != "" {
				files = append(files, path)
			}
		}
	}
	return files
}

// EncodeGnomeCopiedFiles encodes a FileList into the format used by GNOME Nautilus
// and other FreeDesktop file managers (x-special/gnome-copied-files).
// The first line indicates the operation ("copy\n" or "cut\n"), followed by file:// URIs.
func EncodeGnomeCopiedFiles(list FileList) string {
	if len(list.Files) == 0 {
		return ""
	}
	var b strings.Builder
	if list.Cut {
		b.WriteString("cut\n")
	} else {
		b.WriteString("copy\n")
	}
	for _, f := range list.Files {
		u := url.URL{
			Scheme: "file",
			Path:   f,
		}
		b.WriteString(u.String())
		b.WriteString("\n")
	}
	return b.String()
}

// DecodeGnomeCopiedFiles decodes an x-special/gnome-copied-files string into a FileList.
func DecodeGnomeCopiedFiles(data string) (FileList, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return FileList{}, nil
	}

	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return FileList{}, nil
	}

	cut := false
	startIndex := 0

	action := strings.TrimSpace(lines[0])
	if strings.EqualFold(action, "cut") {
		cut = true
		startIndex = 1
	} else if strings.EqualFold(action, "copy") {
		cut = false
		startIndex = 1
	}

	var files []string
	for i := startIndex; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			continue
		}
		if u.Scheme == "file" || u.Scheme == "" {
			if u.Path != "" {
				files = append(files, u.Path)
			}
		}
	}

	return FileList{Files: files, Cut: cut}, nil
}
