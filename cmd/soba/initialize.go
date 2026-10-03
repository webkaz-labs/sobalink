package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
)

func initializeCommand(ctx context.Context, args []string, dir string, ja, dryRun bool, out io.Writer) error {
	f := commandFlags("init", ja, out)
	hostname := f.String("name", "", text(ja, "optional local node name; an existing name is never overwritten", "ローカル端末の名前（省略可。既存の名前は上書きしない）"))
	structured := f.Bool("json", false, text(ja, "machine-readable initialization result", "初期化結果の機械向け出力"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	if hasFlag(args, "name") && (*hostname == "" || !config.ValidName(*hostname) || len(*hostname) > 63 || strings.ContainsAny(*hostname, "_ ")) {
		return errors.New(text(ja, "Choose a node name up to 63 bytes using letters, digits and hyphens", "端末名は63バイト以内の文字・数字・ハイフンで指定してください"))
	}
	if dryRun {
		return json.NewEncoder(out).Encode(map[string]any{"applied": false, "command": "init", "hostname": *hostname, "network": "none", "validation": "local-input-only"})
	}
	result, err := core.InitializeProfile(ctx, core.Options{Directory: dir, Version: version, SkipNetworkStart: true}, *hostname)
	if err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(out).Encode(result)
	}
	if result.State == "exists" {
		fmt.Fprintln(out, text(ja, "The existing profile is unchanged.", "既存のプロファイルは変更していません。"))
	} else {
		fmt.Fprintln(out, text(ja, "Local profile initialized. No network, sign-in, credential or listener was started.", "ローカルのプロファイルを初期化しました。ネットワーク・ログイン・認証情報・待受は開始していません。"))
	}
	fmt.Fprintf(out, "%s: %s\n%s\n", text(ja, "Node name", "端末名"), displayText(result.Hostname), cliCommandExample(dir, "start", "--background"))
	return nil
}
