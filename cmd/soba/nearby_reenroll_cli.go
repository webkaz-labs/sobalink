package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

type nearbyRevokedRecord struct {
	PeerKey      string `json:"peerKey"`
	Generation   uint64 `json:"generation"`
	Verification string `json:"verification"`
}
type nearbyRevokedList struct {
	Kind    string                `json:"kind"`
	Version int                   `json:"version"`
	Records []nearbyRevokedRecord `json:"records"`
}
type nearbyReenrollReceipt struct {
	Kind         string `json:"kind"`
	Version      int    `json:"version"`
	PeerKey      string `json:"peerKey"`
	Generation   uint64 `json:"generation"`
	Verification string `json:"verification"`
	Paired       *bool  `json:"paired"`
	Trusted      *bool  `json:"trusted"`
}

func nearbyValidRevokedRecord(r nearbyRevokedRecord) bool {
	return nearbyHex(r.PeerKey) && r.Generation > 0 && r.Generation <= 9007199254740991 && (nearbyV2Verification(r.Verification) || r.Verification == "unknown")
}
func (c *nearbyCLI) revokedKeys() (nearbyRevokedList, json.RawMessage, error) {
	raw, err := c.call("revoked", map[string]any{})
	if err != nil {
		return nearbyRevokedList{}, nil, err
	}
	var list nearbyRevokedList
	if len(raw) > 256<<10 || json.Unmarshal(raw, &list) != nil || list.Kind != "sobalink-setup-revoked" || list.Version != 1 || list.Records == nil || len(list.Records) > 4096 {
		return list, nil, nearbyError(c.ja, "setup_invalid_response")
	}
	seen := map[string]bool{}
	for _, r := range list.Records {
		if !nearbyValidRevokedRecord(r) || seen[r.PeerKey] {
			return list, nil, nearbyError(c.ja, "setup_invalid_response")
		}
		seen[r.PeerKey] = true
	}
	return list, raw, nil
}
func (c *nearbyCLI) reenrollKey(key string, generation uint64) (nearbyReenrollReceipt, json.RawMessage, error) {
	var receipt nearbyReenrollReceipt
	raw, err := c.call("reenroll", map[string]any{"peerKey": key, "expectedGeneration": generation, "consent": true})
	if err != nil {
		return receipt, nil, err
	}
	if len(raw) > 16<<10 || json.Unmarshal(raw, &receipt) != nil || receipt.Kind != "sobalink-setup-reenroll" || receipt.Version != 1 || receipt.PeerKey != key || receipt.Generation != generation+1 || !nearbyValidRevokedRecord(nearbyRevokedRecord{receipt.PeerKey, receipt.Generation, receipt.Verification}) || receipt.Paired == nil || *receipt.Paired || receipt.Trusted == nil || *receipt.Trusted {
		return receipt, nil, nearbyError(c.ja, "setup_invalid_response")
	}
	return receipt, raw, nil
}
func (c *nearbyCLI) reenrollUncertain(err error) error {
	if !c.machine {
		fmt.Fprintln(c.out, text(c.ja, "Do not repeat re-enrollment blindly. Read nearby revoked again; disappearance alone does not prove which operation completed. No new pair or application permission is implied.", "再登録の許可を確認せず繰り返さないでください。nearby revokedで再照会します。一覧から消えただけではどの操作が完了したか断定できません。ペアやアプリの権限が追加されたという意味ではありません。"))
	}
	return err
}
func (c *nearbyCLI) revocationCommand(action string, _ any, key string, generation uint64) error {
	if action == "revoked" {
		list, raw, err := c.revokedKeys()
		if err != nil {
			return err
		}
		if c.machine {
			return nearbyWriteJSON(c.out, raw)
		}
		c.writeRevokedKeys(list)
		return nil
	}
	_, raw, err := c.reenrollKey(key, generation)
	if err != nil {
		return c.reenrollUncertain(err)
	}
	if c.machine {
		return nearbyWriteJSON(c.out, raw)
	}
	return c.writeReenrolled()
}
func (c *nearbyCLI) writeRevokedKeys(list nearbyRevokedList) {
	fmt.Fprintln(c.out, text(c.ja, "Removed keys. Re-enrollment requires an offline profile with no active setup or saved transport pair. It only permits a fresh request; application permissions stay removed.", "解除した鍵です。再登録の許可にはオフライン状態で、進行中の接続設定や保存された通信ペアがないことが必要です。新しい依頼を可能にするだけで、アプリの権限は復元しません。"))
	for i, r := range list.Records {
		fmt.Fprintf(c.out, "%d  %s | %s: %d | %s: %s\n", i+1, r.PeerKey, text(c.ja, "Generation", "世代"), r.Generation, text(c.ja, "Previous verification", "以前の確認方式"), r.Verification)
	}
	if len(list.Records) == 0 {
		fmt.Fprintln(c.out, text(c.ja, "No removed keys are listed.", "解除した鍵は一覧にありません。"))
	}
}
func (c *nearbyCLI) writeReenrolled() error {
	_, err := fmt.Fprintln(c.out, text(c.ja, "This key may now make a fresh pairing request. It is not paired or trusted. Start a new request with fresh consent; any previous full verification requirement remains.", "この鍵を新しい依頼で再登録できるようになりました。まだペアでも信頼済みでもありません。改めて許可して新しく依頼してください。以前の全文照合の要件は維持されます。"))
	return err
}
func (c *nearbyCLI) reenrollWizard(p *prompts) error {
	list, _, err := c.revokedKeys()
	if err != nil {
		return err
	}
	c.writeRevokedKeys(list)
	if len(list.Records) == 0 {
		return nil
	}
	answer, err := p.ask(text(c.ja, "Removed key number to review (q returns): ", "確認する解除済みの鍵の番号（qで戻る）: "))
	if errors.Is(err, errPromptCanceled) {
		return nil
	}
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(list.Records) {
		return nearbyError(c.ja, "setup_invalid")
	}
	r := list.Records[n-1]
	fmt.Fprintf(c.out, "%s\n%s: %d\n", r.PeerKey, text(c.ja, "Exact generation", "正確な世代"), r.Generation)
	if err = p.confirm(text(c.ja, "Permit a fresh request for this exact removed key; do not restore old pairing or application permissions", "この解除済みの正確な鍵の新規依頼を許可する。以前のペアやアプリ権限は復元しない"), false); errors.Is(err, errPromptCanceled) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, _, err = c.reenrollKey(r.PeerKey, r.Generation); err != nil {
		return c.reenrollUncertain(err)
	}
	return c.writeReenrolled()
}
