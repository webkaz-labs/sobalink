package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const directLANPath = "internal/directlan"
const directLANPackage = Project + "/" + directLANPath

// The original source, license and module sums were independently verified
// against the original versioned Go module zip. These pins are independent of
// the retained provenance file and never describe our modified stack.go bytes.
func directLANUpstream() SourceProvenance {
	return SourceProvenance{
		Module: "github.com/tailscale/wireguard-go", Version: "v0.0.0-20260928213032-417aef361226",
		Commit: "417aef361226c869ab29e15fe3539b01173c4719", SourceURL: "https://github.com/tailscale/wireguard-go/blob/417aef361226c869ab29e15fe3539b01173c4719/tun/netstack/tun.go", License: "MIT",
		ModuleSum: "h1:v3Lpj2iHPWQDqeCwemQPz4fWweIEMLqBkwJqCjRyJQc=", GoModSum: "h1:rUelGmuK4UnSJYM5gl5Mknp6YbwwcL8+VAPMhNYe+jg=",
		Files: map[string]string{"tun/netstack/tun.go": "dc8bdff07b29630c2e0867c0b2b56d6e4de1035049be89c5979c6cffe4b7623b", "LICENSE": "91276db973f25602d1aa43491f59cbc84cb88e6f151e1d0cc82a755563ce0195"},
	}
}

func sourceInventory(root string, packages []goPackage, share string) ([]SourceComponent, error) {
	sources, err := routecatInventory(root, packages, share)
	if err != nil {
		return nil, err
	}
	direct, err := directLANInventory(root, packages, share)
	if err != nil {
		return nil, err
	}
	return append(sources, direct...), nil
}

func directLANInventory(root string, packages []goPackage, share string) ([]SourceComponent, error) {
	var linked *goPackage
	for i := range packages {
		if packages[i].ImportPath == directLANPackage {
			if linked != nil {
				return nil, errors.New("duplicate direct LAN source package")
			}
			linked = &packages[i]
		}
	}
	if linked == nil {
		return nil, nil
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if linked.Module == nil || !linked.Module.Main || linked.Module.Replace != nil || linked.Module.Path != Project || filepath.Clean(linked.Module.Dir) != root || filepath.Clean(linked.Dir) != filepath.Join(root, filepath.FromSlash(directLANPath)) {
		return nil, errors.New("direct LAN source is not from the expected main-module directory")
	}
	raw, err := readSourceFile(root, directLANPath+"/UPSTREAM.json")
	if err != nil {
		return nil, err
	}
	fields, err := uniqueSourceObject(raw)
	if err != nil {
		return nil, err
	}
	for key := range fields {
		switch key {
		case "module", "version", "commit", "source_url", "license", "files", "module_sum", "go_mod_sum":
		default:
			return nil, fmt.Errorf("unknown direct LAN provenance key: %s", key)
		}
	}
	if _, err := uniqueSourceObject(fields["files"]); err != nil {
		return nil, err
	}
	var upstream SourceProvenance
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(upstream, directLANUpstream()) {
		return nil, errors.New("direct LAN provenance differs from reviewed original source pin")
	}
	inputs := map[string][]byte{directLANPath + "/UPSTREAM.json": raw}
	selected := map[string]bool{}
	for _, list := range [][]string{linked.GoFiles, linked.CgoFiles, linked.CFiles, linked.CXXFiles, linked.MFiles, linked.HFiles, linked.FFiles, linked.SFiles, linked.SwigFiles, linked.SwigCXXFiles, linked.SysoFiles, linked.EmbedFiles} {
		for _, name := range list {
			if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || strings.HasSuffix(name, "_test.go") {
				return nil, errors.New("invalid direct LAN runtime build input")
			}
			rel := directLANPath + "/" + name
			data, err := readSourceFile(root, rel)
			if err != nil {
				return nil, err
			}
			inputs[rel] = data
			selected[name] = true
		}
	}
	if !selected["stack.go"] {
		return nil, errors.New("direct LAN build inventory is missing adapted stack.go")
	}
	header := []byte("/* SPDX-License-Identifier: MIT\n * Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.\n * Adapted from github.com/tailscale/wireguard-go/tun/netstack/tun.go.\n")
	if !bytes.HasPrefix(inputs[directLANPath+"/stack.go"], header) {
		return nil, errors.New("direct LAN stack is missing its original copyright and license notice")
	}
	for _, rel := range []string{"go.mod", "go.sum", directLANPath + "/WIREGUARD_LICENSE", directLANPath + "/UPSTREAM.md"} {
		data, err := readSourceFile(root, rel)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("empty direct LAN source input: %s", rel)
		}
		inputs[rel] = data
	}
	if bytesHash(inputs[directLANPath+"/WIREGUARD_LICENSE"]) != upstream.Files["LICENSE"] {
		return nil, errors.New("direct LAN license differs from original MIT license")
	}
	for _, pin := range []struct{ version, sum string }{{upstream.Version, upstream.ModuleSum}, {upstream.Version + "/go.mod", upstream.GoModSum}} {
		count := 0
		for _, line := range strings.Split(string(inputs["go.sum"]), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] == upstream.Module && parts[1] == pin.version {
				if len(parts) != 3 || parts[2] != pin.sum {
					return nil, errors.New("direct LAN original module checksum changed")
				}
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("direct LAN original module checksum missing or duplicated")
		}
	}
	component := SourceComponent{Package: directLANPackage, Path: directLANPath, Modification: "Adapted main-module userspace WireGuard stack source, not the unmodified upstream tun/netstack package. Original source and MIT license hashes are recorded separately from all selected local runtime inputs. Other direct LAN source is project code with its own notices.", Upstream: upstream}
	names := make([]string, 0, len(inputs))
	for rel := range inputs {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		component.BuildInputs = append(component.BuildInputs, Notice{rel, bytesHash(inputs[rel])})
	}
	encoded, err := json.Marshal(component.BuildInputs)
	if err != nil {
		return nil, err
	}
	component.BuildInputsSHA256 = bytesHash(encoded)
	for _, name := range []string{"WIREGUARD_LICENSE", "UPSTREAM.json", "UPSTREAM.md", "stack.go"} {
		rel := directLANPath + "/" + name
		destination := "licenses/source/" + rel
		if err := copyFile(filepath.Join(root, filepath.FromSlash(rel)), filepath.Join(share, filepath.FromSlash(destination))); err != nil {
			return nil, err
		}
		component.Notices = append(component.Notices, Notice{destination, bytesHash(inputs[rel])})
	}
	return []SourceComponent{component}, nil
}
