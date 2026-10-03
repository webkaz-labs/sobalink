package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func definitionCLI(ctx context.Context, command string, args []string, dir string, ja, dryRun bool, out io.Writer, client controlCaller) (bool, error) {
	if command != "profile" && !(command == "service" && len(args) > 0 && (args[0] == "save" || args[0] == "delete")) {
		return false, nil
	}
	query := func(name string, payload, result any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		command, err := json.Marshal(webui.Command{RequestID: fmt.Sprintf("cli-definitions-%d", time.Now().UnixNano()), Name: name, Payload: raw})
		if err != nil {
			return err
		}
		return client(ctx, dir, string(command), result)
	}
	write := func(value any) error {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	request := func(name string, payload any) error {
		if dryRun {
			return write(map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"})
		}
		var value json.RawMessage
		if err := query(name, payload, &value); err != nil {
			return err
		}
		return write(value)
	}
	if command == "profile" {
		return true, profileCLI(args, dir, ja, dryRun, out, query, request, write)
	}
	if args[0] == "delete" {
		return true, deleteDefinitionCLI(args[1:], ja, out, query, request, write)
	}
	if len(args) < 2 || args[1] != "share" && args[1] != "connect" {
		if len(args) == 1 || args[1] == "--help" || args[1] == "-h" {
			return true, usageHelp(out, ja, "service save share|connect --backend tailnet|lan [--replace SERVICE_ID] [share/connect options]")
		}
		return true, usageError(ja, "service save share|connect --backend tailnet|lan [share/connect options]")
	}
	backend, replace, forwarded, err := definitionSaveOptions(args[2:], ja)
	if err != nil {
		return true, err
	}
	payload, err := servicePayload(args[1], forwarded, ja, out)
	if err != nil {
		return true, err
	}
	if backend != "tailnet" && backend != "lan" {
		return true, errors.New(text(ja, "Saving offline requires --backend tailnet|lan", "オフラインで保存するには --backend tailnet|lan を指定してください"))
	}
	if err := resolveNamedPeerPayload(payload, ja, func(result any) error { return client(ctx, dir, "status", result) }); err != nil {
		return true, err
	}
	payload["backend"] = backend
	payload["direction"] = "forward"
	if args[1] == "share" {
		payload["direction"] = "share"
	}
	input := map[string]any{"configuration": payload}
	if replace != "" {
		resolved, err := resolveServiceReferences([]string{replace}, ja, query)
		if err != nil {
			return true, err
		}
		replace = resolved.IDs[0]
		var saved serviceConfiguration
		if err := query("service.config", map[string]string{"id": replace}, &saved); err != nil {
			return true, err
		}
		if err := checkResolvedName(resolved, saved.Configuration.ID, saved.Configuration.Name, ja); err != nil {
			return true, err
		}
		if saved.Active {
			return true, errors.New(text(ja, "Stop the service before replacing its saved definition", "保存済み設定を置き換える前にサービスを停止してください"))
		}
		if saved.Configuration.ID != replace || len(saved.Revision) != 64 {
			return true, errors.New(text(ja, "Saved definition is incomplete; refresh before retrying", "保存済み設定が不完全です。再読込みしてください"))
		}
		payload["id"], input["expectedRevision"] = replace, saved.Revision
	}
	return true, request("service.save", input)
}

func definitionSaveOptions(args []string, ja bool) (backend, replace string, forwarded []string, err error) {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, value, equals := strings.Cut(args[i], "=")
		if name != "--backend" && name != "--replace" {
			forwarded = append(forwarded, args[i])
			continue
		}
		if seen[name] {
			return "", "", nil, errors.New(text(ja, "Do not repeat --backend or --replace", "--backend と --replace は重複できません"))
		}
		seen[name] = true
		if !equals {
			i++
			if i >= len(args) {
				return "", "", nil, usageError(ja, "service save share|connect --backend tailnet|lan [--replace ID]")
			}
			value = args[i]
		}
		if value == "" {
			return "", "", nil, errors.New(text(ja, "A backend or replacement ID cannot be empty", "ネットワークと置き換えるIDは空にできません"))
		}
		if name == "--backend" {
			backend = value
		} else {
			replace = value
		}
	}
	return
}

func deleteDefinitionCLI(args []string, ja bool, out io.Writer, query commandQuery, request actionRequest, write func(any) error) error {
	if len(args) == 0 {
		return usageError(ja, "service delete SERVICE_ID [--stop-active] [--remove-from-groups] [--apply --review REVISION]")
	}
	if args[0] == "--help" || args[0] == "-h" {
		return usageHelp(out, ja, "service delete SERVICE_ID [--stop-active] [--remove-from-groups] [--apply --review REVISION]")
	}
	f := commandFlags("service delete SERVICE_ID", ja, out)
	apply := f.Bool("apply", false, text(ja, "delete the reviewed saved definition", "確認済みの保存設定を削除"))
	review := f.String("review", "", text(ja, "profile revision shown in the deletion preview", "削除プレビューに表示されたリビジョン"))
	stop := f.Bool("stop-active", false, text(ja, "also stop this service if active", "稼働中の場合はこのサービスも停止"))
	groups := f.Bool("remove-from-groups", false, text(ja, "remove references; delete groups that become empty", "参照も削除し、空になるグループを削除"))
	if err := parseFlags(f, args[1:], ja); err != nil {
		return err
	}
	resolved, err := resolveServiceReferences([]string{args[0]}, ja, query)
	if err != nil {
		return err
	}
	args = append([]string(nil), args...)
	args[0] = resolved.IDs[0]
	var saved serviceConfiguration
	if err := query("service.config", map[string]string{"id": args[0]}, &saved); err != nil {
		return err
	}
	if err := checkResolvedName(resolved, saved.Configuration.ID, saved.Configuration.Name, ja); err != nil {
		return err
	}
	var groupState struct {
		Groups []struct {
			Name       string   `json:"name"`
			ServiceIDs []string `json:"serviceIds"`
		} `json:"groups"`
		Revision string `json:"revision"`
	}
	if err := query("group.list", map[string]any{}, &groupState); err != nil {
		return err
	}
	if saved.Configuration.ID != args[0] || len(saved.Revision) != 64 || len(groupState.Revision) != 64 {
		return errors.New(text(ja, "Current definition review is incomplete", "現在の設定の確認結果が不完全です"))
	}
	references := []string{}
	for _, group := range groupState.Groups {
		for _, id := range group.ServiceIDs {
			if id == args[0] {
				references = append(references, group.Name)
			}
		}
	}
	if !*apply {
		return write(map[string]any{"applied": false, "configuration": saved.Configuration, "active": saved.Active, "groups": references, "revision": groupState.Revision, "stopActive": *stop, "removeFromGroups": *groups})
	}
	if *review == "" || *review != groupState.Revision {
		return errors.New(text(ja, "Deletion review changed; preview again and pass --apply --review REVISION", "削除対象が変わっています。再確認して --apply --review REVISION を指定してください"))
	}
	return request("service.delete", map[string]any{"id": args[0], "expectedRevision": saved.Revision, "expectedProfileRevision": groupState.Revision, "stopActive": *stop, "removeFromGroups": *groups})
}

func profileCLI(args []string, dir string, ja, dryRun bool, out io.Writer, query commandQuery, request actionRequest, write func(any) error) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, text(ja, "Export/import saved services and groups only. No node identity, credentials, trust, login, received files or task owners are included. All imported services stay stopped. Exported definitions contain peer references and ports; review before sharing.", "保存済みサービスとグループのみを書き出し・読込みます。端末ID・認証情報・信頼設定・ログイン状態・受信ファイル・タスク所有者は含みません。読込み後もすべて停止状態です。相手の参照とポートは含まれるため、共有前に内容を確認してください。"))
		return usageHelp(out, ja, "profile export [--output FILE] | profile import FILE [--apply --review REVISION]")
	}
	if args[0] == "export" {
		f := commandFlags("profile export", ja, out)
		path := f.String("output", "", text(ja, "write a new private local file", "新しいローカル非公開ファイルに書き出す"))
		if err := parseFlags(f, args[1:], ja); err != nil {
			return err
		}
		var result struct {
			Profile  json.RawMessage `json:"profile"`
			Revision string          `json:"revision"`
			Disabled bool            `json:"disabled"`
		}
		if err := query("profile.export", map[string]any{}, &result); err != nil {
			return err
		}
		if !result.Disabled || !json.Valid(result.Profile) {
			return errors.New(text(ja, "Export response is incomplete", "書き出し結果が不完全です"))
		}
		if *path == "" || dryRun {
			return write(result)
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		state, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(state, absolute)
		if err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return errors.New(text(ja, "Choose an export destination outside the private application state directory", "アプリの非公開データフォルダー外に書き出してください"))
		}
		// O_EXCL prevents replacing an existing file or symlink. Keep writing
		// through that descriptor so a path replacement cannot redirect the write.
		file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := config.Protect(absolute, false); err != nil {
			_ = file.Close()
			return err
		}
		opened, openedErr := file.Stat()
		current, currentErr := os.Lstat(absolute)
		if openedErr != nil || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
			_ = file.Close()
			return errors.New(text(ja, "Export destination changed before writing", "書き出し前に保存先が変わりました"))
		}
		if _, err := file.Write(append(result.Profile, '\n')); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return write(map[string]any{"path": absolute, "disabled": true, "containsPeerReferences": true})
	}
	if args[0] != "import" || len(args) < 2 {
		return usageError(ja, "profile export [--output FILE] | profile import FILE [--apply --review REVISION]")
	}
	if args[1] == "--help" || args[1] == "-h" {
		return usageHelp(out, ja, "profile import FILE [--apply --review REVISION]")
	}
	f := commandFlags("profile import FILE", ja, out)
	apply := f.Bool("apply", false, text(ja, "replace saved definitions with the reviewed import", "確認済みの読込み内容で保存設定を置き換える"))
	review := f.String("review", "", text(ja, "revision shown in the import preview", "読込みプレビューのリビジョン"))
	if err := parseFlags(f, args[2:], ja); err != nil {
		return err
	}
	bundle, err := core.ReadDefinitionBundle(args[1], dir)
	if err != nil {
		return err
	}
	payload := map[string]any{"profile": bundle}
	var preview struct {
		Revision          string                `json:"revision"`
		Profile           core.DefinitionBundle `json:"profile"`
		Disabled          bool                  `json:"disabled"`
		ReplacesServices  int                   `json:"replacesServices"`
		PreservesIdentity bool                  `json:"preservesIdentity"`
	}
	if err := query("profile.import.preview", payload, &preview); err != nil {
		return err
	}
	if !*apply {
		return write(preview)
	}
	if *review == "" || *review != preview.Revision {
		return errors.New(text(ja, "Import review changed; preview again and pass --apply --review REVISION", "読込みの確認内容が変わっています。再確認して --apply --review REVISION を指定してください"))
	}
	payload["expectedRevision"] = *review
	return request("profile.import", payload)
}
