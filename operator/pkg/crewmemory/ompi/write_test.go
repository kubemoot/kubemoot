/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestFilesAddsMarkdownOnlyWhenAsked(t *testing.T) {
	exp := buildExport(t, fact("hardware", "gpu0", "RTX 5090"))
	plain, err := Files(exp, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 2 || plain[ManifestFile] == nil || plain[MemoriesDir+"/"+exp.Memories[0].Identifier+".json"] == nil {
		t.Errorf("files = %v", keysOf(plain))
	}
	withMD, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	if withMD[MarkdownDir+"/hardware/gpu0.md"] == nil || len(withMD) != 3 {
		t.Errorf("files = %v", keysOf(withMD))
	}
}

func TestProjectionKeepsRuntimeFieldsOnly(t *testing.T) {
	m := buildExport(t, fact("hardware", "gpu0", "RTX 5090, 32 GB")).Memories[0]
	md, err := Projection(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"---\n", "identifier: " + m.Identifier, "path: hardware/gpu0", "title: hardware / gpu0", "media_type: text/plain", "version: 41", "id: gpu-analyst", "---\n\nRTX 5090, 32 GB\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("projection lacks %q:\n%s", want, md)
		}
	}
	for _, absent := range []string{"scope:", "crew:", "checksum", "x-kubemoot", "usedAt", "evidenceUnavailable"} {
		if strings.Contains(md, absent) {
			t.Errorf("projection carries %q (section 7.2):\n%s", absent, md)
		}
	}
}

func TestProjectionFrontMatterSurvivesHostileTitles(t *testing.T) {
	m := buildExport(t, fact("t\n---\nevil: yes", "k: [x]\n# c", "body\x00 text")).Memories[0]
	md, err := Projection(m)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(md, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("front matter not delimited:\n%s", md)
	}
	var front map[string]any
	if err := yaml.Unmarshal([]byte(parts[1]), &front); err != nil {
		t.Fatalf("front matter is not YAML: %v\n%s", err, parts[1])
	}
	if _, injected := front["evil"]; injected {
		t.Error("a hostile title injected a front matter key")
	}
	if front["identifier"] != m.Identifier || strings.Contains(md, "\x00") {
		t.Errorf("front matter = %v, NUL kept = %v", front, strings.Contains(md, "\x00"))
	}
}

func TestPathsStayInsideTheExport(t *testing.T) {
	exp := buildExport(t, fact("../../etc", "passwd", "x"), fact("/abs", "..", "y"), fact("a\\b", "c/d", "z"), fact("", "", "empty"))
	files, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	if err := Write(dir, files); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(filepath.Dir(dir), func(p string, _ os.DirEntry, werr error) error {
		if werr == nil && !strings.HasPrefix(p, filepath.Dir(dir)) {
			t.Errorf("file outside the export: %s", p)
		}
		return werr
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Errorf("something was written beside the export directory: %v", entries)
	}
}

func TestMarkdownPathsAreUniqueIgnoringCase(t *testing.T) {
	exp := buildExport(t, fact("t", "Foo", "upper"), fact("T", "foo", "lower"), fact("t", "FOO", "caps"))
	lower := map[string]bool{}
	for _, m := range exp.Memories {
		p := strings.ToLower(m.Path)
		if lower[p] {
			t.Errorf("path %q collides ignoring case", m.Path)
		}
		lower[p] = true
	}
	if len(exp.Memories) != 3 {
		t.Errorf("memories = %d, want 3", len(exp.Memories))
	}
}

func TestWriteChecksPathsBeforeCreatingTheDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "m")
	if err := Write(out, map[string][]byte{ManifestFile: []byte("{}"), "../x.json": []byte("y")}); err == nil {
		t.Fatal("Write() = nil, want error")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("export directory created for a rejected set of files")
	}
}

func TestWriteLeavesNothingBehindWhenAFileCannotBeWritten(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "m")
	files := map[string][]byte{ManifestFile: []byte("{}"), strings.Repeat("n", 300) + ".json": []byte("x")}
	if err := Write(out, files); err == nil {
		t.Fatal("Write() = nil, want a file name too long error")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Errorf("parent holds %v (err %v), want nothing: no partial export, no staging directory", entries, err)
	}
}

func TestWriteIntoAnExistingEmptyDirectory(t *testing.T) {
	out := t.TempDir()
	if err := Write(out, map[string][]byte{ManifestFile: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, ManifestFile)); err != nil {
		t.Error(err)
	}
}

func TestWriteRefusesUnsafePaths(t *testing.T) {
	for _, rel := range []string{"", "/etc/x", "a/../b", `a\b`} {
		if err := Write(filepath.Join(t.TempDir(), "m"), map[string][]byte{rel: nil}); err == nil {
			t.Errorf("Write() accepted %q", rel)
		}
	}
}

func TestWriteRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, map[string][]byte{ManifestFile: []byte("{}")}); err == nil {
		t.Error("Write() = nil, want a non-empty directory error")
	}
}

func TestWrittenFilesRevalidateFromDisk(t *testing.T) {
	exp := buildExport(t, fact("hardware", "gpu0", "RTX 5090"), fact("net", "vlan", "50"))
	files, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	if err := Write(dir, files); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Errorf("manifest on disk: %v", err)
	}
	for _, m := range exp.Memories {
		data, err := os.ReadFile(filepath.Join(dir, MemoriesDir, m.Identifier+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateMemory(data); err != nil {
			t.Errorf("%s on disk: %v", m.Identifier, err)
		}
	}
}
