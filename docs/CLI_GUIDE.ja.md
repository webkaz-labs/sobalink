# 案内付き・自動化向けのCLI操作

[English](CLI_GUIDE.en.md) · [基本方針](DEVELOPMENT_PRINCIPLES.ja.md)

`soba connect` と `soba share` は、端末での選択・編集・確認を案内します。`status`・`peers`・保存済みサービスとグループの操作は人向け表示が基本です。`--json` を付けると安定した機械向け出力になります。接続・共有の明示指定とdry-runの結果はJSONを維持します。ここでの検証は模擬の相手と共有情報を対象としており、実際の登録・実機でのアプリ互換性・OSのライフサイクル動作を保証するものではありません。

## 最短の操作

```sh
soba init
soba start --background
soba setup --network tailnet
soba login --link --wait
soba connect
```

`init` はローカルの設定情報だけを作ります。繰り返しても既存のプロファイルを維持し、ネットワークへの登録や認証情報の作成は行いません。名前と機械向け出力を明示する場合は `init --name example-node --json` を使います。

接続では、認証済みの相手からこの端末に公開されたサービスを更新して表示します。番号で選び、`r` で更新、`m` で既知のサービスを手動設定、`q` で取消できます。通常のTailscaleアプリには、接続先にsobalinkを入れず手動設定で接続できます。

共有では対象アプリを起動し、`soba share` で許可する相手を表示番号か名前で選びます。複数の相手はカンマ区切りで指定でき、重複や一意に決まらない名前は受け付けません。共有情報の表示は明示的に選び、最小限の情報を選択した相手だけに公開します。

適用前に、相手・用途・ポート全体・ローカル入口またはアプリ側ポート・有効期間・共有情報の表示範囲・保存名を確認します。用途の候補は編集できる例です。保存名には日本語などの文字・数字・ハイフン・アンダースコアを使えます。確認画面で名前・ポート・期間・用途を編集し、相手の選択に戻り、または取り消せます。失敗時は内容を再確認して再試行するか編集できます。相手の共有許可が変わった場合は選び直してください。

対応端末では↑↓で選び、←→で文字を編集できます。番号・名前・カンマ区切りの直接入力も使えます。出力のリダイレクト時や `TERM=dumb` では通常の文字入力に切り替わります。`--interactive` は案内付き操作を明示し、`--json` は対話入力を求めず案内付き操作とは併用できません。案内付き入力では `TEA_TRACE` を拒否し、入力履歴を保存せず、不正なUTF-8・制御文字を受け付けず、終了時に端末の設定を戻します。

## 自動化向けの明示指定

```sh
soba --dry-run connect --json --name api --peer PEER_ID --ports 8080 --local-port 18080
soba connect --json --name api --peer PEER_ID --ports 8080 --local-port 18080
soba share --json --name ssh --preset ssh --peers PEER_ID --ttl 2h --discoverable
soba --json-errors status --json
soba peers --json
```

`--peer-name example-node` または `--peer-names example-node,second-node` は、現在認証されている相手の正確な名前を解決し、存在しない・重複した・一意でない名前を拒否します。オフラインの定義やネットワーク照会が不要な確認には、IDを指定する `--peer`・`--peers` を使います。

dry-run はローカル入力を検証して適用する内容を表示します。待受ポートの利用可否や相手側アプリの動作成功を保証しません。名前・JSONキー・ID・入力値は言語を切り替えても変わりません。人向けの案内とエラーは `--locale auto|ja|en`、機械向けのエラーコードは `--json-errors` で指定します。

公開されたサービスへの接続では、更新結果の同じ行にある `peerId`・`id`・`revision`・`network`・`ports` を使い、選択した共有との対応を明示します。

```sh
soba discover --json
soba connect --json --name advertised-api --peer PEER_ID --network tcp --ports 8080 --purpose web --local-port 18080 --service-id GRANT_ID --service-revision REVIEWED_REVISION
```

revision は中身を変更せず使う確認情報です。Coreが鮮度を確認し、認証済みの相手に再照会してから適用します。共有許可の作り直し・用途や接続先の変更・共有期限の短縮は拒否し、同じ共有の期限延長は許可します。案内付き操作は適用前に同じ共有を再確認するため、確認に時間をかけても対象を無言で変更しません。自動化では新しいrevisionを採用する前に確認した全項目を比較してください。

## 保存済み設定・接続先・停止

```sh
soba --offline rules
soba --offline rules --json
soba --offline settings api
soba --offline service show api
soba status
soba stop
soba stop
```

`rules` は保存済み定義を一覧表示します。service show/copy/restart/delete・settings・stop-service・サービスとタスクの選択・グループのメンバーには、正確な保存名かIDを使えます。一意でない名前やID、同じサービスの重複選択は拒否します。解決したIDと更新情報に固定し、範囲や有効期間を無言で変更しません。共通指定の `--offline` を付けると、本体を停止したままネットワークを開かず一覧・設定を確認できます。保存済みであることは待受準備完了を意味しません。`settings` ではローカルと接続先の対応・SSHのHostKeyAlias・HTTPの候補を確認できます。SSHのホスト鍵検証とTLSの証明書検証を有効のままにし、HTTPSでは元のTLS名とオリジンを維持してください。これらのサービス接続先はSOCKSプロキシではありません。

`status` は相手の範囲・実際の接続先・状態・実効の有効期限・所有者・リースと次の操作を表示します。ローカルの本体に到達できない場合、JSONでは `state: stopped` を返し、その状態で `stop` を繰り返しても成功します。権限・プロトコルなどのエラーは隠しません。別のプロファイルを選ぶ場合は、次の操作も同じ `--state-dir` で実行します。

## グループとタスクの所有者

```sh
soba group save demo api
soba --dry-run group start demo --ttl 2h --owner session-example
soba group start demo --ttl 2h --owner session-example --confirm
soba group wait demo --owner session-example
soba group stop demo --owner session-example
soba task --services api --ttl 45m --confirm -- PROGRAM ARG
```

グループ一覧と共有の確認は、サービス名と範囲を読みやすい文字で表示します。`--json` で共有を含むサービス・グループを開始する場合は明示的な `--confirm` が必要で、対話入力は求めません。

`--ttl` は今回の稼働中の許可期間だけを変え、保存済み定義とその更新情報は変更しません。正の整数秒で指定します。`--owner` はその後の停止・待機を同じ所有者に限定し、自動で更新式リースを作りません。タスクは引き続き専用の更新式リースを使い、シェルを介さず引数を実行して、終了・失敗・取消時に所有する接続を停止します。準備完了は通信の確認です。相手側のジョブ完了・取消には、そのジョブ固有の仕組みが必要です。

## 起動時の動作を確認

`soba autostart --json` はユーザー単位のサインイン時の登録内容と、公開可能な起動時の動作を表示します。saved モードでは選択済みのネットワーク・承認済みの自動受信に加え、有効で明示的に許可された起動用の接続・保存済みプロキシが開始対象になります。範囲と有効性を表示し、認証情報は含めません。範囲・保存済み認証情報の更新情報・取消状態が変わった場合は確認用のトークンを更新する必要があります。`--startup offline` はこれらを開始しません。受信側の共有や前回のファイル転送は再開しません。

個別の許可の確認・管理は[起動時の設定ガイド](STARTUP.ja.md)を参照してください。登録のプレビューではOSの変更やアプリの起動を行いません。

## 復旧と機密の入力

ポートが競合しても無言で別のポートへ変えません。確認画面で入口ポートを編集するか、競合する待受を意図的に停止してください。共有許可やネットワークが変わったら共有情報を更新し、現在のサービスを選び直します。接続先を使う間は本体と提供側アプリを起動したままにします。

認証情報や機密の招待を引数・シェル履歴・ログへ入れないでください。既存の非公開ファイル・パイプ入力と、実行時の明示的な承認を使います。この文書の例には認証情報や実在の端末設定を含めていません。

## 代替ポートの明示的な確認

停止中の保存済み送信接続では、`soba service ports NAME_OR_ID` で固定ポートを変更せず候補を確認できます。設定全体と有効期間を確認し、別の操作 `soba service restart NAME_OR_ID --local-port PORT --expected-revision REVISION` で選びます。候補は予約ではなく、開始時に全ポートの待受を再確認します。[代替ポートの確認](PORT_PROPOSALS.ja.md)を参照してください。
