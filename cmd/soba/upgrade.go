package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func managedUpgradeCLI(ctx context.Context, args []string, dir, locale string, ja, dryRun bool, out io.Writer, client controlCaller) error {
	if len(args) > 0 && (args[0] == "status" || args[0] == "cancel") {
		if len(args) != 1 {
			return usageError(ja, "direct-lan upgrade status|cancel")
		}
		command := webui.Command{RequestID: newCLIRequestID("upgrade"), Name: "direct-lan.upgrade." + args[0], Payload: json.RawMessage(`{}`)}
		if dryRun {
			return json.NewEncoder(out).Encode(command)
		}
		raw, err := json.Marshal(command)
		if err != nil {
			return err
		}
		var progress core.UpgradeProgress
		if err := client(ctx, dir, string(raw), &progress); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(progress)
	}
	flags := commandFlags("direct-lan upgrade", ja, out)
	peer := flags.String("peer", "", text(ja, "exact paired identity to upgrade", "更新するペアの正確なID"))
	deadline := flags.String("deadline", "", text(ja, "original RFC3339 review expiry (at most 5 minutes)", "元の確認期限（RFC3339、最大5分）"))
	apply := flags.Bool("apply", false, text(ja, "apply this exact review; management may reopen with a fresh sign-in", "確認した内容を適用（管理画面が再起動し、再ログインが必要な場合があります）"))
	revision := flags.String("review", "", text(ja, "exact revision from the review", "確認結果の正確なrevision"))
	if err := parseFlags(flags, args, ja); err != nil {
		return err
	}
	if *peer == "" || *apply && (*revision == "" || *deadline == "") || !*apply && *revision != "" {
		return usageError(ja, "direct-lan upgrade --peer ID [--deadline RFC3339] [--apply --review REVISION]")
	}
	if *deadline == "" {
		*deadline = time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second).Format(time.RFC3339)
	}
	expires, err := time.Parse(time.RFC3339, *deadline)
	if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(5*time.Minute)) {
		return errors.New(text(ja, "Review deadline must be in the next five minutes; expired reviews require a new review", "確認期限は今から5分以内にしてください。期限切れの場合は再確認が必要です"))
	}
	intent := core.UpgradeIntent{PeerID: *peer, Deadline: *deadline, ExpectedRevision: *revision}
	if dryRun {
		name := "direct-lan.upgrade.review"
		if *apply {
			name = "direct-lan.upgrade.run"
		}
		return json.NewEncoder(out).Encode(map[string]any{"name": name, "payload": intent})
	}
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	commandClient := client
	var pinned upgradeIdentity
	query := func(name string, payload any, result any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		command, err := json.Marshal(webui.Command{RequestID: newCLIRequestID("upgrade"), Name: name, Payload: raw})
		if err != nil {
			return err
		}
		return commandClient(ctx, dir, string(command), result)
	}
	if !*apply {
		var review core.UpgradeReview
		if err := query("direct-lan.upgrade.review", intent, &review); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(review)
	}
	// Refuse before mutation, so fresh UI codes can never land in detached logs
	// or redirected machine output. There is no implicit browser-session transfer.
	if !loginPrivateTerminal(out) {
		return errors.New(text(ja, "Apply requires a private terminal for progress and a fresh local sign-in code", "適用には進行状況と新しいローカルログインコードを表示する非公開の端末が必要です"))
	}
	if err := client(ctx, dir, lifecycleIdentityCommand, &pinned); err != nil {
		return err
	}
	if pinned.ProcessID <= 0 || pinned.Instance == "" {
		return errors.New(text(ja, "The current process identity is unavailable", "現在のプロセスの識別情報を確認できません"))
	}
	commandClient = boundUpgradeClient(client, &pinned)
	fmt.Fprintln(out, text(ja, "Applying the reviewed upgrade. Connections may briefly stop; management may reopen and require a fresh local sign-in.", "確認済みの更新を適用します。接続が一時停止し、管理画面の再起動とローカル再ログインが必要な場合があります。"))
	progress, err := applyManagedUpgradeIntent(intent, query, func() error {
		fmt.Fprintln(out, text(ja, "Waiting for acknowledged shutdown and old-process exit.", "停止完了の応答と旧プロセスの終了を確認しています。"))
		var next upgradeIdentity
		if err := restartManagedUpgradeProcess(ctx, dir, locale, ja, out, client, &pinned, &next); err != nil {
			return err
		}
		pinned = next
		return reopenUpgradeUI(ctx, dir, ja, out, commandClient)
	})
	if err != nil {
		if errors.Is(err, errUpgradeBinding) {
			return errors.New(text(ja, "Upgrade progress no longer matches the reviewed request", "更新の進行状況が確認済みの要求と一致しません"))
		}
		if errors.Is(err, errUpgradeSuccessor) {
			return errors.New(text(ja, "Fresh offline management rejected the exact upgrade; review current state again", "新しいオフライン管理で更新を拒否しました。現在の状態を再確認してください"))
		}
		return err
	}

	last := ""
	for {
		if progress.PeerID != intent.PeerID || progress.Deadline != intent.Deadline {
			return errors.New(text(ja, "Upgrade progress no longer matches the reviewed request", "更新の進行状況が確認済みの要求と一致しません"))
		}
		if progress.State != last {
			fmt.Fprintln(out, upgradeProgressText(progress.State, ja))
			last = progress.State
		}
		switch progress.State {
		case "network-started":
			fmt.Fprintln(out, text(ja, "Pair confirmation is saved and ordinary network start was accepted. Listener readiness, application permission and bound-session readiness remain separate.", "ペアの確認を保存し、通常接続の開始を受け付けました。待受の準備、アプリの許可、セッション準備は別途必要です。"))
			return nil
		case "local-confirmed":
			return errors.New(text(ja, "Local confirmation is saved, but ordinary activation is incomplete; inspect upgrade status", "ローカル確認は保存済みですが、通常接続の開始は未完了です。更新状態を確認してください"))
		case "failed", "cancelled", "expired":
			return errors.New(text(ja, "Upgrade did not complete; inspect upgrade status and review before retrying", "更新は完了していません。更新状態を確認し、再確認してから再試行してください") + " (" + progress.ErrorCode + ")")
		}
		if err := loginPause(ctx); err != nil {
			return fmt.Errorf("%s: %w", text(ja, "Upgrade observation ended; confirmation is not implied. Inspect upgrade status before retrying", "更新の確認を終了しました。完了を意味しません。再試行前に更新状態を確認してください"), err)
		}
		var next core.UpgradeProgress
		if err := query("direct-lan.upgrade.status", map[string]any{}, &next); err != nil {
			return err
		}
		progress = next
	}
}

func upgradeProgressText(state string, ja bool) string {
	switch state {
	case "preparing":
		return text(ja, "Preparing the reviewed peer context.", "確認したペアコンテキストを準備しています。")
	case "exchanging", "confirming":
		return text(ja, "Confirming the peer through authenticated exchanges.", "認証済みの交換で相手を確認しています。")
	case "activating", "connecting":
		return text(ja, "Peer context confirmed; closing its owner before ordinary activation.", "ペアコンテキストを確認しました。所有処理を終了して通常接続を開始します。")
	case "network-started":
		return text(ja, "Ordinary network start was accepted.", "通常接続の開始を受け付けました。")
	case "failed", "cancelled", "expired":
		return text(ja, "Upgrade stopped before completion.", "更新は完了前に停止しました。")
	default:
		return text(ja, "Upgrade is in progress.", "更新を処理しています。")
	}
}

var errUpgradeBinding = errors.New("upgrade binding changed")
var errUpgradeSuccessor = errors.New("fresh offline successor rejected the exact upgrade")

// applyManagedUpgradeIntent is the shared CLI/Web controller entry. The exact
// reviewed intent and deadline are reapplied only after the verified restart.
func applyManagedUpgradeIntent(intent core.UpgradeIntent, query func(string, any, any) error, restart func() error) (core.UpgradeProgress, error) {
	var progress core.UpgradeProgress
	if err := query("direct-lan.upgrade.run", intent, &progress); err != nil {
		return progress, err
	}
	if progress.PeerID != intent.PeerID || progress.Deadline != intent.Deadline {
		return progress, errUpgradeBinding
	}
	if progress.RestartRequired {
		if err := restart(); err != nil {
			return progress, err
		}
		// Each IPC reply is a complete snapshot. JSON omits false/empty fields,
		// so decoding into the old reply would retain its restart requirement
		// (or stale peer/deadline) after the verified successor has started.
		var successor core.UpgradeProgress
		if err := query("direct-lan.upgrade.run", intent, &successor); err != nil {
			return successor, err
		}
		progress = successor
		if progress.RestartRequired || progress.PeerID != intent.PeerID || progress.Deadline != intent.Deadline {
			return progress, errUpgradeSuccessor
		}
	}
	return progress, nil
}

func restartManagedUpgrade(ctx context.Context, dir, locale string, ja bool, out io.Writer, client controlCaller) error {
	if err := restartManagedUpgradeProcess(ctx, dir, locale, ja, out, client); err != nil {
		return err
	}
	return reopenUpgradeUI(ctx, dir, ja, out, client)
}

func restartManagedUpgradeProcess(ctx context.Context, dir, locale string, ja bool, out io.Writer, client controlCaller, binding ...*upgradeIdentity) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executableInfo, err := os.Stat(executable)
	if err != nil || !executableInfo.Mode().IsRegular() {
		return errors.New("launcher executable is unavailable")
	}
	var old upgradeIdentity
	if err := client(ctx, dir, lifecycleIdentityCommand, &old); err != nil {
		return err
	}
	if len(binding) != 0 && (len(binding) != 2 || binding[0] == nil || binding[1] == nil || old.ProcessID != binding[0].ProcessID || old.Instance != binding[0].Instance) {
		return errors.New("reviewed process identity changed")
	}
	if !sameUpgradeExecutable(executable, old.Executable) || old.ProcessID <= 0 || old.Instance == "" {
		return errors.New(text(ja, "The running application does not match this launcher; no restart was requested", "稼働中の本体がこの起動プログラムと一致しません。再起動は要求していません"))
	}
	next, err := managedUpgradeRestartSequence(old, func() error {
		return stopForManagedUpgrade(ctx, dir, old, client)
	}, func() (upgradeIdentity, error) {
		fmt.Fprintln(out, text(ja, "Old application closed and released its profile. Opening fresh offline management.", "旧本体の終了とプロファイル解放を確認しました。新しいオフライン管理を開きます。"))
		var result bytes.Buffer
		launch := func(exe string, args []string, log *os.File) (backgroundProcess, error) {
			currentInfo, err := os.Stat(exe)
			if err != nil || exe != executable || !os.SameFile(executableInfo, currentInfo) || executableInfo.Size() != currentInfo.Size() || !executableInfo.ModTime().Equal(currentInfo.ModTime()) {
				return backgroundProcess{}, errors.New("launcher executable changed")
			}
			return launchBackground(executable, args, log)
		}
		if err := startBackground(ctx, dir, locale, true, ja, &result, client, launch, 10*time.Second); err != nil {
			return upgradeIdentity{}, err
		}
		var started struct {
			State string `json:"state"`
			PID   int    `json:"pid"`
		}
		if json.Unmarshal(result.Bytes(), &started) != nil || started.State != "ready" || started.PID <= 0 {
			return upgradeIdentity{}, errors.New(text(ja, "A fresh successor was not verified; upgrade was not resumed", "新しい後継プロセスを確認できないため、更新を再開していません"))
		}
		var successor upgradeIdentity
		if err := client(ctx, dir, lifecycleIdentityCommand, &successor); err != nil {
			return upgradeIdentity{}, err
		}
		if successor.ProcessID != started.PID || !sameUpgradeExecutable(executable, successor.Executable) {
			return upgradeIdentity{}, errors.New(text(ja, "Successor identity did not match; upgrade was not resumed", "後継のIDが一致しないため、更新を再開していません"))
		}
		return successor, nil
	})
	if err != nil {
		return err
	}
	if len(binding) == 2 {
		*binding[1] = next
	}
	return nil
}

// This gate contains no OS work. Its production shutdown callback returns only
// after successful close acknowledgement, lock release and native process exit.
func managedUpgradeRestartSequence(old upgradeIdentity, shutdown func() error, launch func() (upgradeIdentity, error)) (upgradeIdentity, error) {
	if err := shutdown(); err != nil {
		return upgradeIdentity{}, err
	}
	next, err := launch()
	if err != nil {
		return upgradeIdentity{}, err
	}
	if next.ProcessID <= 0 || next.Instance == "" || next.Instance == old.Instance || !next.Offline || next.AttemptedNetwork || next.NetworkReady {
		return upgradeIdentity{}, errors.New("successor is not a verified fresh offline process; upgrade was not resumed")
	}
	return next, nil
}

func sameUpgradeExecutable(a, b string) bool {
	if a == "" || b == "" || !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && left.Mode().IsRegular() && right.Mode().IsRegular() && os.SameFile(left, right)
}

func validUpgradeUIURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || u.Path != "" && u.Path != "/" {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return false
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	return err == nil && port != 0 && u.Host == netip.AddrPortFrom(ip, uint16(port)).String()
}

func reopenUpgradeUI(ctx context.Context, dir string, ja bool, out io.Writer, client controlCaller) error {
	if !loginPrivateTerminal(out) {
		return errors.New("private terminal was lost; sign-in code was not requested")
	}
	var ui struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	if err := client(ctx, dir, "ui", &ui); err != nil {
		return err
	}
	if !validUpgradeUIURL(ui.URL) || ui.Code == "" {
		return errors.New("unexpected local management response; browser launch refused")
	}
	fmt.Fprintln(out, text(ja, "Local management:", "ローカル管理:"), ui.URL)
	fmt.Fprintln(out, text(ja, "Fresh one-time code (5 minutes):", "新しい一回用コード（5分）:"), ui.Code)
	if err := launchLoginBrowser(ctx, ui.URL); err != nil {
		fmt.Fprintln(out, text(ja, "Browser could not open; use the local address and code above.", "ブラウザーを開けませんでした。上のローカルURLとコードを使ってください。"))
	}
	return nil
}

// boundUpgradeClient keeps CLI actions on the reviewed/verified instance. The
// pointer is replaced only after the shared native restart gate succeeds.
func boundUpgradeClient(client controlCaller, pinned *upgradeIdentity) controlCaller {
	return func(ctx context.Context, dir, raw string, result any) error {
		if raw == "ui" || raw == "ui.url" || len(raw) > 0 && raw[0] == '{' {
			encoded, err := json.Marshal(upgradeBound{ProcessID: pinned.ProcessID, Instance: pinned.Instance, Command: raw})
			if err != nil {
				return err
			}
			return client(ctx, dir, lifecycleBoundPrefix+string(encoded), result)
		}
		return client(ctx, dir, raw, result)
	}
}
