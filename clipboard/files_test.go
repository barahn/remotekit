// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

package clipboard

import (
	"context"
	"reflect"
	"testing"
)

func TestEncodeDecodeDropFiles(t *testing.T) {
	tests := []struct {
		name       string
		files      []string
		cut        bool
		dropEffect uint32
	}{
		{
			name:       "single file copy",
			files:      []string{`C:\Users\Tester\Documents\report.pdf`},
			cut:        false,
			dropEffect: DropEffectCopy,
		},
		{
			name:       "multiple files cut",
			files:      []string{`C:\Users\Tester\file1.txt`, `D:\Data\subfolder\file 2 with spaces.png`},
			cut:        true,
			dropEffect: DropEffectMove,
		},
		{
			name:       "unicode characters in path",
			files:      []string{`C:\Arquivos\relatório_2026_São_Paulo.xlsx`, `C:\Users\테스트\파일.txt`},
			cut:        false,
			dropEffect: DropEffectCopy,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := FileList{Files: tc.files, Cut: tc.cut}
			data, de, err := EncodeDropFiles(input)
			if err != nil {
				t.Fatalf("EncodeDropFiles error: %v", err)
			}
			if de != tc.dropEffect {
				t.Errorf("dropEffect = %d, want %d", de, tc.dropEffect)
			}

			decoded, err := DecodeDropFiles(data, de)
			if err != nil {
				t.Fatalf("DecodeDropFiles error: %v", err)
			}
			if !reflect.DeepEqual(decoded.Files, tc.files) {
				t.Errorf("decoded files = %v, want %v", decoded.Files, tc.files)
			}
			if decoded.Cut != tc.cut {
				t.Errorf("decoded Cut = %v, want %v", decoded.Cut, tc.cut)
			}
		})
	}
}

func TestDecodeDropFiles_Malformed(t *testing.T) {
	// Too short for DROPFILES header (less than 20 bytes)
	_, err := DecodeDropFiles([]byte{0, 1, 2}, DropEffectCopy)
	if err == nil {
		t.Error("expected error for data shorter than DROPFILES header")
	}

	// Empty data
	_, err = DecodeDropFiles(nil, DropEffectCopy)
	if err == nil {
		t.Error("expected error for nil data")
	}
}

func TestEncodeDecodeURIList(t *testing.T) {
	files := []string{
		"/home/user/document.pdf",
		"/home/user/My Photos/summer vacation 2026.jpg",
		"/tmp/test_file.txt",
	}

	encoded := EncodeURIList(files)
	decoded := DecodeURIList(encoded)

	if !reflect.DeepEqual(decoded, files) {
		t.Errorf("DecodeURIList = %v, want %v", decoded, files)
	}

	// Handle comments and empty lines
	withComments := "# This is a comment\r\n" + encoded + "\r\n# Another comment\r\n"
	decodedWithComments := DecodeURIList(withComments)
	if !reflect.DeepEqual(decodedWithComments, files) {
		t.Errorf("DecodeURIList with comments = %v, want %v", decodedWithComments, files)
	}
}

func TestEncodeDecodeGnomeCopiedFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		cut   bool
	}{
		{
			name:  "copy operation",
			files: []string{"/home/user/notes.txt", "/var/log/syslog"},
			cut:   false,
		},
		{
			name:  "cut operation",
			files: []string{"/home/user/Downloads/archive.tar.gz"},
			cut:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := FileList{Files: tc.files, Cut: tc.cut}
			encoded := EncodeGnomeCopiedFiles(input)
			decoded, err := DecodeGnomeCopiedFiles(encoded)
			if err != nil {
				t.Fatalf("DecodeGnomeCopiedFiles error: %v", err)
			}
			if !reflect.DeepEqual(decoded.Files, tc.files) {
				t.Errorf("decoded files = %v, want %v", decoded.Files, tc.files)
			}
			if decoded.Cut != tc.cut {
				t.Errorf("decoded Cut = %v, want %v", decoded.Cut, tc.cut)
			}
		})
	}
}

func TestMemoryClipboard_FileManager(t *testing.T) {
	mc := NewMemoryClipboard()
	ctx := context.Background()

	// Initial files should be empty
	fl, err := mc.GetFiles(ctx)
	if err != nil {
		t.Fatalf("GetFiles: %v", err)
	}
	if len(fl.Files) != 0 {
		t.Errorf("initial files = %v, want empty", fl.Files)
	}

	// Set files
	want := FileList{Files: []string{"/tmp/test1.txt", "/tmp/test2.txt"}, Cut: true}
	if err := mc.SetFiles(ctx, want); err != nil {
		t.Fatalf("SetFiles: %v", err)
	}

	got, err := mc.GetFiles(ctx)
	if err != nil {
		t.Fatalf("GetFiles after set: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got files = %v, want %v", got, want)
	}

	// Concealed files
	if err := mc.SetConcealedFiles(ctx, want); err != nil {
		t.Fatalf("SetConcealedFiles: %v", err)
	}
	concealed, err := mc.Concealed(ctx)
	if err != nil {
		t.Fatalf("Concealed: %v", err)
	}
	if !concealed {
		t.Error("expected Concealed to be true after SetConcealedFiles")
	}
}
