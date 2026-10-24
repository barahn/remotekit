// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

// Win32 drop effect constants for Preferred DropEffect.
const (
	DropEffectNone uint32 = 0
	DropEffectCopy uint32 = 1
	DropEffectMove uint32 = 2
	DropEffectLink uint32 = 4
)

// dropFilesHeaderLength is the size of the DROPFILES structure in bytes.
// DWORD pFiles (4) + POINT pt (8) + BOOL fNC (4) + BOOL fWide (4) = 20 bytes.
const dropFilesHeaderLength = 20

var (
	errDropFilesTooShort = errors.New("clipboard: DROPFILES data shorter than header")
	errDropFilesCorrupt  = errors.New("clipboard: DROPFILES data corrupted or missing null terminator")
)

// EncodeDropFiles encodes a FileList into the Win32 CF_HDROP format (DROPFILES structure
// followed by null-terminated UTF-16LE paths and a final double-null terminator).
// It returns the serialized bytes and the corresponding Preferred DropEffect (1 for copy, 2 for move/cut).
func EncodeDropFiles(list FileList) ([]byte, uint32, error) {
	dropEffect := DropEffectCopy
	if list.Cut {
		dropEffect = DropEffectMove
	}

	buf := new(bytes.Buffer)

	// DROPFILES structure:
	// DWORD pFiles: offset of the file list from beginning of structure (20)
	if err := binary.Write(buf, binary.LittleEndian, uint32(dropFilesHeaderLength)); err != nil {
		return nil, 0, err
	}
	// POINT pt: drop point (x=0, y=0)
	if err := binary.Write(buf, binary.LittleEndian, int32(0)); err != nil {
		return nil, 0, err
	}
	if err := binary.Write(buf, binary.LittleEndian, int32(0)); err != nil {
		return nil, 0, err
	}
	// BOOL fNC: non-client area (0)
	if err := binary.Write(buf, binary.LittleEndian, uint32(0)); err != nil {
		return nil, 0, err
	}
	// BOOL fWide: TRUE (1) indicating wide characters (UTF-16LE)
	if err := binary.Write(buf, binary.LittleEndian, uint32(1)); err != nil {
		return nil, 0, err
	}

	// Write each path as null-terminated UTF-16LE
	for _, f := range list.Files {
		encoded := utf16.Encode([]rune(f))
		for _, u := range encoded {
			if err := binary.Write(buf, binary.LittleEndian, u); err != nil {
				return nil, 0, err
			}
		}
		// Null terminator for this path
		if err := binary.Write(buf, binary.LittleEndian, uint16(0)); err != nil {
			return nil, 0, err
		}
	}

	// Final double null terminator
	if err := binary.Write(buf, binary.LittleEndian, uint16(0)); err != nil {
		return nil, 0, err
	}

	return buf.Bytes(), dropEffect, nil
}

// DecodeDropFiles decodes a Win32 CF_HDROP byte stream into a FileList.
func DecodeDropFiles(data []byte, dropEffect uint32) (FileList, error) {
	if len(data) < dropFilesHeaderLength {
		return FileList{}, errDropFilesTooShort
	}

	pFiles := binary.LittleEndian.Uint32(data[0:4])
	fWide := binary.LittleEndian.Uint32(data[16:20])

	if int(pFiles) > len(data) {
		return FileList{}, errDropFilesCorrupt
	}

	pathsData := data[pFiles:]
	var files []string

	if fWide != 0 {
		// UTF-16LE paths
		if len(pathsData)%2 != 0 {
			pathsData = pathsData[:len(pathsData)-1]
		}
		var currentRunes []uint16
		for i := 0; i+1 < len(pathsData); i += 2 {
			val := binary.LittleEndian.Uint16(pathsData[i : i+2])
			if val == 0 {
				if len(currentRunes) == 0 {
					// Double null encountered, end of list
					break
				}
				files = append(files, string(utf16.Decode(currentRunes)))
				currentRunes = nil
			} else {
				currentRunes = append(currentRunes, val)
			}
		}
	} else {
		// ANSI / ASCII paths
		var currentBytes []byte
		for _, b := range pathsData {
			if b == 0 {
				if len(currentBytes) == 0 {
					// Double null encountered
					break
				}
				files = append(files, string(currentBytes))
				currentBytes = nil
			} else {
				currentBytes = append(currentBytes, b)
			}
		}
	}

	cut := (dropEffect == DropEffectMove)
	return FileList{Files: files, Cut: cut}, nil
}
