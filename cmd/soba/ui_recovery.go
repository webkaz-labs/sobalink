package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/control"
	"github.com/webkaz-labs/sobalink/internal/core"
)

var launchUIOffline = startUIOffline

type uiRecoveryError struct {
	code, message string
	cause         error
}

func (e *uiRecoveryError) Error() string     { return e.message }
func (e *uiRecoveryError) ErrorCode() string { return e.code }
func (e *uiRecoveryError) Unwrap() error     { return e.cause }

// Machine output stays query-only. Human UI recovery uses the same narrow
// platform absence classification as status/stop, never permission or uncertain
// failures. Preserve the selected profile in every recovery command.
func uiUnavailableError(err error, dir string, ja bool, mode ...string) error {
	if !control.Unavailable(err) {
		return err
	}
	return &uiRecoveryError{"ui_agent_unavailable", fmt.Sprintf(text(ja,
		"No local application is reachable for this profile. Run %s, then retry %s.",
		"このプロファイルの本体に接続できません。%s を実行してから %s を再実行してください。"),
		cliCommandExample(dir, "start", "--offline", "--background"), cliCommandExample(dir, append([]string{"ui"}, mode...)...)), err}
}

func uiAccessWithRecovery(ctx context.Context, dir string, ja bool, out io.Writer, client controlCaller, result any, mode ...string) error {
	err := client(ctx, dir, "ui", result)
	if err == nil || !control.Unavailable(err) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := core.CheckExistingProfile(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &uiRecoveryError{"ui_profile_uninitialized", fmt.Sprintf(text(ja,
				"No saved profile was found. If you used another --state-dir, select it. For first use, run %s, then %s.",
				"保存済みプロファイルがありません。以前に別の --state-dir を使った場合は同じ場所を指定してください。初めて使う場合は %s、その後 %s を実行してください。"),
				cliCommandExample(dir, "init"), cliCommandExample(dir, append([]string{"ui"}, mode...)...)), err}
		}
		return err
	}
	started, err := launchUIOffline(ctx, dir, ja, client)
	if err != nil {
		return err
	}
	if started {
		fmt.Fprintln(out, text(ja,
			"Started local management only. Saved networking and automatic connections remain stopped.",
			"ローカル管理のみを起動しました。保存済みネットワークと自動接続は開始していません。"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// One fresh request after readiness. Never replay a delivered or uncertain
	// capability request, restart an existing owner, or silently retry startup.
	return uiUnavailableError(client(ctx, dir, "ui", result), dir, ja, mode...)
}

func startUIOffline(ctx context.Context, dir string, ja bool, client controlCaller) (bool, error) {
	return startUIOfflineWithLauncher(ctx, dir, ja, client, launchBackground)
}

func startUIOfflineWithLauncher(ctx context.Context, dir string, ja bool, client controlCaller, launch backgroundLauncher) (bool, error) {
	var output bytes.Buffer
	locale := text(ja, "en", "ja")
	if err := startBackgroundWithProfile(ctx, dir, locale, true, true, ja, &output, client, launch, 10*time.Second); err != nil {
		return false, err
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return false, err
	}
	return result.State == "ready", nil
}
