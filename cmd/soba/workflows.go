package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

type workflowSelection struct {
	Services []core.ServiceSpec `json:"services"`
	Revision string             `json:"revision"`
	Ready    bool               `json:"ready"`
	States   []workflowState    `json:"states"`
	Group    string             `json:"group,omitempty"`
	wire     json.RawMessage
}

// Keep additional Core response fields (actual endpoints, lease expiry and
// application verification) intact in machine-readable readiness output.
func (s *workflowSelection) UnmarshalJSON(data []byte) error {
	type selection workflowSelection
	var decoded selection
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = workflowSelection(decoded)
	s.wire = append(json.RawMessage(nil), data...)
	return nil
}

func (s workflowSelection) MarshalJSON() ([]byte, error) {
	if s.wire != nil {
		return s.wire, nil
	}
	type selection workflowSelection
	return json.Marshal(selection(s))
}

type workflowState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Owner  string `json:"owner,omitempty"`
}

type workflowGroup struct {
	Name       string                 `json:"name"`
	ServiceIDs []string               `json:"serviceIds"`
	RustDesk   *core.RustDeskMetadata `json:"rustdesk,omitempty"`
}

type workflowTarget struct {
	IDs   []string `json:"ids,omitempty"`
	Group string   `json:"group,omitempty"`
}

type workflowCLI struct {
	ctx              context.Context
	dir              string
	ja, dryRun       bool
	structured       bool
	out              io.Writer
	in               io.Reader
	client           controlCaller
	runProcess       func(context.Context, []string, io.Reader, io.Writer) error
	poll, renewEvery time.Duration
	cleanupTimeout   time.Duration
}

func workflowCommand(ctx context.Context, command string, args []string, dir string, ja, dryRun bool, out io.Writer, stdin io.Reader, client controlCaller) (bool, error) {
	w := &workflowCLI{ctx: ctx, dir: dir, ja: ja, dryRun: dryRun, out: out, in: stdin, client: client, runProcess: runWorkflowProcess, poll: 200 * time.Millisecond, renewEvery: 10 * time.Second, cleanupTimeout: 8 * time.Second}
	switch command {
	case "group":
		return true, w.group(args)
	case "services":
		return true, w.services(args)
	case "wait-ready":
		return true, w.selectionAction("wait", args, "")
	case "stop-shares":
		f := commandFlags("stop-shares", ja, out)
		f.BoolVar(&w.structured, "json", false, text(ja, "structured output", "構造化された出力"))
		if err := parseFlags(f, args, ja); err != nil {
			return true, err
		}
		return true, w.apply("service.stop-shares", map[string]any{}, nil)
	case "task":
		return true, w.task(args)
	default:
		return false, nil
	}
}

func (w *workflowCLI) call(ctx context.Context, name string, payload, result any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	command, err := json.Marshal(webui.Command{RequestID: fmt.Sprintf("cli-workflow-%d", time.Now().UnixNano()), Name: name, Payload: raw})
	if err != nil {
		return err
	}
	if err = w.client(ctx, w.dir, string(command), result); err != nil {
		return fmt.Errorf("%s: %w", text(w.ja, "Command failed; review the error and the running soba agent", "操作に失敗しました。エラーと稼働中の soba を確認してください"), err)
	}
	return nil
}

func (w *workflowCLI) write(value any) error {
	encoder := json.NewEncoder(w.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (w *workflowCLI) apply(name string, payload any, selection *workflowSelection) error {
	if w.dryRun {
		preview := map[string]any{"applied": false, "command": name, "payload": payload, "validation": "local-input-only"}
		if selection != nil {
			preview["selection"], preview["validation"] = selection, "saved-selection-reviewed"
		}
		return w.write(preview)
	}
	var result json.RawMessage
	if err := w.call(w.ctx, name, payload, &result); err != nil {
		return err
	}
	if w.structured {
		return w.write(result)
	}
	switch name {
	case "group.save":
		_, err := fmt.Fprintln(w.out, text(w.ja, "Group saved. No services were started.", "グループを保存しました。サービスは開始していません。"))
		return err
	case "service.stop-shares":
		_, err := fmt.Fprintln(w.out, text(w.ja, "All active shares have been stopped.", "稼働中の共有をすべて停止しました。"))
		return err
	default:
		return writeHumanSelection(w.out, w.ja, result)
	}
}

// Accept options before or after selected IDs without changing argument values.
// The task command handles its mandatory -- separator before this parser.
func workflowFlags(f *flag.FlagSet, args []string, ja bool) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		option := f.Lookup(name)
		if option == nil {
			continue
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := parseFlags(f, flags, ja); err != nil {
		return nil, err
	}
	return positional, nil
}

func workflowIDs(list string, positional []string, ja bool) ([]string, error) {
	ids := append([]string(nil), positional...)
	if list != "" {
		ids = append(strings.Split(list, ","), ids...)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if (!config.ValidPeerID(id) && !config.ValidName(id)) || seen[id] {
			return nil, errors.New(text(ja, "Select distinct saved service names or IDs; use soba rules to review them", "重複のない保存名かサービスIDを指定してください。soba rules で確認できます"))
		}
		seen[id] = true
	}
	return ids, nil
}

func (w *workflowCLI) selectServices(target workflowTarget) (workflowSelection, error) {
	var selection workflowSelection
	resolved, err := resolveServiceReferences(target.IDs, w.ja, func(name string, payload, result any) error { return w.call(w.ctx, name, payload, result) })
	if err != nil {
		return selection, err
	}
	target.IDs = resolved.IDs
	if err := w.call(w.ctx, "service.selection", target, &selection); err != nil {
		return selection, err
	}
	ids := make([]string, 0, len(selection.Services))
	valid := len(selection.Services) != 0 && len(selection.Revision) == 64
	for _, service := range selection.Services {
		if !config.ValidPeerID(service.ID) || slices.Contains(ids, service.ID) || (service.Direction != "share" && service.Direction != "forward") {
			valid = false
		}
		if err := checkResolvedName(resolved, service.ID, service.Name, w.ja); err != nil {
			return selection, err
		}
		ids = append(ids, service.ID)
	}
	if len(target.IDs) > 0 && !slices.Equal(target.IDs, ids) {
		valid = false
	}
	if target.Group != "" && selection.Group != target.Group {
		valid = false
	}
	if !valid {
		return selection, errors.New(text(w.ja, "The saved selection is incomplete or changed; review it before retrying", "保存済みの選択が不完全か変更されています。確認してから再実行してください"))
	}
	return selection, nil
}

func (w *workflowCLI) group(args []string) error {
	if len(args) == 0 || workflowHelpArg(args) {
		return w.help("group")
	}
	operation, args := args[0], args[1:]
	if operation == "start" || operation == "stop" || operation == "wait" {
		return w.selectionAction(operation, args, "group")
	}
	if operation != "list" && operation != "save" {
		return errors.New(text(w.ja, "Use group list, save, start, stop or wait", "group list、save、start、stop、wait を使ってください"))
	}
	f := commandFlags("group "+operation, w.ja, w.out)
	f.BoolVar(&w.structured, "json", false, text(w.ja, "structured output", "構造化された出力"))
	var replace, removeRustDesk bool
	if operation == "save" {
		f.BoolVar(&removeRustDesk, "remove-rustdesk", false, text(w.ja, "explicitly detach RustDesk application metadata when replacing this group", "グループ置換時にRustDeskアプリ設定の対応を明示的に外す"))
		f.BoolVar(&replace, "replace", false, text(w.ja, "replace an existing group's members", "既存のグループのメンバーを置き換える"))
	}
	positional, err := workflowFlags(f, args, w.ja)
	if err != nil {
		return err
	}
	if operation == "list" {
		if len(positional) != 0 {
			return usageError(w.ja, "group list [--json]")
		}
		var result json.RawMessage
		if err := w.call(w.ctx, "group.list", map[string]any{}, &result); err != nil {
			return err
		}
		if w.structured {
			return w.write(result)
		}
		return w.writeGroupList(result)
	}
	if len(positional) < 2 || !config.ValidName(positional[0]) {
		return usageError(w.ja, "group save NAME SERVICE_ID... [--replace] [--remove-rustdesk] [--json]")
	}
	ids, err := workflowIDs("", positional[1:], w.ja)
	if err != nil {
		return err
	}
	var groups struct {
		Groups   []workflowGroup `json:"groups"`
		Revision string          `json:"revision"`
	}
	if err := w.call(w.ctx, "group.list", map[string]any{}, &groups); err != nil {
		return err
	}
	if len(groups.Revision) != 64 {
		return errors.New(text(w.ja, "Group revision is missing; refresh group list before retrying", "グループの更新情報がありません。group list で再確認してください"))
	}
	if removeRustDesk && !replace {
		return errors.New(text(w.ja, "--remove-rustdesk requires --replace", "--remove-rustdesk には --replace が必要です"))
	}
	var rustDesk *core.RustDeskMetadata
	for _, group := range groups.Groups {
		if group.Name == positional[0] && !removeRustDesk {
			rustDesk = group.RustDesk
		}
		if group.Name == positional[0] && !replace {
			return errors.New(text(w.ja, "This group exists; review its members with group list and use --replace", "同名のグループがあります。group list で確認して --replace を指定してください"))
		}
	}
	selection, err := w.selectServices(workflowTarget{IDs: ids})
	if err != nil {
		return err
	}
	payload := map[string]any{"group": workflowGroup{Name: positional[0], ServiceIDs: selectionIDs(selection), RustDesk: rustDesk}, "expectedRevision": groups.Revision}
	if removeRustDesk {
		payload["removeRustDesk"] = true
	}
	return w.apply("group.save", payload, &selection)
}

func (w *workflowCLI) services(args []string) error {
	if len(args) == 0 || workflowHelpArg(args) {
		return w.help("services")
	}
	operation := args[0]
	if operation != "start" && operation != "stop" && operation != "wait" {
		return errors.New(text(w.ja, "Use services start, stop or wait", "services start、stop、wait を使ってください"))
	}
	return w.selectionAction(operation, args[1:], "")
}

func (w *workflowCLI) selectionAction(operation string, args []string, kind string) error {
	label := "services " + operation
	if kind == "group" {
		label = "group " + operation
	}
	f := commandFlags(label, w.ja, w.out)
	var list, group string
	owner := f.String("owner", "", text(w.ja, "explicit operation owner; stop and wait require the same owner", "操作の所有者（停止と待機も同じ所有者を指定）"))
	var ttl time.Duration
	if operation == "start" {
		f.DurationVar(&ttl, "ttl", 0, text(w.ja, "temporary finite lifetime for this start; saved definitions stay unchanged", "今回だけの有限の有効期間（保存済み定義は変更しない）"))
	}
	if kind != "group" {
		f.StringVar(&list, "services", "", text(w.ja, "comma-separated saved service IDs", "保存済みサービスID（カンマ区切り）"))
		f.StringVar(&group, "group", "", text(w.ja, "saved group name", "保存済みのグループ名"))
	}
	structured := f.Bool("json", false, text(w.ja, "structured output; shares require --confirm", "構造化された出力（共有の開始には --confirm が必要）"))
	var confirm bool
	if operation == "start" {
		f.BoolVar(&confirm, "confirm", false, text(w.ja, "approve the selected shares and their saved lifetimes", "選択した共有と保存済みの有効期間を承認する"))
	}
	timeout := 30 * time.Second
	if operation == "wait" {
		f.DurationVar(&timeout, "timeout", timeout, text(w.ja, "maximum transport-readiness wait (up to 24h)", "通信の準備を待つ最大時間（24時間以内）"))
	}
	positional, err := workflowFlags(f, args, w.ja)
	if err != nil {
		return err
	}
	if kind == "group" {
		if len(positional) != 1 {
			return usageError(w.ja, label+" NAME [OPTIONS]")
		}
		group, positional = positional[0], nil
	}
	ids, err := workflowIDs(list, positional, w.ja)
	if err != nil {
		return err
	}
	if (group == "") == (len(ids) == 0) || (group != "" && !config.ValidName(group)) {
		return errors.New(text(w.ja, "Choose saved service IDs or one --group NAME", "保存済みサービスID、または --group NAME のどちらかを指定してください"))
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return errors.New(text(w.ja, "--timeout must be positive and at most 24h", "--timeout は0より長く24時間以内で指定してください"))
	}
	if *owner != "" && !config.ValidName(*owner) {
		return errors.New(text(w.ja, "--owner must be a valid name", "--owner には有効な名前を指定してください"))
	}
	if hasFlag(args, "owner") && *owner == "" {
		return errors.New(text(w.ja, "--owner cannot be empty", "--owner は空にできません"))
	}
	if hasFlag(args, "ttl") && !validWorkflowTTL(ttl) {
		return errors.New(text(w.ja, "--ttl must be a positive whole-second duration", "--ttl は正の整数秒で指定してください"))
	}
	target := workflowTarget{IDs: ids, Group: group}
	selection, err := w.selectServices(target)
	if err != nil {
		return err
	}
	if target.Group == "" {
		target.IDs = selectionIDs(selection)
	}
	payload := workflowPayload(target, selection.Revision)
	if *owner != "" {
		payload["owner"] = *owner
	}
	if ttl > 0 {
		payload["ttlSeconds"], payload["lifetime"] = int(ttl.Seconds()), "finite"
	}
	if operation == "wait" {
		if w.dryRun {
			return w.apply("services.ready", payload, &selection)
		}
		ctx, cancel := context.WithTimeout(w.ctx, timeout)
		defer cancel()
		result, err := w.wait(ctx, selectionIDs(selection), *owner)
		var writeErr error
		if *structured {
			writeErr = w.write(result)
		} else {
			raw, _ := json.Marshal(result)
			writeErr = writeHumanSelection(w.out, w.ja, raw)
		}
		if writeErr != nil {
			return writeErr
		}
		return err
	}
	w.structured = *structured
	if operation == "start" && !w.dryRun {
		if err := w.confirmShares(workflowRuntimeReview(selection, ttl), confirm, *structured); err != nil {
			return err
		}
	}
	return w.apply("services."+operation, payload, &selection)
}

func validWorkflowTTL(ttl time.Duration) bool { return ttl >= time.Second && ttl%time.Second == 0 }

func workflowRuntimeReview(selection workflowSelection, ttl time.Duration) workflowSelection {
	if ttl == 0 {
		return selection
	}
	selection.wire = nil
	selection.Services = append([]core.ServiceSpec(nil), selection.Services...)
	for i := range selection.Services {
		selection.Services[i].Lifetime = "finite"
		selection.Services[i].TTLSeconds = int(ttl.Seconds())
	}
	return selection
}

func workflowPayload(target workflowTarget, revision string) map[string]any {
	payload := map[string]any{"expectedRevision": revision}
	if target.Group != "" {
		payload["group"] = target.Group
	} else {
		payload["ids"] = target.IDs
	}
	return payload
}

func selectionIDs(selection workflowSelection) []string {
	ids := make([]string, 0, len(selection.Services))
	for _, service := range selection.Services {
		ids = append(ids, service.ID)
	}
	return ids
}

func (w *workflowCLI) confirmShares(selection workflowSelection, confirmed, structured bool) error {
	shares := false
	for _, service := range selection.Services {
		shares = shares || service.Direction == "share"
	}
	if !shares || confirmed {
		return nil
	}
	if structured {
		return errors.New(text(w.ja, "Review the saved shares with --dry-run, then use --confirm to start them", "--dry-run で保存済みの共有を確認し、--confirm で開始してください"))
	}
	if _, err := fmt.Fprintln(w.out, text(w.ja, "Review the saved shares and their permission lifetimes:", "保存済みの共有と許可の有効期間を確認してください:")); err != nil {
		return err
	}
	for _, service := range selection.Services {
		if service.Direction != "share" {
			continue
		}
		writeHumanService(w.out, w.ja, humanFromSpec(service), nil)
	}
	if _, err := fmt.Fprint(w.out, text(w.ja, "Start these shares? [y/N] ", "この共有を開始しますか？ [y/N] ")); err != nil {
		return err
	}
	answer, err := workflowConfirmation(w.ctx, w.in)
	if err != nil {
		return err
	}
	if answer != "y" && answer != "yes" && answer != "はい" {
		return errors.New(text(w.ja, "Canceled; no service was started", "取り消しました。サービスは開始していません"))
	}
	return nil
}

// A one-byte read never prefetches input that belongs to the child command.
// Cancellation may leave one blocked console read until the CLI exits.
func workflowConfirmation(ctx context.Context, in io.Reader) (string, error) {
	type result struct {
		answer string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var line []byte
		buffer := make([]byte, 1)
		for len(line) < 64 {
			if err := ctx.Err(); err != nil {
				done <- result{err: err}
				return
			}
			n, err := in.Read(buffer)
			if n != 0 {
				if buffer[0] == '\n' {
					done <- result{answer: strings.ToLower(strings.TrimSpace(string(line)))}
					return
				}
				line = append(line, buffer[0])
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- result{answer: strings.ToLower(strings.TrimSpace(string(line)))}
				} else {
					done <- result{err: err}
				}
				return
			}
		}
		done <- result{}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-done:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return result.answer, result.err
	}
}

func (w *workflowCLI) wait(ctx context.Context, ids []string, owner string) (workflowSelection, error) {
	var selection workflowSelection
	for {
		selection = workflowSelection{}
		if err := w.call(ctx, "services.ready", map[string]any{"ids": ids, "owner": owner}, &selection); err != nil {
			return selection, err
		}
		states := map[string]workflowState{}
		ready := selection.Ready
		for _, state := range selection.States {
			states[state.ID] = state
		}
		for _, id := range ids {
			state, exists := states[id]
			if !exists || owner != "" && state.Owner != owner {
				return selection, fmt.Errorf("%s: %s", text(w.ja, "Selected service is missing or its task owner changed", "選択したサービスが見つからないか、所有タスクが変わりました"), id)
			}
			switch state.Status {
			case "active":
			case "reconnecting":
				ready = false
			case "saved", "stopped", "expired", "failed":
				return selection, fmt.Errorf("%s: %s (%s)", text(w.ja, "Service is not running; review its state before retrying", "サービスは稼働していません。状態を確認してから再実行してください"), id, state.Status)
			default:
				return selection, fmt.Errorf("%s: %s", text(w.ja, "Service has an unknown state; review soba status", "サービスの状態が不明です。soba status で確認してください"), id)
			}
		}
		if ready {
			return selection, nil
		}
		timer := time.NewTimer(w.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return selection, ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *workflowCLI) task(args []string) (err error) {
	if workflowHelpArg(args) {
		return w.help("task")
	}
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return usageError(w.ja, "task --services ID[,ID] | --group NAME [--ttl DURATION] [--confirm] [--timeout 30s] -- COMMAND ARG...")
	}
	argv := args[separator+1:]
	if argv[0] == "" {
		return errors.New(text(w.ja, "The task command cannot be empty", "タスクのコマンドは空にできません"))
	}
	f := commandFlags("task", w.ja, w.out)
	list := f.String("services", "", text(w.ja, "comma-separated saved service IDs", "保存済みサービスID（カンマ区切り）"))
	group := f.String("group", "", text(w.ja, "saved group name", "保存済みのグループ名"))
	timeout := f.Duration("timeout", 30*time.Second, text(w.ja, "maximum transport-readiness wait (up to 24h)", "通信の準備を待つ最大時間（24時間以内）"))
	confirm := f.Bool("confirm", false, text(w.ja, "approve selected shares and their saved lifetimes", "選択した共有と保存済みの有効期間を承認する"))
	ttl := f.Duration("ttl", 0, text(w.ja, "temporary finite permission lifetime for this task", "このタスクだけの有限の許可期間"))
	if err := parseFlags(f, args[:separator], w.ja); err != nil {
		return err
	}
	ids, err := workflowIDs(*list, nil, w.ja)
	if err != nil {
		return err
	}
	if (*group == "") == (len(ids) == 0) || *group != "" && !config.ValidName(*group) {
		return errors.New(text(w.ja, "Task requires --services ID[,ID] or --group NAME", "task には --services ID[,ID] または --group NAME が必要です"))
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New(text(w.ja, "--timeout must be positive and at most 24h", "--timeout は0より長く24時間以内で指定してください"))
	}
	if hasFlag(args[:separator], "ttl") && !validWorkflowTTL(*ttl) {
		return errors.New(text(w.ja, "--ttl must be a positive whole-second duration", "--ttl は正の整数秒で指定してください"))
	}
	target := workflowTarget{IDs: ids, Group: *group}
	selection, err := w.selectServices(target)
	if err != nil {
		return err
	}
	if target.Group == "" {
		target.IDs = selectionIDs(selection)
	}
	payload := workflowPayload(target, selection.Revision)
	payload["leaseSeconds"] = 30
	if *ttl > 0 {
		payload["ttlSeconds"], payload["lifetime"] = int(ttl.Seconds()), "finite"
	}
	if w.dryRun {
		return w.write(map[string]any{"applied": false, "command": "task", "argv": argv, "start": payload, "selection": selection, "validation": "saved-selection-reviewed"})
	}
	if err := w.confirmShares(workflowRuntimeReview(selection, *ttl), *confirm, false); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	owner := "task-" + hex.EncodeToString(random[:])
	payload["owner"] = owner
	ids = selectionIDs(selection)
	ctx, cancel := context.WithCancelCause(w.ctx)
	defer cancel(nil)
	// Register cleanup before starting: a lost response or partial start still
	// receives an owner-scoped stop under a fresh, bounded context.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), w.cleanupTimeout)
		defer stop()
		var stopped json.RawMessage
		if cleanupErr := w.call(cleanup, "services.stop", map[string]any{"ids": ids, "owner": owner}, &stopped); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", text(w.ja, "Owned cleanup could not be confirmed; task leases expire 30 seconds after their last renewal", "所有する接続の停止を確認できませんでした。タスクのリースは最終更新から30秒で切れます"), cleanupErr))
		}
	}()
	var started workflowSelection
	if err := w.call(ctx, "services.start", payload, &started); err != nil {
		return err
	}
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(w.renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, finish := context.WithTimeout(ctx, w.renewEvery)
				var result json.RawMessage
				err := w.call(renewCtx, "services.renew", map[string]any{"ids": ids, "owner": owner, "leaseSeconds": 30}, &result)
				finish()
				if err != nil {
					cancel(fmt.Errorf("%s: %w", text(w.ja, "Task lease renewal failed", "タスクのリース更新に失敗しました"), err))
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-renewed }()
	waiting, stopWait := context.WithTimeout(ctx, *timeout)
	_, err = w.wait(waiting, ids, owner)
	stopWait()
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return err
	}
	if _, err := fmt.Fprintln(w.out, text(w.ja, "Transport is ready. Running the command directly. Application behavior and remote job completion are unverified.", "通信の準備ができました。コマンドを直接実行します。アプリの動作と相手側のジョブ完了は未確認です。")); err != nil {
		return err
	}
	err = w.runProcess(ctx, argv, w.in, w.out)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

func workflowHelpArg(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")
}

func (w *workflowCLI) help(command string) error {
	help, _ := workflowHelp(command, w.ja)
	_, err := fmt.Fprintln(w.out, help)
	return err
}

func workflowHelp(command string, ja bool) (string, bool) {
	switch command {
	case "group":
		return text(ja, groupWorkflowHelpEN, groupWorkflowHelpJA), true
	case "services", "wait-ready":
		return text(ja, servicesWorkflowHelpEN, servicesWorkflowHelpJA), true
	case "task":
		return text(ja, taskWorkflowHelpEN, taskWorkflowHelpJA), true
	case "stop-shares":
		return text(ja, "Usage: soba stop-shares [--json]\nStop every active share, including shares leased by tasks.", "使い方: soba stop-shares [--json]\nタスクが所有する共有も含め、稼働中の共有をすべて停止します。"), true
	}
	return "", false
}

const groupWorkflowHelpEN = `Usage: soba group list [--json]
       soba group save NAME SERVICE_ID... [--replace] [--remove-rustdesk] [--json]
       soba group start NAME [--ttl DURATION] [--owner NAME] [--confirm] [--json]
       soba group stop NAME [--owner NAME] [--json]
       soba group wait NAME [--owner NAME] [--timeout 30s] [--json]

Saving a group only stores references to existing service IDs; it starts nothing.
Starts use each saved service's endpoint, peer scope and lifetime.
--ttl overrides only this runtime lifetime; --owner scopes later stop/wait actions. Review shares
with --dry-run before using --confirm. Existing groups require --replace.
RustDesk metadata is preserved; detaching it requires --replace --remove-rustdesk.
Readiness confirms transport only; application behavior remains unverified.`

const groupWorkflowHelpJA = `使い方: soba group list [--json]
        soba group save NAME SERVICE_ID... [--replace] [--remove-rustdesk] [--json]
        soba group start NAME [--ttl DURATION] [--owner NAME] [--confirm] [--json]
        soba group stop NAME [--owner NAME] [--json]
        soba group wait NAME [--owner NAME] [--timeout 30s] [--json]

グループの保存は既存のサービスIDをまとめるだけで、何も開始しません。
開始時は保存済みの接続先・許可する相手・有効期間を使います。
--ttl は今回の有効期間だけを変更し、--owner は停止・待機の所有者を指定します。共有は
--dry-run で確認してから --confirm を指定してください。既存の置換には
--replace が必要です。RustDeskの対応情報を外すには --remove-rustdesk も指定します。
準備完了は通信のみの確認で、アプリの動作は未確認です。`

const servicesWorkflowHelpEN = `Usage: soba services start SERVICE_ID... [--ttl DURATION] [--owner NAME] [--confirm] [--json]
       soba services stop SERVICE_ID... [--owner NAME] [--json]
       soba services wait SERVICE_ID... [--owner NAME] [--timeout 30s] [--json]
       soba wait-ready SERVICE_ID... [--owner NAME] [--timeout 30s] [--json]

Use --services ID[,ID] or --group NAME instead of positional service IDs.
Starts use saved permission lifetimes without changing definitions.
--ttl changes only this start; --owner requires the same owner for stop/wait. Preview with
soba --dry-run services start SERVICE_ID...; --json shares require --confirm.
Readiness confirms transport only, not application success or remote jobs.
Use soba stop-shares to stop every share, including task-owned shares.`

const servicesWorkflowHelpJA = `使い方: soba services start SERVICE_ID... [--ttl DURATION] [--owner NAME] [--confirm] [--json]
        soba services stop SERVICE_ID... [--owner NAME] [--json]
        soba services wait SERVICE_ID... [--owner NAME] [--timeout 30s] [--json]
        soba wait-ready SERVICE_ID... [--owner NAME] [--timeout 30s] [--json]

サービスIDの代わりに --services ID[,ID] または --group NAME を使えます。
開始時は定義を変更せず保存済みの有効期間を使います。
--ttl は今回だけの期間、--owner は停止・待機にも必要な所有者です。
soba --dry-run services start SERVICE_ID... で確認できます。
--json で共有を開始するには --confirm が必要です。準備完了は通信のみの
確認で、アプリの成功や相手側ジョブの完了は未確認です。
soba stop-shares はタスクが所有する共有を含むすべての共有を停止します。`

const taskWorkflowHelpEN = `Usage: soba task --services ID[,ID] | --group NAME [--ttl DURATION] [--confirm] [--timeout 30s] -- COMMAND ARG...

Review saved services with --dry-run, start them under a private task lease, wait
for transport readiness, then execute COMMAND directly without a shell. Explicit
-- is required. --ttl optionally overrides this run without editing saved lifetimes; the task lease renews
every 10 seconds and expires after 30 seconds without renewal. The task stops
its own services on success, command failure, cancellation or lost renewal.
Already active services cannot be taken over. Shares require confirmation.
Transport readiness does not establish application success. Remote jobs need
their own cancellation mechanism.`

const taskWorkflowHelpJA = `使い方: soba task --services ID[,ID] | --group NAME [--ttl DURATION] [--confirm] [--timeout 30s] -- COMMAND ARG...

--dry-run で保存済みサービスを確認し、タスク専用のリースで開始します。
通信の準備を待ってから、シェルを介さず COMMAND を直接実行します。
区切りの -- は必須です。--ttl は保存済み定義を変えず今回の期間を指定します。リースは10秒ごとに
更新されます。更新が途絶えると30秒で切れます。成功・コマンド失敗・取消・
更新失敗のすべてで、このタスクが所有するサービスを停止します。
稼働中のサービスを引き継ぐことはできません。共有には承認が必要です。
通信の準備完了はアプリの成功を保証しません。相手側のジョブを取り消すには
そのジョブ固有の手段が必要です。`

func (w *workflowCLI) writeGroupList(raw json.RawMessage) error {
	var groups struct {
		Groups []workflowGroup `json:"groups"`
	}
	if err := json.Unmarshal(raw, &groups); err != nil {
		return err
	}
	var exported struct {
		Profile core.DefinitionBundle `json:"profile"`
	}
	if err := w.call(w.ctx, "profile.export", map[string]any{}, &exported); err != nil {
		return err
	}
	names := map[string]string{}
	for _, service := range exported.Profile.Services {
		names[service.ID] = service.Name
	}
	fmt.Fprintln(w.out, text(w.ja, "Saved groups (saving does not start services):", "保存済みグループ（保存だけでは開始しません）:"))
	if len(groups.Groups) == 0 {
		fmt.Fprintln(w.out, text(w.ja, "No saved groups. Use group save NAME SERVICE_NAME...", "保存済みグループはありません。group save 名前 サービス名... で保存できます。"))
	}
	for _, group := range groups.Groups {
		fmt.Fprintln(w.out, displayText(group.Name)+":")
		for _, id := range group.ServiceIDs {
			fmt.Fprintln(w.out, "  "+humanPeerLabel(id, names))
		}
		if group.RustDesk != nil {
			fmt.Fprintln(w.out, text(w.ja, "  RustDesk client settings attached", "  RustDeskアプリ設定との対応あり"))
		}
	}
	return nil
}
