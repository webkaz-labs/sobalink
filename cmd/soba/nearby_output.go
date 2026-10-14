package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Public setup DTOs mirror internal/core/setup_types.go. Keep the original JSON
// for machine output so additive Core fields are not lost or localized.
type nearbyScope struct {
	ScopeID        string `json:"scopeId"`
	InterfaceIndex int    `json:"interfaceIndex"`
	Interface      string `json:"interface"`
	LocalAddress   string `json:"localAddress"`
	Prefix         string `json:"prefix"`
}
type nearbyScopes struct {
	Kind    string        `json:"kind"`
	Version int           `json:"version"`
	Scopes  []nearbyScope `json:"scopes"`
}
type nearbyContact struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	SessionID string    `json:"sessionId"`
	Alias     string    `json:"alias"`
	Bootstrap string    `json:"bootstrap"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type nearbyCandidate struct {
	CandidateID  string        `json:"candidateId"`
	Contact      nearbyContact `json:"contact"`
	ObservedAt   time.Time     `json:"observedAt"`
	ExpiresAt    time.Time     `json:"expiresAt"`
	Verification string        `json:"verification"`
}
type nearbyParticipant struct {
	Protocol     int    `json:"protocol"`
	Mode         string `json:"mode"`
	Verification string `json:"verification"`
	SessionID    string `json:"session_id"`
	Key          string `json:"key"`
	Alias        string `json:"alias"`
	Bootstrap    string `json:"bootstrap"`
	Scope        struct {
		InterfaceIndex int    `json:"interface_index"`
		LocalAddress   string `json:"local_address"`
		Prefix         string `json:"prefix"`
	} `json:"scope"`
	AllowHost bool      `json:"allow_host"`
	Expires   time.Time `json:"expires"`
}
type nearbyPlan struct {
	Revision         uint64               `json:"revision"`
	Fingerprint      string               `json:"fingerprint"`
	Verification     string               `json:"verification"`
	VerificationText string               `json:"verificationText"`
	VerificationQR   [][]bool             `json:"verificationQR,omitempty"`
	Backend          string               `json:"backend"`
	Participants     [2]nearbyParticipant `json:"participants"`
	HostKey          string               `json:"hostKey"`
	Relay            struct {
		Kind              string `json:"kind"`
		Address           string `json:"address"`
		CertificateSHA256 string `json:"certificateSHA256"`
	} `json:"relay"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type nearbySnapshot struct {
	Alias          string            `json:"alias"`
	Mode           string            `json:"mode"`
	Verification   string            `json:"verification"`
	RequestID      string            `json:"requestId"`
	Target         *nearbyV2Target   `json:"target"`
	Request        *nearbyV2Request  `json:"request"`
	Kind           string            `json:"kind"`
	Version        int               `json:"version"`
	SessionID      string            `json:"sessionId"`
	ExpiresAt      time.Time         `json:"expiresAt"`
	State          string            `json:"state"`
	ReasonCode     string            `json:"reasonCode"`
	NextAction     string            `json:"nextAction"`
	Scope          *nearbyScope      `json:"scope"`
	Contact        *nearbyContact    `json:"contact"`
	ContactText    string            `json:"contactText"`
	ContactQR      [][]bool          `json:"contactQR"`
	Candidates     []nearbyCandidate `json:"candidates"`
	Selected       *nearbyContact    `json:"selected"`
	Plan           *nearbyPlan       `json:"plan"`
	LocalApproved  bool              `json:"localApproved"`
	RemoteApproved bool              `json:"remoteApproved"`
	Paired         bool              `json:"paired"`
	Connected      bool              `json:"connected"`
	Trusted        bool              `json:"trusted"`
	Process        struct {
		State                   string `json:"state"`
		ProfilePreserved        bool   `json:"profilePreserved"`
		ApprovalsSurviveRestart bool   `json:"approvalsSurviveRestart"`
	} `json:"process"`
	Discovery struct {
		State      string `json:"state"`
		ReasonCode string `json:"reasonCode"`
	} `json:"discovery"`
}

type nearbyCLIError struct{ code, message string }

func (e *nearbyCLIError) Error() string     { return e.message }
func (e *nearbyCLIError) ErrorCode() string { return e.code }
func nearbyError(ja bool, code string) error {
	messages := map[string][2]string{
		"setup_reenroll_offline_required":     {"Stop the same profile and restart it offline before permitting reenrollment. Active transfers and connections will stop.", "再登録を許可する前に、同じプロファイルを停止してオフラインで再起動してください。進行中の転送や接続は終了します。"},
		"setup_peer_still_paired":             {"A saved transport pair still exists for this key. Explicitly revoke it and confirm cleanup before reenrollment.", "この鍵の通信ペアがまだ保存されています。明示的に解除し、終了状態を確認してから再登録してください。"},
		"setup_generation_changed":            {"The removed key's generation changed. Read the current removed-key list and review it again; old consent cannot be reused.", "解除した鍵の世代が変わりました。現在の一覧を読み直して確認してください。古い同意は使えません。"},
		"setup_reenroll_consent_required":     {"Review the exact removed key and generation, then explicitly consent to a fresh pairing attempt.", "解除した正確な鍵と世代を確認し、新しいペアリングの試行を明示的に許可してください。"},
		"setup_generation_exhausted":          {"This profile cannot safely advance its pairing generation. Do not clear its private state or retry blindly.", "このプロファイルのペアリング世代を安全に進められません。非公開の状態を消したり、確認せず再試行したりしないでください。"},
		"setup_request_unavailable":           {"The latest request exchange could not be completed. Check the same session and the receiving device; this does not mean it was declined.", "依頼の最新の通信を完了できませんでした。同じセッションと相手の受付状態を確認してください。拒否されたという意味ではありません。"},
		"setup_verification_downgrade":        {"This key already has independent full verification. Select that method on both devices; it cannot silently become first-use acceptance.", "この鍵は既に独立した全文照合済みです。両端末で全文照合を選んでください。初回受入方式へ無言で格下げできません。"},
		"setup_request_cancelled":             {"The sender cancelled this request. Previous approval does not apply to a later request.", "依頼元が取り消しました。以前の承認は次の依頼に引き継ぎません。"},
		"setup_mode_invalid":                  {"Choose request, receive, or outbound mode.", "接続依頼・受付・接続の開始のみの方式を選んでください。"},
		"setup_role_invalid":                  {"The selected device is not receiving compatible requests. Check its receiving screen.", "相手は対応する依頼を受付中ではありません。相手の受付画面を確認してください。"},
		"setup_version_mismatch":              {"The devices use incompatible request protocols. Update both or explicitly use full verification; no automatic fallback occurs.", "両端末の依頼方式が対応していません。両方を更新するか、明示的に全文照合を使ってください。自動で方式は変えません。"},
		"setup_target_invalid":                {"Enter a numeric private IP:port from the receiving screen, within the selected network scope.", "相手の受付画面にある数値のプライベートIP:ポートを、選択した範囲内で入力してください。"},
		"setup_target_unreachable":            {"Could not reach the receiving device. Check its address, receiving state, and network path; an empty discovery list does not prove a firewall block.", "相手に到達できません。接続先、受付状態、通信経路を確認してください。探索結果が空でもファイアウォールが原因とは限りません。"},
		"setup_target_changed":                {"The selected recipient changed. Inspect the target again and review its key before a new request.", "確認した接続先が変わりました。接続先と鍵を確認し直してから依頼してください。"},
		"setup_request_changed":               {"This request changed. Refresh and review the current request before approving.", "依頼が変わりました。更新して現在の依頼を確認してから承認してください。"},
		"setup_request_rejected":              {"The request was declined. Check with the recipient before starting a fresh request.", "依頼が拒否されました。相手に確認してから新しい依頼を始めてください。"},
		"setup_request_expired":               {"The request expired. Inspect cleanup, then start a fresh request with new consent; a longer review time can be selected beforehand.", "依頼の期限が切れました。終了状態を確認し、改めて許可して依頼してください。開始前に長い確認時間を選べます。"},
		"setup_tofu_acknowledgement_required": {"Explicitly acknowledge first-use key acceptance. This does not independently verify the recipient's identity.", "初回に受け入れた鍵を記憶することを明示的に確認してください。独立した本人確認ではありません。"},
		"setup_peer_revoked":                  {"This key was removed previously. It cannot be silently restored by an old request.", "この鍵は以前解除されています。古い依頼で自動的に復元できません。"},
		"setup_outbound_only":                 {"This session starts connections only. Enter the receiving device's address; discovery and LAN listeners are disabled.", "このセッションは接続の開始のみです。相手の接続先を入力してください。探索とLAN待受は無効です。"},
		"setup_discovery_expired":             {"The discovery period ended. An existing request can continue until its own deadline; otherwise enter the receiving address or start fresh discovery with consent.", "探索時間が終了しました。既存の依頼はその期限まで続けられます。接続先を入力するか、改めて許可して探索してください。"},
		"setup_invalid":                       {"Invalid nearby inputs; use soba nearby --help. No input values are echoed.", "近くの端末との接続の指定を確認してください。soba nearby --help を参照できます。入力値はエラーに表示しません。"},
		"setup_consent_required":              {"Review the chosen scope and local listener permission, then explicitly add --consent. JSON never prompts or gives consent.", "選択した範囲とローカル待受の許可を確認し、明示的に --consent を指定してください。JSONでは質問も暗黙の承認も行いません。"},
		"setup_scope_required":                {"Choose one scope with --scope from nearby scopes. Dry-run requires an explicit scope and never queries interfaces or Core.", "nearby scopes の範囲を --scope で一つ選んでください。dry-runでは範囲の明示が必要で、インターフェースや本体を照会しません。"},
		"setup_scope_unavailable":             {"The selected scope is unavailable. Review nearby scopes; do not expand to another interface without new consent.", "選択した範囲を利用できません。nearby scopes を確認し、新しい承認なしで別のインターフェースへ広げないでください。"},
		"setup_input_required":                {"Choose exactly one --file PATH or --stdin (pipe only), or use the explicit --fingerprint for approval.", "--file PATH または --stdin（パイプ入力）のどちらか一つを指定してください。承認時は --fingerprint の明示指定も使えます。"},
		"setup_input_unavailable":             {"Could not read the input. Use a readable regular file or a bounded pipe.", "入力を読み込めません。読取り可能な通常ファイルか有限のパイプ入力を使ってください。"},
		"setup_input_too_large":               {"The setup input is too large. Copy only the single contact or verification line.", "接続情報が大きすぎます。連絡先または照合情報の1行だけをコピーしてください。"},
		"setup_verification_required":         {"Compare the complete SHA-256 fingerprint through an independent channel. Explicit --verified confirms that comparison and consents to this exact connection plan; matching text alone is not approval.", "独立した方法でSHA-256指紋の全桁を照合してください。--verified は照合の完了と、この正確な接続計画への承認を示します。文字列の一致だけでは承認になりません。"},
		"setup_fingerprint_mismatch":          {"The complete fingerprints differ. Do not connect; check both devices and the current revision.", "指紋の全桁が一致しません。接続せず、両端末と現在の版を確認してください。"},
		"setup_stale":                         {"The session or reviewed plan changed or expired. Check the original session; review the new plan before approving again.", "セッションまたは確認した計画が変わったか期限切れです。元のセッションを照会し、新しい計画を確認してから再承認してください。"},
		"setup_invalid_response":              {"The setup response is incomplete or inconsistent. Check the original session with matching CLI and Core versions; do not start a replacement blindly.", "接続の応答が不完全か整合していません。CLIと本体の版を合わせて元のセッションを確認し、確認せずに代わりの接続を開始しないでください。"},
		"setup_control_unavailable":           {"Result not confirmed. Use the same profile to check nearby status and the original session before repeating any action. Do not start a replacement session blindly.", "結果を確認できません。同じプロファイルの nearby status で元のセッションを照会してから操作してください。確認せずに代わりのセッションを開始しないでください。"},
		"setup_stop_required":                 {"Stop the same profile explicitly, then start it offline. Active connections and transfers stop. Setup approvals do not survive restart; the profile is preserved.", "同じプロファイルを明示的に停止し、オフラインで起動してください。現在の接続と転送は終了します。接続の承認は再起動で失われ、プロファイルは保持されます。"},
		"setup_needs_restart":                 {"The saved network needs an explicit restart of the same profile. Existing TCP sessions do not migrate. Check pairing and reachability after restart; setup approvals do not survive it.", "保存済みネットワークには同じプロファイルの明示的な再起動が必要です。既存のTCP接続は移行しません。再起動後にペアと到達性を確認してください。接続の承認は再起動で失われます。"},
		"setup_recovery_required":             {"Pairing or saved state is uncertain. Preserve this profile and inspect both devices before a new invitation or session. Do not assume rollback or retry blindly.", "ペアリングまたは保存状態が未確定です。このプロファイルを保持し、新たな招待やセッションの前に両端末を確認してください。元に戻ったと判断したり、確認せずに再試行したりしないでください。"},
		"setup_file_exists":                   {"Could not create a new private output file. Choose a new path; an existing file is never replaced.", "新しい非公開の出力ファイルを作成できません。別のパスを指定してください。既存ファイルは置換しません。"},
		"setup_offline_unavailable":           {"Nearby setup needs the local agent, optionally started with start --offline. Global --offline is for saved-definition edits and cannot run nearby setup.", "近くの端末との接続にはローカル本体が必要です。start --offline での起動も使えます。共通指定の --offline は保存済み定義の編集用で、この操作には使えません。"},
	}
	additional := map[string][2]string{
		"setup_contact_invalid":                 {"Contact information is invalid or expired. Privately obtain the other device's current contact line.", "連絡先が無効か期限切れです。相手の現在の連絡先の1行を非公開の方法で取得してください。"},
		"setup_busy":                            {"An existing session is active or closing. Check that original session; finish or cancel it before starting another.", "既存のセッションが実行中か終了処理中です。元のセッションを確認し、完了または取消の後に次を開始してください。"},
		"setup_discovery_unavailable":           {"Discovery is unavailable. Privately exchange current contact text/files and select each other on both devices.", "探索を利用できません。現在の連絡先の文字列・ファイルを非公開の方法で交換し、両端末で互いを選んでください。"},
		"setup_port_invalid":                    {"Choose distinct high ports excluding 54543–54545 and the local management port. A relay port also requires --allow-host.", "54543〜54545とローカル管理用を除き、重複しない高位ポートを選んでください。中継ポートには --allow-host も必要です。"},
		"setup_port_unavailable":                {"The selected listener is unavailable. Check the original session first. For a fresh guided review, enter p at the final review to edit ports, then approve the exact new endpoints. No automatic remapping or firewall change occurs.", "選択した待受を利用できません。先に元のセッションを確認してください。新しい案内付きの確認では、最終確認の p でポートを編集し、新しい正確な待受を承認します。自動変更やファイアウォール変更はしません。"},
		"setup_no_permitted_host":               {"No permitted, reachable relay was found; the cause is not confirmed. If both devices declined, allow one device to relay in a fresh review after cleanup. Start fresh setup on both devices; existing connections must keep their approved host.", "許可され、到達可能な中継役が見つかりませんでした。原因は未確認です。両方で許可しなかった場合は、終了処理後の新しい確認で片方を中継に使うことを許可してください。両端末で新しく開始し、既存の接続では承認済みのホストを維持します。"},
		"setup_selection_locked":                {"This session is bound to another selection. Check its original state and cancel it explicitly before choosing a different device.", "このセッションは別の選択に固定されています。元の状態を確認し、別の端末を選ぶ前に明示的に取り消してください。"},
		"setup_scope_mismatch":                  {"The selected contact is outside this scope or refers to this same session. Review the intended contact without broadening permission.", "連絡先がこの範囲外か、自分自身のセッションです。許可範囲を広げず、接続する連絡先を確認してください。"},
		"setup_saved_authority_review_required": {"Saved peers or permissions require separate review. Preserve this profile and review the existing network before nearby setup.", "保存済みの相手や権限を別途確認する必要があります。このプロファイルを保持し、接続設定の前に既存ネットワークを確認してください。"},
		"setup_existing_anchor_conflict":        {"The existing relay or destination permission differs from this plan. Preserve active connections and review advanced network settings explicitly.", "既存の中継や接続先の許可がこの計画と異なります。現在の接続を保持し、詳細ネットワーク設定を明示的に確認してください。"},
		"setup_connection_failed":               {"Mutual connectivity could not be confirmed. Inspect both devices and this original session; pairing alone is not a confirmed connection.", "相互の接続を確認できませんでした。両端末と元のセッションを確認してください。ペア成立だけでは接続確認になりません。"},
	}
	for key, value := range additional {
		messages[key] = value
	}
	for _, key := range []string{"setup_candidate_expired", "setup_session_changed", "setup_session_expired", "setup_plan_changed", "setup_plan_expired", "setup_scope_changed", "setup_listener_changed", "setup_state_changed", "setup_authority_changed"} {
		messages[key] = messages["setup_stale"]
	}
	messages["setup_candidate_expired"] = [2]string{"This nearby device changed or is no longer available. Check the original nearby status, cancel its temporary setup, then search and confirm again on both devices.", "近くの端末の情報が変わったか、利用できなくなりました。元の nearby status を確認して一時的なセットアップを取り消し、両端末で改めて探索・確認してください。"}
	messages["setup_session_expired"] = [2]string{"This setup session expired. Check its original status and cleanup, then start a fresh setup on both devices with new consent. Do not repeat the expired approval.", "セットアップの有効期限が切れました。元の状態と終了処理を確認し、両端末で改めて許可して新しく開始してください。期限切れの承認を繰り返さないでください。"}
	for _, key := range []string{"setup_commit_uncertain", "setup_cleanup_unconfirmed"} {
		messages[key] = messages["setup_recovery_required"]
	}
	messages["setup_restart_required"] = messages["setup_stop_required"]
	pair, ok := messages[code]
	if !ok {
		pair = [2]string{"Setup could not finish. Check the original session and both devices before changing scope or trying again.", "接続を完了できませんでした。範囲の変更や再試行の前に、元のセッションと両端末を確認してください。"}
		if !validErrorCode(code) {
			code = "setup_control_unavailable"
		}
	}
	return &nearbyCLIError{code, text(ja, pair[0], pair[1])}
}
func nearbyControlError(ja bool, err error) error {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && validErrorCode(coded.ErrorCode()) {
		return nearbyError(ja, coded.ErrorCode())
	}
	return nearbyError(ja, "setup_control_unavailable")
}
func nearbyStateLabel(ja bool, state string) string {
	labels := map[string][2]string{
		"idle": {"Not started", "未開始"}, "discovering": {"Looking for nearby devices", "近くの端末を探索中"},
		"selecting": {"Choose the other device on both screens", "両画面で相手を選択してください"}, "authenticating": {"Checking the selected device", "選択した相手を確認中"},
		"awaiting_approval": {"Compare and approve on both devices", "両端末で照合・承認してください"}, "preparing": {"Preparing the approved connection", "承認した接続を準備中"},
		"connecting": {"Checking mutual connectivity", "相互の接続を確認中"}, "connected": {"Connection confirmed", "接続確認済み"},
		"recovery_required": {"Saved state needs review", "保存状態の確認が必要"}, "cancelling": {"Stopping setup; listener cleanup pending", "接続設定を終了中・待受の終了確認待ち"}, "cancelled": {"Cancelled", "取消済み"},
		"needs_restart": {"Restart required", "再起動が必要"}, "expired": {"Expired", "期限切れ"}, "stop_required": {"Stop and restart offline first", "先に停止・オフライン起動が必要"}, "failed": {"Could not connect", "接続できません"},
	}
	if pair, ok := labels[state]; ok {
		return text(ja, pair[0], pair[1])
	}
	return text(ja, "Unknown state", "状態不明")
}
func nearbyNextActionLabel(ja bool, action string) string {
	labels := map[string][2]string{
		"start_new_setup":          {"The previous receipt session has closed; explicitly review fresh setup inputs to connect another device. The existing LAN and saved pair remain", "前の確認応答セッションが終了しました。別の端末を接続するには新しい入力を明示的に確認してください。既存のLAN接続と保存済みのペアは維持します"},
		"wait_for_cleanup":         {"Wait for owner cleanup, then check this original session; stopping is not yet confirmed", "終了処理を待ち、元のセッションを照会してください。停止はまだ確認できません"},
		"choose_scope":             {"List nearby scopes, then review one exact local scope before starting", "nearby scopes で一覧を表示し、開始前に正確なローカルの範囲を一つ確認してください"},
		"select_peer":              {"Choose the intended unverified device", "接続する未検証の端末を選んでください"},
		"select_on_both_devices":   {"Choose each other on both screens, then refresh", "両画面で互いを選んでから更新してください"},
		"compare_full_fingerprint": {"Independently compare all fingerprint digits and explicitly approve this revision", "独立した方法で指紋の全桁を照合し、この版を明示的に承認してください"},
		"wait_for_peer":            {"Keep the other device open and refresh the original session", "相手の画面を開いたまま、元のセッションを更新してください"},
		"review_recovery":          {"Keep the profile and review pairing and saved state on both devices", "プロファイルを保持し、両端末のペアと保存状態を確認してください"},
		"restart_offline":          {"Explicitly stop and restart the same profile offline", "同じプロファイルを明示的に停止し、オフラインで再起動してください"},
		"review_existing_network":  {"Review the saved network before any change; keep current profile data", "変更前に保存済みネットワークを確認し、現在のプロファイルを保持してください"},
		"try_manual":               {"Privately exchange contact text/files; this does not bypass network restrictions", "非公開の方法で連絡先の文字列・ファイルを交換してください。通信制限は解除しません"},
		"try_again":                {"Review this original session and both devices before explicitly starting a new review", "元のセッションと両端末を確認してから、新しい確認を明示的に開始してください"},
		"done":                     {"Connection setup finished; receiving trust remains separate", "接続設定が完了しました。受信の信頼は別操作です"},
	}
	if label, ok := labels[action]; ok {
		return text(ja, label[0], label[1])
	}
	return text(ja, "Check the original session before any further action", "次の操作の前に元のセッションを照会してください")
}
func nearbyYes(ja bool, v bool) string {
	if v {
		return text(ja, "yes", "はい")
	}
	return text(ja, "no", "いいえ")
}
func nearbyWriteScope(out io.Writer, ja bool, scope nearbyScope) error {
	_, err := fmt.Fprintf(out, "%s: %s | %s: %d (%s) | %s: %s | %s: %s\n", text(ja, "Scope", "範囲"), displayText(scope.ScopeID), text(ja, "Interface", "インターフェース"), scope.InterfaceIndex, displayText(scope.Interface), text(ja, "Local address", "ローカルアドレス"), displayText(scope.LocalAddress), text(ja, "Allowed prefix", "許可する範囲"), displayText(scope.Prefix))
	return err
}
func nearbyWriteHuman(out io.Writer, dir string, ja bool, s nearbySnapshot) error {
	if s.Version == 2 {
		return nearbyWriteV2Human(out, dir, ja, s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", text(ja, "Nearby setup", "近くの端末との接続"), nearbyStateLabel(ja, s.State))
	if s.SessionID != "" {
		fmt.Fprintf(&b, "%s: %s\n%s: %s\n", text(ja, "Session", "セッション"), displayText(s.SessionID), text(ja, "Expires", "有効期限"), s.ExpiresAt.Local().Format(time.RFC3339))
	}
	if s.Scope != nil {
		nearbyWriteScope(&b, ja, *s.Scope)
	}
	if s.Selected != nil {
		fmt.Fprintf(&b, "%s: %s (%s)\n", text(ja, "Selected device", "選択した端末"), displayText(s.Selected.Alias), displayText(s.Selected.Bootstrap))
	}
	if s.ReasonCode != "" {
		fmt.Fprintf(&b, "%s: %s (%s)\n", text(ja, "Reason", "理由"), nearbyError(ja, s.ReasonCode), displayText(s.ReasonCode))
	}
	if s.Discovery.State == "unavailable" {
		fmt.Fprintln(&b, text(ja, "Discovery is unavailable. Exchange the contact text or a file privately. A contact does not bypass network restrictions.", "探索を利用できません。連絡先の文字列やファイルを非公開の方法で交換してください。連絡先はネットワークの制限を解除しません。"))
	}
	for i, c := range s.Candidates {
		fmt.Fprintf(&b, "  %d. %s | %s | %s: %s | %s\n", i+1, displayText(c.Contact.Alias), displayText(c.Contact.Bootstrap), text(ja, "Candidate", "候補"), displayText(c.CandidateID), text(ja, "unverified", "未検証"))
	}
	if len(s.Candidates) == 0 && (s.State == "discovering" || s.State == "selecting") {
		fmt.Fprintln(&b, text(ja, "No device found yet. Open nearby setup on the other device, or exchange contact text/files. The cause is not known.", "まだ端末が見つかりません。相手でも接続画面を開くか、連絡先の文字列・ファイルを交換してください。原因は未確認です。"))
	}
	if s.ContactText != "" {
		fmt.Fprintln(&b, text(ja, "Public short-lived contact (copy this line; it does not prove identity):", "短命の公開連絡先（次の1行をコピー。本人確認の証明ではありません）:"))
		fmt.Fprintln(&b, s.ContactText)
	}
	if s.Plan != nil {
		p := s.Plan
		nearbyWriteApprovalEffect(&b, dir, ja)
		fmt.Fprintf(&b, "%s: %d | %s: %s\n", text(ja, "Plan revision", "計画の版"), p.Revision, text(ja, "Backend", "接続方式"), p.Backend)
		for _, peer := range p.Participants {
			fmt.Fprintf(&b, "  %s: %s | %s: %s\n    %s: %s | %s: %s | %s: %s\n", text(ja, "Device", "端末"), displayText(peer.Alias), text(ja, "Identity", "識別情報"), displayText(peer.Key), text(ja, "Bootstrap", "接続待受"), displayText(peer.Bootstrap), text(ja, "Scope", "範囲"), displayText(peer.Scope.Prefix), text(ja, "May host relay", "中継役の許可"), nearbyYes(ja, peer.AllowHost))
		}
		fmt.Fprintf(&b, "%s: %s\n%s: %s\n%s: %s\n%s: %s\n", text(ja, "Relay host identity", "中継役の識別情報"), displayText(p.HostKey), text(ja, "Exact relay", "正確な中継先"), displayText(p.Relay.Address), text(ja, "Certificate SHA-256", "証明書SHA-256"), displayText(p.Relay.CertificateSHA256), text(ja, "Plan expires", "計画の有効期限"), p.ExpiresAt.Local().Format(time.RFC3339))
		fmt.Fprintln(&b, text(ja, "Compare the complete SHA-256 fingerprint through an independent channel on both devices:", "独立した方法で、両端末のSHA-256指紋の全桁を照合してください:"))
		fmt.Fprintln(&b, p.Fingerprint)
		fmt.Fprintln(&b, p.VerificationText)
	}
	fmt.Fprintf(&b, "%s: %s | %s: %s\n%s: %s | %s: %s | %s: %s\n", text(ja, "Local approval", "この端末の承認"), nearbyYes(ja, s.LocalApproved), text(ja, "Other device approval", "相手の承認"), nearbyYes(ja, s.RemoteApproved), text(ja, "Paired", "ペア成立"), nearbyYes(ja, s.Paired), text(ja, "Mutual connection confirmed", "相互の接続確認"), nearbyYes(ja, s.Connected), text(ja, "Receiving trust", "受信の信頼"), nearbyYes(ja, s.Trusted))
	fmt.Fprintln(&b, text(ja, "Discovery and pairing never grant receiving trust. To receive messages or file offers, approve the exact authenticated identity separately with soba trust. Autosave and service sharing are separate.", "探索やペアリングでは受信を信頼しません。メッセージやファイルの申込みを受けるには、認証済みの正確な識別情報を soba trust で別途許可してください。自動保存とサービス共有も別操作です。"))
	if s.Process.State == "stop_required" || s.State == "stop_required" {
		fmt.Fprintln(&b, nearbyError(ja, "setup_stop_required"))
		fmt.Fprintln(&b, cliCommandExample(dir, "stop"))
		fmt.Fprintln(&b, cliCommandExample(dir, "start", "--offline"))
	} else if s.Process.State == "needs_restart" {
		fmt.Fprintln(&b, nearbyError(ja, "setup_needs_restart"))
		fmt.Fprintln(&b, cliCommandExample(dir, "stop"))
		fmt.Fprintln(&b, cliCommandExample(dir, "start"))
		fmt.Fprintln(&b, cliCommandExample(dir, "status"))
	} else if s.State == "cancelling" {
		fmt.Fprintln(&b, text(ja, "Cancellation was requested. Listener cleanup has not finished; keep checking the original session.", "取消を要求しました。待受の終了処理は未完了です。元のセッションを続けて照会してください。"))
	} else if s.State == "awaiting_approval" {
		fmt.Fprintln(&b, text(ja, "Next: compare the full fingerprint, then approve this exact revision on each device. Matching text alone does not approve.", "次: 指紋の全桁を照合し、各端末でこの正確な版を承認してください。文字列の一致だけでは承認されません。"))
	} else if s.Selected != nil && !s.Connected && (s.State == "selecting" || s.State == "authenticating") {
		fmt.Fprintln(&b, text(ja, "Next: select this device on the other screen too, then refresh this session. A timeout does not establish which direction is blocked.", "次: 相手の画面でもこの端末を選び、このセッションを更新してください。時間切れだけで通信が遮断された方向は断定できません。"))
	}
	if s.State == "connected" && s.Process.State == "ready" {
		if s.NextAction == "start_new_setup" {
			fmt.Fprintln(&b, text(ja, "To connect another device, list scopes and explicitly review a new alias, scope and relay role before starting:", "別の端末を接続するには範囲を一覧表示し、新しい表示名・範囲・中継役を明示的に確認してから開始してください:"))
			fmt.Fprintln(&b, cliCommandExample(dir, "nearby", "scopes"))
			fmt.Fprintln(&b, cliCommandExample(dir, "nearby", "start", "--alias", "NEW_ALIAS", "--scope", "SCOPE_ID", "--consent"))
		} else {
			fmt.Fprintln(&b, text(ja, "Before another setup, explicitly cancel only this receipt session and poll its status until nextAction=start_new_setup. This does not stop the established LAN or revoke saved pairs. Guided nearby can walk through the new consent:", "次の接続設定の前に、この確認応答セッションだけを明示的に取り消し、nextAction=start_new_setup になるまで状態を照会してください。成立済みのLAN接続の停止や保存済みペアの解除は行いません。nearby の案内付き操作で新しい承認へ進めます:"))
		}
		fmt.Fprintln(&b, cliCommandExample(dir, "nearby", "--interactive"))
	}
	if s.NextAction != "" {
		fmt.Fprintf(&b, "%s: %s (%s)\n", text(ja, "Next step", "次の操作"), nearbyNextActionLabel(ja, s.NextAction), displayText(s.NextAction))
	}
	statusArgs := []string{"nearby", "status"}
	if s.SessionID != "" {
		statusArgs = append(statusArgs, "--session", s.SessionID)
	}
	fmt.Fprintln(&b, cliCommandExample(dir, statusArgs...))
	if s.SessionID != "" {
		fmt.Fprintln(&b, cliCommandExample(dir, "nearby", "cancel", "--session", s.SessionID))
	}
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
func nearbyWriteApprovalEffect(out io.Writer, dir string, ja bool) error {
	var b strings.Builder
	fmt.Fprintln(&b, text(ja,
		"The setup/contact deadline is at most 120 seconds and limits this review, not the resulting connection. After both approvals, the confirmed LAN connection and selected relay listener continue beyond this short setup session until explicitly stopped. The saved network can reconnect when Sobalink starts again. Stop this same profile to end its active connections; stopping does not revoke saved pairs:",
		"最大120秒の接続設定と連絡先の期限は今回の確認の期限で、成立後の接続期限ではありません。両側の承認後に確認されたLAN接続と選択された中継待受は、短い設定セッションが終わっても明示的に停止するまで継続します。保存済みネットワークはSobalinkの次の起動時に再接続できます。現在の接続を終了するには同じプロファイルを停止してください。停止しても保存済みのペアは解除されません:"))
	fmt.Fprintln(&b, cliCommandExample(dir, "stop"))
	n, err := io.WriteString(out, b.String())
	if err == nil && n != b.Len() {
		return io.ErrShortWrite
	}
	return err
}
func nearbyWriteJSON(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }

const nearbyHelpEN = `Connect to a nearby device with a bounded, reviewed setup

  soba nearby                       Guided setup in a terminal
  soba nearby --interactive         Explicit guided setup
  soba nearby scopes [--json]       List local scope choices without starting discovery
  soba nearby start --alias NAME --scope SCOPE_ID [--allow-host] --consent [--json]
  soba nearby status [--session ID] [--qr] [--json]
  soba nearby select --session ID --candidate CANDIDATE_ID [--json]
  soba nearby manual --session ID --file CONTACT_FILE [--json]
  soba nearby contact --session ID [--output NEW_FILE] [--qr] [--json]
  soba nearby verification --session ID [--output NEW_FILE] [--qr] [--json]
  soba nearby approve --session ID --revision N --file VERIFICATION_FILE --verified [--json]
  soba nearby approve --session ID --revision N --fingerprint FULL_SHA256 --verified [--json]
  soba nearby cancel --session ID [--json]

Choose a user-supplied alias; the OS hostname is never selected automatically.
Start advertises the alias and short-lived contact only within the selected scope.
It opens a bootstrap listener; --allow-host also permits a provisional relay there.
--bootstrap-port defaults to 54546; --relay-port defaults to 54547.
Review the exact scope/address/ports before --consent. The guided flow can select one unambiguous scope for your review.
Choose y or n explicitly for the relay role; at least one device must allow hosting.
At the final guided review, p edits ports and displays the new endpoints before consent.
JSON never prompts, selects an ambiguous scope, or supplies consent.
Both devices must select each other before setup proceeds. Advertisements are unverified.
Copy contact text or save it with contact --output NEW_FILE. Manual also accepts --stdin
from a pipe. Export the full verification line with verification --output NEW_FILE.
Exchange it independently, or compare all 64 hex digits.
--verified separately attests that comparison and consents to the exact plan/revision;
matching copied text or scanning a QR alone never authorizes a connection.
No setup action grants receiving trust, autosave, or service sharing.
Cancel ends setup; it does not revoke an already established pair or disconnect it.
QR is optional; copying text/files and full-fingerprint entry work without a camera.
An unknown response is not failure proof: check the original session in the same profile.
Never blindly repeat start or replace the session. Stale revisions need a new review.
The local agent must be running; start --offline keeps saved networks stopped for setup.
When instructed, stop and restart the same profile explicitly. Approvals are process-bound;
TCP sessions do not migrate, and saved profile data is kept. No OS/firewall change is made.
The 120-second setup/contact deadline is not the connection lifetime. After both approvals,
the confirmed LAN connection and relay listener continue until explicitly stopped.
The saved network can reconnect at the next launch. Use soba --state-dir DIR stop
for the same profile to stop its active connections. Cancel is not pair revocation.
Cancellation requests cleanup; human cancel waits until cleanup has actually finished.
JSON may return cancelling/wait_for_cleanup: poll status for the original session.
Stopping the process does not revoke saved pairs.
To connect another device, guided nearby offers a separate explicit choice. Declining
leaves the current connection alone. Scripts cancel the exact previous receipt session
if still open, then poll its original ID until connected with nextAction=start_new_setup.
Only then review and start a fresh session; old approvals are never reused.
--dry-run only validates explicit local inputs. It performs no IPC, discovery or file write.
Existing soba setup --network ... commands remain available for advanced setup.`
const nearbyHelpJA = `範囲と許可を確認して近くの端末へ接続する

  soba nearby                       端末で案内付きの接続設定
  soba nearby --interactive         案内付きの接続設定を明示
  soba nearby scopes [--json]       探索を始めずローカルの範囲候補を表示
  soba nearby start --alias NAME --scope SCOPE_ID [--allow-host] --consent [--json]
  soba nearby status [--session ID] [--qr] [--json]
  soba nearby select --session ID --candidate CANDIDATE_ID [--json]
  soba nearby manual --session ID --file CONTACT_FILE [--json]
  soba nearby contact --session ID [--output NEW_FILE] [--qr] [--json]
  soba nearby verification --session ID [--output NEW_FILE] [--qr] [--json]
  soba nearby approve --session ID --revision N --file VERIFICATION_FILE --verified [--json]
  soba nearby approve --session ID --revision N --fingerprint FULL_SHA256 --verified [--json]
  soba nearby cancel --session ID [--json]

表示名は自分で指定します。OSのホスト名は自動選択しません。
開始すると、選択した範囲内だけで表示名と短命の連絡先を公開し、接続の待受を開きます。
--allow-host は同じ範囲の一時的な中継待受も許可します。
--bootstrap-port の既定は54546、--relay-port の既定は54547です。
--consent の前に正確な範囲・アドレス・ポートを確認し、案内付き操作では一つだけの範囲を選んで確認に進めます。
中継役には y または n を明示的に選び、少なくとも片方で中継を許可してください。
案内付きの最終確認では p でポートを編集し、承認前に新しい待受を確認できます。
JSONでは質問・曖昧な範囲の選択・暗黙の承認を行いません。
両端末で互いを選んでから設定を進めます。広告は未検証です。
連絡先の文字列をコピーするか、contact --output NEW_FILE で保存してください。
manual はパイプの --stdin にも対応します。verification --output NEW_FILE で照合情報を保存できます。
独立した方法で照合情報の1行を交換するか、
64桁の指紋をすべて照合します。--verified は照合の完了と正確な計画・版への承認を別途示します。
文字列の一致やQR読取りだけでは接続を許可しません。
接続操作では受信の信頼・自動保存・サービス共有を許可しません。
取消は接続設定を終了し、成立済みのペアの解除や切断は行いません。
QRは任意です。カメラがなくても文字列・ファイルの交換や全桁の入力を使えます。
応答が不明でも失敗とは限りません。同じプロファイルで元のセッションを照会してください。
確認せずに start を繰り返したりセッションを置き換えたりせず、古い版は確認し直してください。
ローカル本体の起動が必要です。start --offline なら保存済みネットワークを止めて設定できます。
案内が出た場合は同じプロファイルを明示的に停止・再起動します。承認は現在の起動中のみ有効です。
TCP接続は移行せず、保存済みプロファイルは保持します。OSやファイアウォールは変更しません。
120秒の接続設定・連絡先の期限は、成立後の接続期限ではありません。両側の承認後、
確認済みのLAN接続と中継待受は明示的に停止するまで継続し、保存済みネットワークは次の起動時に再接続できます。
現在の接続は同じプロファイルの soba --state-dir DIR stop で停止します。取消はペア解除ではありません。
取消は終了処理を要求し、人向けの cancel は終了処理の完了を待ちます。
JSONの cancelling/wait_for_cleanup は未完了です。元のセッションの状態を続けて照会してください。
本体を停止しても保存済みのペアは解除されません。
別の端末を接続する場合、案内付きの nearby で明示的に選択できます。断った場合は既存の接続を維持します。
スクリプトでは前の確認応答セッションが開いていれば正確なIDを指定して取り消し、元のIDの状態が
connected・nextAction=start_new_setup になるまで照会します。その後、新しい入力と承認で開始してください。
以前の承認は再利用しません。
--dry-run は明示したローカル入力だけを検証し、本体への接続・探索・ファイル書込みを行いません。
詳細設定には従来の soba setup --network ... も引き続き使えます。`
