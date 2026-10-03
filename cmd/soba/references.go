package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

type resolvedServices struct {
	IDs   []string
	Names map[string]string
}

func resolveServiceReferences(refs []string, ja bool, query commandQuery) (resolvedServices, error) {
	resolved := resolvedServices{Names: map[string]string{}}
	if len(refs) == 0 {
		return resolved, nil
	}
	var exported struct {
		Profile core.DefinitionBundle `json:"profile"`
	}
	if err := query("profile.export", map[string]any{}, &exported); err != nil {
		return resolved, err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		var matches []core.ServiceSpec
		for _, service := range exported.Profile.Services {
			if service.ID == ref || service.Name == ref {
				matches = append(matches, service)
			}
		}
		if len(matches) > 1 {
			return resolved, fmt.Errorf("%s: %s", text(ja, "Ambiguous saved service name or ID; use its exact unique ID from rules", "保存名またはIDが複数に一致します。rules で確認した一意のIDを指定してください"), displayText(ref))
		}
		id := ref
		if len(matches) == 1 {
			id = matches[0].ID
			if ref == matches[0].Name {
				resolved.Names[id] = matches[0].Name
			}
		} else if !config.ValidPeerID(ref) {
			return resolved, fmt.Errorf("%s: %s", text(ja, "Saved service not found; use rules to review its exact name or ID", "保存済みサービスがありません。rules で正確な名前かIDを確認してください"), displayText(ref))
		}
		// An explicit ID remains valid input even when another read has removed it;
		// the authoritative revision-checked Core operation decides availability.
		if !config.ValidPeerID(id) || seen[id] {
			return resolved, errors.New(text(ja, "Select distinct saved services; a name and its ID refer to the same service", "保存済みサービスを重複なく選んでください。名前とIDは同じサービスを指します"))
		}
		seen[id] = true
		resolved.IDs = append(resolved.IDs, id)
	}
	return resolved, nil
}
func checkResolvedName(resolved resolvedServices, id, name string, ja bool) error {
	if expected, ok := resolved.Names[id]; ok && expected != name {
		return errors.New(text(ja, "The selected saved name changed; review rules before retrying", "選択した保存名が変わりました。rules で確認してから再実行してください"))
	}
	return nil
}

// Peer names are explicit selectors, kept separate from --peer/--peers so an
// offline ID-only preview never guesses a network identity or requires a query.
func resolveNamedPeerPayload(payload map[string]any, ja bool, query stateQuery) error {
	raw, one := payload["peerName"].(string)
	many, more := payload["peerNames"].([]string)
	if !one && !more {
		return nil
	}
	refs := many
	if one {
		refs = []string{raw}
	}
	var snapshot guidedSnapshot
	if err := query(&snapshot); err != nil {
		return fmt.Errorf("%s: %w", text(ja, "Named peer selection needs the running network; use --peer/--peers IDs for offline definitions", "相手を名前で選ぶにはネットワークの起動が必要です。オフラインの定義には --peer/--peers のIDを使ってください"), err)
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		var matches []guidedPeer
		for _, peer := range snapshot.Peers {
			if peer.Verified && peer.Name == ref {
				matches = append(matches, peer)
			}
		}
		if len(matches) != 1 || !config.ValidPeerID(matches[0].ID) || seen[matches[0].ID] {
			return fmt.Errorf("%s: %s", text(ja, "Peer name is missing, ambiguous or repeated; use peers and select an exact current name or explicit ID", "相手の名前が存在しない・一意でない・重複しています。peers で現在の名前か明示的なIDを確認してください"), displayText(ref))
		}
		ids = append(ids, matches[0].ID)
		seen[matches[0].ID] = true
	}
	delete(payload, "peerName")
	delete(payload, "peerNames")
	if one {
		payload["peerId"] = ids[0]
	} else {
		payload["peerIds"] = ids
	}
	return nil
}
func splitPeerNames(value string) []string {
	values := strings.Split(value, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}
