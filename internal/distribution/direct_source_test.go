package distribution

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func directSourceFixture(t *testing.T, root string) goPackage {
	t.Helper()
	for _, name := range []string{"WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md"} {
		writeFixture(t, filepath.Join(root, directLANPath, name), string(readFixture(t, filepath.Join("..", "directlan", name))))
	}
	writeFixture(t, filepath.Join(root, directLANPath, "stack.go"), "/* SPDX-License-Identifier: MIT\n * Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.\n * Adapted from github.com/tailscale/wireguard-go/tun/netstack/tun.go.\n */\npackage directlan\n")
	writeFixture(t, filepath.Join(root, directLANPath, "bind.go"), "package directlan\n// original local source\n")
	writeFixture(t, filepath.Join(root, "go.mod"), "module "+Project+"\n")
	p := directLANUpstream()
	writeFixture(t, filepath.Join(root, "go.sum"), p.Module+" "+p.Version+" "+p.ModuleSum+"\n"+p.Module+" "+p.Version+"/go.mod "+p.GoModSum+"\n")
	return goPackage{ImportPath: directLANPackage, Dir: filepath.Join(root, directLANPath), Module: &goModule{Path: Project, Main: true, Dir: root}, GoFiles: []string{"stack.go", "bind.go"}, Imports: []string{"fmt"}}
}
func TestDirectSourceInventoryExactProvenance(t *testing.T) {
	root := t.TempDir()
	p := directSourceFixture(t, root)
	share := t.TempDir()
	sources, err := sourceInventory(root, []goPackage{p}, share)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Package != directLANPackage || !reflect.DeepEqual(sources[0].Upstream, directLANUpstream()) {
		t.Fatalf("invalid source: %+v", sources)
	}
	s := sources[0]
	if len(s.Notices) != 4 || len(s.BuildInputs) != 7 {
		t.Fatalf("incomplete inventory: %+v", s)
	}
	for _, n := range s.Notices {
		if bytesHash(readFixture(t, filepath.Join(share, filepath.FromSlash(n.Path)))) != n.SHA256 {
			t.Fatal("notice hash differs")
		}
	}
	for _, n := range s.BuildInputs {
		if bytesHash(readFixture(t, filepath.Join(root, filepath.FromSlash(n.Path)))) != n.SHA256 {
			t.Fatal("input hash differs")
		}
	}
	raw, _ := json.Marshal(s.BuildInputs)
	if bytesHash(raw) != s.BuildInputsSHA256 {
		t.Fatal("input digest differs")
	}
	before := s.BuildInputsSHA256
	writeFixture(t, filepath.Join(root, directLANPath, "bind.go"), "package directlan\n// changed local input\n")
	after, err := sourceInventory(root, []goPackage{p}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if after[0].BuildInputsSHA256 == before || !reflect.DeepEqual(after[0].Upstream, s.Upstream) {
		t.Fatal("adapted bytes confused with original provenance")
	}
	bom, _ := json.Marshal(makeSBOM([]goPackage{p}, NoticeInventory{Sources: after}, "1.2.3", Targets[0], strings.Repeat("a", 40), strings.Repeat("b", 64)))
	if bytes.Contains(bom, []byte(root)) || !bytes.Contains(bom, []byte("source:internal/directlan")) {
		t.Fatal("source identity missing or local path leaked")
	}
}
func TestDirectSourceInventoryFailClosed(t *testing.T) {
	for _, kind := range []string{"license", "provenance", "unknown key", "duplicate key", "missing header", "missing stack", "test input", "replacement", "package path", "module path", "checksum", "duplicate package", "missing description"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			p := directSourceFixture(t, root)
			packages := []goPackage{p}
			switch kind {
			case "license":
				writeFixture(t, filepath.Join(root, directLANPath, "WIREGUARD_LICENSE"), "different terms")
			case "provenance":
				pin := directLANUpstream()
				pin.Files["LICENSE"] = bytesHash([]byte("different terms"))
				writeJSON(filepath.Join(root, directLANPath, "UPSTREAM.json"), pin)
				writeFixture(t, filepath.Join(root, directLANPath, "WIREGUARD_LICENSE"), "different terms")
			case "unknown key", "duplicate key":
				name := filepath.Join(root, directLANPath, "UPSTREAM.json")
				key := `"unknown":true,`
				if kind == "duplicate key" {
					key = `"license":"MIT",`
				}
				writeFixture(t, name, strings.Replace(string(readFixture(t, name)), "{", "{"+key, 1))
			case "missing header":
				writeFixture(t, filepath.Join(root, directLANPath, "stack.go"), "package directlan\n")
			case "missing stack":
				packages[0].GoFiles = []string{"bind.go"}
			case "test input":
				packages[0].GoFiles = append(packages[0].GoFiles, "unselected_test.go")
			case "replacement":
				packages[0].Module.Replace = &goModule{Path: "elsewhere"}
			case "package path":
				packages[0].Dir = root
			case "module path":
				packages[0].Module.Path = "example.org/other"
			case "checksum":
				writeFixture(t, filepath.Join(root, "go.sum"), "wrong sum\n")
			case "duplicate package":
				packages = append(packages, packages[0])
			case "missing description":
				os.Remove(filepath.Join(root, directLANPath, "UPSTREAM.md"))
			}
			if _, err := sourceInventory(root, packages, t.TempDir()); err == nil {
				t.Fatal("accepted invalid direct source")
			}
		})
	}
}
func TestDirectSourceInteriorLinkRejected(t *testing.T) {
	root := t.TempDir()
	p := directSourceFixture(t, root)
	path := filepath.Join(root, directLANPath, "bind.go")
	os.Remove(path)
	if err := os.Symlink(filepath.Join(root, "go.mod"), path); err != nil {
		t.Skip("symlink unavailable")
	}
	if _, err := directLANInventory(root, []goPackage{p}, t.TempDir()); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestBuildRechecksDirectSource(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "changed"}[mutate], func(t *testing.T) {
			tool, _ := mockTool(t)
			p := directSourceFixture(t, tool.Root)
			oldRun := tool.run
			tool.run = func(env []string, args ...string) ([]byte, error) {
				data, err := oldRun(env, args...)
				if err != nil {
					return nil, err
				}
				if args[0] == "list" {
					raw, _ := json.Marshal(p)
					data = append(append(data, '\n'), raw...)
				}
				if args[0] == "build" && mutate {
					writeFixture(t, filepath.Join(tool.Root, directLANPath, "bind.go"), "package directlan\n// changed during build\n")
				}
				return data, nil
			}
			err := tool.Build("1.2.3", Targets[0], strings.Repeat("a", 40), io.Discard)
			if mutate {
				if err == nil || !strings.Contains(err.Error(), "changed during package build") {
					t.Fatalf("accepted mutation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			archive := readArchiveFixture(t, filepath.Join(tool.Root, "dist", stem("1.2.3", Targets[0])+Targets[0].Extension()))
			for _, name := range []string{"WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md", "stack.go"} {
				if len(archive["share/sobalink/licenses/source/"+directLANPath+"/"+name]) == 0 {
					t.Fatal("missing retained direct source notice")
				}
			}
		})
	}
}
