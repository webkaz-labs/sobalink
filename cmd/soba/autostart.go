package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/webkaz-labs/sobalink/internal/autostart"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

type autostartEnvironment func() (autostart.Options, error)

func currentAutostartEnvironment() (autostart.Options, error) {
	o := autostart.Options{OS: runtime.GOOS}
	var err error
	if o.Home, err = os.UserHomeDir(); err != nil {
		return o, err
	}
	if o.ConfigDir, err = os.UserConfigDir(); err != nil {
		return o, err
	}
	if o.Executable, err = os.Executable(); err != nil {
		return o, err
	}
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil {
			return o, err
		}
		o.UserID = u.Uid
	}
	return o, nil
}

func runAutostartManager(ctx context.Context, executable string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, executable, args...).Run(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	return nil
}

type startupReview struct {
	Mode              string             `json:"mode"`
	SavedNetwork      string             `json:"savedNetwork"`
	Hostname          string             `json:"hostname,omitempty"`
	NetworkStarts     bool               `json:"networkStarts"`
	AutosaveReceivers []core.Trust       `json:"autosaveReceivers"`
	ServicesRestart   bool               `json:"servicesRestart"`
	ProxiesRestart    bool               `json:"proxiesRestart"`
	Launch            *core.LaunchReview `json:"launch,omitempty"`
	TransfersRestart  bool               `json:"transfersRestart"`
}

type autostartReview struct {
	Plan        autostart.Plan `json:"plan"`
	Startup     startupReview  `json:"startup"`
	ReviewToken string         `json:"reviewToken"`
	Applied     bool           `json:"applied"`
}

func buildAutostartReview(opts autostart.SobaOptions) (autostartReview, error) {
	plan, err := autostart.BuildSoba(opts)
	if err != nil {
		return autostartReview{}, err
	}
	profile, err := core.ReadProfile(filepath.Join(opts.StateDir, "sobalink.json"))
	if errors.Is(err, os.ErrNotExist) {
		profile = core.Profile{Settings: core.Settings{Network: "none"}}
	} else if err != nil {
		return autostartReview{}, err
	}
	var policy json.RawMessage
	if err := config.ReadJSON(filepath.Join(opts.StateDir, "capacity.json"), &policy); err != nil && !errors.Is(err, os.ErrNotExist) {
		return autostartReview{}, err
	}
	mode := opts.StartupMode
	if mode == "" {
		mode = "saved"
	}
	startup := startupReview{Mode: mode, SavedNetwork: profile.Settings.Network, Hostname: profile.Settings.Hostname, AutosaveReceivers: []core.Trust{}}
	startup.NetworkStarts = mode == "saved" && (startup.SavedNetwork == "tailnet" || startup.SavedNetwork == "lan" || startup.SavedNetwork == "direct-lan" || startup.SavedNetwork == "mixed")
	for _, peer := range profile.Peers {
		if startup.NetworkStarts && peer.Network == startup.SavedNetwork && peer.Autosave && !peer.Paused {
			startup.AutosaveReceivers = append(startup.AutosaveReceivers, peer)
		}
	}
	launch, err := core.ReadLaunchReview(opts.StateDir)
	if err != nil {
		return autostartReview{}, err
	}
	if startup.NetworkStarts {
		startup.Launch = &launch
		startup.ServicesRestart = launch.ServicesRestart
		startup.ProxiesRestart = launch.ProxiesRestart
	}
	plan.Note = "User-level registration only. Saved mode restores the selected network and approved receivers, and may start only the valid explicitly approved outbound selections and saved proxies in this review. Inbound shares and previous transfers do not restart. Registration does not change the currently running application."
	if mode == "offline" {
		plan.Note = "User-level registration only. Offline mode starts local management without network, file reception, service selections or saved proxies. Registration does not change the currently running application."
	}
	// Bind every persisted setting/approval to the reviewed plan. A changed
	// profile must be reviewed again even when its visible receiver count agrees.
	raw, err := json.Marshal(struct {
		Plan    string
		Profile core.Profile
		Policy  json.RawMessage
		Launch  core.LaunchReview
	}{autostart.ReviewDigest(plan), profile, policy, launch})
	if err != nil {
		return autostartReview{}, err
	}
	digest := sha256.Sum256(raw)
	return autostartReview{Plan: plan, Startup: startup, ReviewToken: hex.EncodeToString(digest[:])}, nil
}

func autostartCommand(ctx context.Context, dir string, args []string, ja, dryRun bool, out io.Writer, environment autostartEnvironment, runner autostart.Runner) error {
	action := "enable"
	if len(args) > 0 && (args[0] == "enable" || args[0] == "disable") {
		action, args = args[0], args[1:]
	}
	f := commandFlags("autostart", ja, out)
	mode := f.String("startup", "saved", text(ja, "saved: restore saved network/receiver and explicit startup approvals; offline: local management only", "saved: 保存済みネットワーク・受信許可・明示的な起動許可を復元、offline: ローカル管理のみ"))
	apply := f.Bool("apply", false, text(ja, "apply only the exact reviewed registration plan", "確認済みの登録計画だけを適用する"))
	review := f.String("review", "", text(ja, "reviewToken from the matching preview", "一致するプレビューの reviewToken"))
	jsonOutput := f.Bool("json", false, text(ja, "print stable JSON", "機械向けJSONで表示する"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	if *mode != "saved" && *mode != "offline" {
		return errors.New(text(ja, "--startup must be saved or offline", "--startup は saved または offline を指定してください"))
	}
	if dryRun && *apply {
		return errors.New(text(ja, "--dry-run cannot be combined with --apply", "--dry-run と --apply は同時に指定できません"))
	}
	if !*apply && *review != "" {
		return errors.New(text(ja, "--review requires --apply", "--review は --apply と一緒に指定してください"))
	}
	opts, err := environment()
	if err != nil {
		return err
	}
	opts.StateDir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	opts.Action = action
	input := autostart.SobaOptions{Options: opts, StartupMode: *mode}
	preview, err := buildAutostartReview(input)
	if err != nil {
		return err
	}
	if *apply {
		if *review == "" || *review != preview.ReviewToken {
			return errors.New(text(ja, "Review the current autostart preview, then repeat with --apply --review TOKEN. A changed plan or profile needs a new review", "現在の autostart プレビューを確認し、--apply --review TOKEN を付けて再実行してください。計画や設定が変わった場合は再確認が必要です"))
		}
		if err := autostart.Apply(ctx, preview.Plan, runner); err != nil {
			return err
		}
		preview.Applied = true
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(preview)
	}
	fmt.Fprintln(out, text(ja, "Per-user autostart plan:", "ユーザー単位の自動起動計画:"), action)
	if action == "disable" {
		fmt.Fprintln(out, text(ja, "This removes future sign-in startup. Matching registration being removed:", "次回以降のサインイン時の起動を解除します。解除する既存登録の内容:"))
	}
	fmt.Fprintln(out, text(ja, "File:", "ファイル:"), preview.Plan.Path)
	fmt.Fprintln(out, text(ja, "Startup mode / saved network:", "起動方式 / 保存済みネットワーク:"), preview.Startup.Mode, "/", preview.Startup.SavedNetwork)
	if preview.Startup.NetworkStarts {
		fmt.Fprintln(out, text(ja, "The saved network and peer approvals reconnect. Automatic saving resumes for these previously approved receivers:", "保存済みネットワークと相手の許可が復元されます。次の承認済み相手からの自動保存が再開します:"))
		for _, peer := range preview.Startup.AutosaveReceivers {
			fmt.Fprintf(out, "  %s -> %s\n", peer.ID, peer.Directory)
		}
		if len(preview.Startup.AutosaveReceivers) == 0 {
			fmt.Fprintln(out, text(ja, "  None", "  なし"))
		}
	} else {
		fmt.Fprintln(out, text(ja, "Only local management starts; no network or file reception starts.", "ローカル管理のみ起動します。ネットワーク接続・ファイル受信は開始しません。"))
	}
	if preview.Startup.Launch != nil {
		writeAutostartLaunchReview(out, ja, *preview.Startup.Launch)
	}
	fmt.Fprintln(out, text(ja, "Inbound shares and previous transfers do not restart. Only valid, explicitly enabled outbound selections and saved proxies shown above may start. Registration takes effect at a future sign-in and does not stop or start the current application.", "受信側の共有と前回のファイル転送は再開しません。上に表示した有効で明示的に許可済みの接続・保存済みプロキシだけが開始対象です。登録は次回以降のサインインに適用され、現在の本体は起動・停止しません。"))
	if action == "enable" {
		fmt.Fprintln(out, preview.Plan.Content)
	}
	for _, command := range preview.Plan.Commands {
		fmt.Fprintf(out, "%s %q\n", text(ja, "Command arguments:", "コマンド引数:"), command)
	}
	fmt.Fprintln(out, "reviewToken:", preview.ReviewToken)
	if preview.Applied {
		fmt.Fprintln(out, text(ja, "User-level autostart registration updated.", "ユーザー単位の自動起動登録を更新しました。"))
	} else {
		fmt.Fprintln(out, text(ja, "Nothing changed. After reviewing, repeat with --apply --review TOKEN.", "変更はありません。確認後に --apply --review TOKEN を付けて再実行してください。"))
	}
	return nil
}

func writeAutostartLaunchReview(out io.Writer, ja bool, launch core.LaunchReview) {
	fmt.Fprintf(out, "%s: %s; %s: %s\n", text(ja, "Approved outbound starts", "承認済みの接続の開始"), humanYesNo(ja, launch.ServicesRestart), text(ja, "saved proxy starts", "保存済みプロキシの開始"), humanYesNo(ja, launch.ProxiesRestart))
	var startup struct {
		Entries []struct {
			Name     string             `json:"name"`
			Enabled  bool               `json:"enabled"`
			Valid    bool               `json:"valid"`
			Services []core.ServiceSpec `json:"services"`
		} `json:"entries"`
	}
	raw, _ := json.Marshal(launch.Startup)
	_ = json.Unmarshal(raw, &startup)
	for _, entry := range startup.Entries {
		fmt.Fprintf(out, "  %s: %s; %s: %s; %s: %s\n", text(ja, "Selection", "選択"), displayText(entry.Name), text(ja, "enabled", "有効化"), humanYesNo(ja, entry.Enabled), text(ja, "scope still valid", "範囲は現在も有効"), humanYesNo(ja, entry.Valid))
		if entry.Enabled && entry.Valid {
			for _, service := range entry.Services {
				writeHumanService(out, ja, humanFromSpec(service), nil)
			}
		}
	}
	var proxies struct {
		Entries []core.SavedProxyView `json:"entries"`
	}
	raw, _ = json.Marshal(launch.SavedProxies)
	_ = json.Unmarshal(raw, &proxies)
	for _, entry := range proxies.Entries {
		fmt.Fprintf(out, "  SOCKS: %s; %s: %s; %s: %s\n", displayText(entry.Name), text(ja, "enabled", "有効化"), humanYesNo(ja, entry.StartOnLaunch), text(ja, "scope still valid", "範囲は現在も有効"), humanYesNo(ja, entry.Valid))
		if entry.StartOnLaunch && entry.Valid {
			fmt.Fprintf(out, "    %s:%d; %s: %s", entry.Scope.LoopbackHost, entry.Scope.LocalPort, text(ja, "lifetime", "期限"), entry.Scope.Lifetime)
			if entry.Scope.Lifetime == "finite" {
				fmt.Fprintf(out, " (%ds)", entry.Scope.TTLSeconds)
			}
			fmt.Fprintln(out)
			for _, target := range entry.Scope.Targets {
				fmt.Fprintf(out, "    %s / tcp %d\n", target.PeerID, target.Port)
			}
		}
	}
	fmt.Fprintln(out, text(ja, "Saved proxy credentials remain private and are not included in this review.", "保存済みプロキシの認証情報は非公開のままで、この確認には含めません。"))
}
