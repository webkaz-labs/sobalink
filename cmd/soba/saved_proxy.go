package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func privateProxyCommand(name string) bool {
	return name == "proxy.start" || name == "proxy.save" || name == "proxy.generate"
}
func savedProxyCLI(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer, stdin io.Reader, query commandQuery, request actionRequest) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	name := args[0]
	if name == "saved-help" {
		_, err := io.WriteString(out, text(ja, savedProxyHelpEN, savedProxyHelpJA)+"\n")
		return true, err
	}
	if name == "saved" {
		if len(args) != 1 {
			return true, usageError(ja, "proxy saved")
		}
		return true, request("proxy.saved.list", map[string]any{})
	}
	if name == "save" || name == "generate" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := io.WriteString(out, text(ja, savedProxyHelpEN, savedProxyHelpJA)+"\n")
			return true, err
		}
		raw, err := commandPayload(ctx, append([]string{"proxy." + name}, args[1:]...), stdin, ja, dir)
		if err != nil {
			return true, err
		}
		return true, request("proxy."+name, raw)
	}
	if name != "saved-start" && name != "saved-delete" && name != "saved-disable" && name != "reveal" {
		return false, nil
	}
	if len(args) < 2 {
		return true, usageError(ja, "proxy "+name+" NAME --review REVISION")
	}
	f := commandFlags("proxy "+name, ja, out)
	revision := f.String("review", "", text(ja, "saved proxy revision", "保存済みプロキシの revision"))
	path := f.String("private-file", "", text(ja, "private credential output file", "認証情報の非公開出力ファイル"))
	if err := parseFlags(f, args[2:], ja); err != nil {
		return true, err
	}
	if *revision == "" {
		return true, errors.New(text(ja, "Specify --review from proxy saved", "proxy saved の結果の --review を指定してください"))
	}
	payload := map[string]string{"name": args[1], "expectedRevision": *revision}
	if name != "reveal" {
		if *path != "" {
			return true, usageError(ja, "proxy "+name+" NAME --review REVISION")
		}
		return true, request("proxy.saved."+name[len("saved-"):], payload)
	}
	if *path == "" {
		return true, errors.New(text(ja, "Credential reveal requires --private-file PATH; secrets are never printed", "認証情報の確認には --private-file PATH が必要です。認証情報は画面出力しません"))
	}
	target, err := filepath.Abs(*path)
	if err != nil {
		return true, err
	}
	if dryRun {
		return true, json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "proxy.reveal", "privateFile": target, "validation": "local-input-only"})
	}
	var credentials core.SavedProxyCredentials
	if err := query("proxy.reveal", payload, &credentials); err != nil {
		return true, err
	}
	if len(credentials.Username) == 0 || len(credentials.Password) == 0 {
		return true, errors.New(text(ja, "Private credentials were unavailable", "非公開の認証情報を取得できませんでした"))
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return true, errors.New("private credential encoding failed")
	}
	if err := config.AtomicWritePrivate(target, append(data, '\n')); err != nil {
		return true, errors.New(text(ja, "Could not write the private credential file; check its directory and permissions", "認証情報の非公開ファイルを書き込めませんでした。保存先と権限を確認してください"))
	}
	_, err = fmt.Fprintln(out, text(ja, "Credentials written to the selected private file", "指定した非公開ファイルに認証情報を保存しました"))
	return true, err
}

const savedProxyHelpEN = `Optional private saved SOCKS profiles

  soba proxy saved
  soba proxy save --json-file PRIVATE_FILE | --stdin
  soba proxy generate --json-file PRIVATE_FILE | --stdin
  soba proxy saved-start NAME --review REVISION
  soba proxy saved-disable NAME --review REVISION
  soba proxy saved-delete NAME --review REVISION
  soba proxy reveal NAME --review REVISION --private-file PATH

First use proxy preview and proxy saved to obtain scope/revision and the private
store revision. Save input contains scope, expectedRevision, expectedStoreRevision,
username, password and explicit startOnLaunch:true|false. Generate accepts the
same fields except username/password and explicitly generates strong credentials.
Neither operation starts a listener now. Use saved-start to start explicitly.
Only reveal returns credentials, directly into the chosen private file. Keep that
file private. Ordinary status, exports and dry-run output contain no credentials.
Deleting a saved entry also stops its session. Disable prevents future automatic
starts. Saving/replacing a scope requires fresh review and private runtime input.`
const savedProxyHelpJA = `任意の非公開 SOCKS 設定

  soba proxy saved
  soba proxy save --json-file PRIVATE_FILE | --stdin
  soba proxy generate --json-file PRIVATE_FILE | --stdin
  soba proxy saved-start NAME --review REVISION
  soba proxy saved-disable NAME --review REVISION
  soba proxy saved-delete NAME --review REVISION
  soba proxy reveal NAME --review REVISION --private-file PATH

先に proxy preview と proxy saved で範囲・revision と非公開設定の revision を
取得します。save の入力は scope、expectedRevision、expectedStoreRevision、
username、password、明示的な startOnLaunch:true|false です。generate は認証情報を
入力せず、同じ確認項目から強力な認証情報を明示的に生成します。
どちらも今すぐ入口を開始しません。明示的な開始には saved-start を使います。
認証情報は reveal だけで、指定した非公開ファイルに直接保存します。
このファイルは非公開で保管してください。通常の状態表示、書き出し、dry-run に
認証情報は含まれません。削除するとその設定の接続も停止します。
disable は次回以降の自動開始を無効にします。保存・置換には再確認が必要です。`
