package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/webkaz-labs/sobalink/internal/core"
)

func networkCLI(args []string, dir string, ja, dryRun bool, out io.Writer, query commandQuery, request func(string, any) error) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, text(ja, "Usage: soba network resume [--json] [--expected-revision REVISION]\nWithout a revision, review the saved connection without starting it. Apply only after reviewing the exact settings and permissions. A running backend is never restarted.", "使い方: soba network resume [--json] [--expected-revision REVISION]\nリビジョンの指定がなければ、接続を開始せず保存済み設定を確認します。正確な設定と許可を確認してから適用してください。稼働中の接続は再起動しません。"))
		return nil
	}
	if args[0] != "resume" {
		return usageError(ja, "network resume [--json] [--expected-revision REVISION]")
	}
	f := commandFlags("network resume", ja, out)
	revision := f.String("expected-revision", "", text(ja, "apply only the exact reviewed saved connection", "確認済みの正確な保存済み接続だけを開始"))
	asJSON := f.Bool("json", false, "JSON")
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	if *revision != "" {
		return request("network.resume", map[string]string{"expectedRevision": *revision})
	}
	if dryRun {
		return request("network.resume.preview", map[string]any{})
	}
	var review core.NetworkResumeReview
	if err := query("network.resume.preview", map[string]any{}, &review); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(review)
	}
	fmt.Fprintln(out, text(ja, "Review saved connection — not started", "保存済み接続の確認 — 未開始"))
	fmt.Fprintf(out, "%s: %s / %s\n", text(ja, "Network / name", "接続方式 / 名前"), review.Mode, review.Hostname)
	fmt.Fprintf(out, "%s: %v\n", text(ja, "Backends", "接続方式"), review.Backends)
	fmt.Fprintf(out, "%s: %d / %d\n", text(ja, "Trusted devices / paired records", "信頼済み相手 / ペア記録"), review.TrustedDevices, review.PairedDevices)
	if review.ExternalControlTraffic {
		fmt.Fprintln(out, text(ja, "The saved selection permits Tailnet external control traffic.", "保存済みの選択により、Tailnet の外部制御通信を開始します。"))
	}
	if review.LAN != nil {
		fmt.Fprintf(out, "%s: %s %s\n", text(ja, "Relay", "中継"), review.LAN.Relay.Kind, review.LAN.Relay.Address)
		fmt.Fprintf(out, "%s: %s\n", text(ja, "Certificate fingerprint", "証明書の指紋"), review.LAN.Relay.CertificateSHA256)
		fmt.Fprintf(out, "%s: %s %v\n", text(ja, "Destination policy", "接続先ポリシー"), review.LAN.Policy.Mode, review.LAN.Policy.Prefixes)
		for _, relay := range review.LAN.PreparedRelays {
			fmt.Fprintf(out, "%s: %s %s\n", text(ja, "Prepared relay / fingerprint", "準備済みの中継 / 指紋"), relay.Address, relay.CertificateSHA256)
		}
		if review.LAN.WANCandidates != nil {
			b, _ := json.Marshal(review.LAN.WANCandidates)
			fmt.Fprintf(out, "%s: %s\n", text(ja, "WAN candidates", "WAN 候補"), b)
		}
	}
	if review.DirectLAN != nil {
		fmt.Fprintf(out, "%s: %s %v\n", text(ja, "Direct LAN endpoint / prefixes", "直接LANの端点 / 許可範囲"), review.DirectLAN.Listen, review.DirectLAN.Prefixes)
	}
	fmt.Fprintln(out, text(ja, "Existing peer permissions remain in effect. Saved automatic receiving may become available after network readiness:", "相手への既存の許可は有効なままです。接続の準備後、保存済みの自動受信が利用可能になります:"))
	for _, peer := range review.AutomaticReceivers {
		fmt.Fprintf(out, "  %s (%s): %s\n", peer.Name, peer.PeerID, peer.Directory)
	}
	fmt.Fprintf(out, "%s: %t / %t\n", text(ja, "Startup / saved proxies remain suppressed", "起動時の接続 / 保存済みプロキシの抑止を維持"), review.StartupSuppressed, review.SavedProxiesSuppressed)
	if len(review.PendingStartup) > 0 {
		fmt.Fprintf(out, "%s: %v\n", text(ja, "Previously approved pending startup", "許可済みの起動待ち"), review.PendingStartup)
	}
	fmt.Fprintln(out, text(ja, "Does not renew grants, resume file transfers or confirm peer/application readiness. Cancel by not applying.", "許可の延長、ファイル転送の再開、相手やアプリの準備確認は行いません。取り消す場合は適用しないでください。"))
	fmt.Fprintln(out, cliCommandExample(dir, "network", "resume", "--expected-revision", review.Revision))
	return nil
}
