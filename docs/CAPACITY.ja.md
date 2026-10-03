# 容量・有効期間・履歴

[English](CAPACITY.en.md) · [使い方](GENERIC.ja.md) · [安全性](SECURITY.ja.md) · [API の契約](../web/API.md)

制限には異なる役割があります。論理制限を外しても、メモリー・ディスク・ソケットや OS の実行時間が無制限になるわけではありません。指定した選択・実効値・現在の使用量を合わせて確認します。

| 種類 | 選択肢 | 意味 |
| --- | --- | --- |
| 論理ポリシー | `default`、正の `limited`、明示した `unlimited` | 件数・容量・履歴整理の目安・操作期限 |
| 資源予算 | `default` または正の有限 `limited` | 保存容量・メタデータ・待受・通信・キュー・応答ページは有限。`unlimited` は拒否 |
| サービスの有効期間 | 有限期間、外向きの `until-stopped`、共有の `until-revoked` | 容量と独立した明示的な許可期間 |
| 通信形式・安全性の条件 | 容量の好みではない | ポート・ID・パスの形式、本人確認と範囲、予約入口、ローカル管理、非上書きは常に有効 |

`default` はそのビルドの表示される初期値で、0や無制限ではありません。有限値は JSON で正確に扱える正の整数、期間は内部表現内の整数秒です。置き換えるポリシーで省略した項目は初期値へ戻ります。版は `1` で、未知の項目・重複・null・曖昧な指定は拒否します。正確なビルドの `requested`・`effective`・`catalog`・`adjustable`・`usage` は `policy.config` で確認します。

## 初期値は固定の製品上限ではない

| 論理設定 | 初期値 |
| --- | --- |
| `savedServices`・`trustedPeers`・`sharePeers` | 保存定義64件、信頼／ペア相手128件、共有相手32件 |
| `rangePolicies`・`portIntervals` | 範囲ポリシー64件、設定した区間256件 |
| `groups`・`groupMembers` | グループ32件、各グループ64項目 |
| `batchEntries`・`fileBytes`・`batchBytes` | ディレクトリ込み256項目、1ファイル・一括それぞれ1 GiB |
| `pathDepth`・`pathBytes` | 移植可能な相対パスごとに16階層・UTF-8で4096バイト |
| `messageBytes` | 復号した文字列16 KiB |
| `transferHistoryEntries` | 方向ごとに転送履歴32件 |
| `messageHistoryEntries`・`messageHistoryBytes`・`messageHistoryAgeSeconds` | 整理候補の目安：128件、符号化した履歴48 KiB、30日 |
| `stagingSeconds`・`receiveWaitSeconds`・`fileTransferSeconds` | 各操作600秒 |

上の論理設定は正の有限値または明示的な `unlimited` へ変更できます。低くした値は新規受付へ適用し、保存済みの項目や開始済みの処理を追い出しません。履歴の目安は確認付き削除の候補を選ぶだけで、設定変更だけでは履歴を自動削除しません。タスクのリースとサービス許可期間は操作期限とは別です。

資源予算はそれぞれ調整でき、常に有限です。主な初期値は次のとおりです。

| 資源設定 | 初期値 |
| --- | --- |
| `profileBytes`・`lanStateBytes`・`messageStorageBytes` | プロフィール4 MiB、非公開 LAN 状態2 MiB、文字履歴4 MiB |
| `messageTextBytes` | 復号した文字列の保存・通信枠16 KiB |
| `materializedListeners` | UDP 共有・ローカル接続・任意のプロキシ待受で合計64件 |
| `tcpConnections`・`tcpPerPolicy`・`tcpPerPeer` | 全体512、ポリシーごと128、相手ごと64接続 |
| `udpSessions`・`udpPerPolicy` | 全体512、ポリシーごと256セッション |
| `udpQueuedBytes`・`udpPolicyQueuedBytes`・`udpQueuePackets` | 全体16 MiB、ポリシーごと1 MiB、キューごと64パケット |
| `transferSpoolBytes`・`receiveReservedBytes` | 送信一時保存4 GiB、受信予約4 GiB |
| `transferManifestBytes`・`transferMetadataBytes` | マニフェストの計上256 KiB、保持する転送メタデータ1 MiB |
| `transferPending`・`transferPendingPerPeer` | 待機転送32件、相手ごと8件 |
| `transferConcurrentFiles`・`transferConcurrentPerPeer` | 同時ファイル4本、相手ごと2本 |
| `stagingInventoryEntries`・`stagingInventoryDepth` | 一時保存の調査100,000項目・64階層 |
| `discoveryBytes`・`pageBytes`・`pageEntries` | 探索応答256 KiB、ローカルページ1 MiB・128項目 |

実際のディスク・メモリー・ファイル記述子・OS の制約で、選んだ予算より前に失敗する場合があります。これは計上する資源の境界で、プロセス全体のヒープや RSS の上限保証ではありません。パスの階層と全体の UTF-8 長は論理設定で、初期値は16階層・4096バイトです。無制限でも有限のマニフェスト予算は残り、実効パス長は `transferManifestBytes` 以下、実効階層もそのバイト数に収まる構成要素数以下です。マニフェスト全体と保持メタデータにも容量が必要です。各構成要素255バイト以内、危険な名前・予約名・絶対パス・トラバーサル・NUL・バックスラッシュ・リンクの拒否は容量によらず維持します。ポリシーが許可する長いパスでも実際の OS・ファイルシステムが拒否する場合があります。大きい設定の試験は全 OS の長いパス対応を証明しません。ローカルの絶対受信先フィールドには、現在は別の4096バイトの入力検証制限があります。`pathBytes` は送る相対パスに適用し、この受信先には適用しません。一時保存の調査階層は別の有限な探索予算です。

`profileBytes` は非公開の起動許可・保存プロキシ・取消し記録の各ファイルにも個別に適用します。使用量は秘密を表示せず各ファイルのバイト数を示し、保存済みのいずれかのファイルより小さい値には変更できません。ディレクトリ全体を合算した上限ではありません。

設定が組み合わさる例：

- 2 GiB のファイルには `fileBytes`・`batchBytes` と、送信側の一時保存・受信側の予約容量の両方が必要です
- `batchEntries` を無制限にしても、有限のマニフェスト・保持メタデータ予算は残ります。ディレクトリも項目に数えます。送信元の列挙で保持する祖先・パス情報も `transferManifestBytes` に計上します
- `messageBytes` を無制限にしても `messageTextBytes` は残ります。文字数を増やすには履歴保存容量の見直しも必要な場合があり、通信枠は JSON のエスケープを考慮します
- 相手やサービスの件数を無制限にしても、保存するプロフィール・LAN 状態には有限の容量が必要です。件数を下げても既存の記録を残し、既存の保存データより小さい保存容量への変更は拒否します
- TCP は除外・内部予約入口を除いた1〜65535番を、ポートごとの待受を作らず扱えます。UDP とローカル接続は実待受を作るため、有限の待受予算を使います
- 探索は有限の件数ずつ順に巡回します。多数の相手を1回ですべて調べるわけではなく、保存済みの相手がオンラインとは限りません

## 確認して適用する

ローカル画面から容量を選択し、変更を確認できます。詳細な CLI 操作も同じ Core を使います。

```sh
soba command policy.config '{}'
soba command policy.preview --json-file capacity-change.json
soba command policy.apply --json-file capacity-apply.json
```

現在の requested を基準に、維持したい選択を残して完全なポリシーを作り、変更ファイルの `policy` に入れます。次の例は表示した値だけを増やし、省略した項目は初期値へ戻す置換です。

```json
{"policy":{"version":1,"logical":{"sharePeers":{"mode":"limited","value":64}},"resources":{"profileBytes":{"mode":"limited","value":8388608}}}}
```

返された実効値と使用量を確認します。適用ファイルには同じ `policy` と、確認結果の `revision` をコピーした `expectedRevision` を入れます。ポリシー・保存済みプロフィール・提案内容が変われば再確認が必要です。確認だけでは変更せず、保存成功後に選択を反映します。新規受付に予算を使い、使用中の資源は引き続き計上します。容量を増やしても新しい許可・待受・転送は始まりません。

## 有効期間と取消

共有の既定は有限の1時間です。`--ttl 72h` も指定でき、固定の24時間上限はありません。期限のない共有には `--lifetime until-revoked` を明示します。外向き接続の既定は `until-stopped` で、有限の `--ttl` も選べます。保存済み定義には選択を残しますが、それだけで再開始を許可しません。別に確認した[外向き起動時接続](STARTUP.ja.md)は次回オンライン起動で新たな有限期間を開始でき、受信共有は手動のままです。再接続で許可を更新しません。

ローカル一時保存はブラウザー・CLI とも既定600秒です。明示的な無制限はその操作のポリシー期限だけを外し、取消・本体終了・保存容量・ネットワーク失敗の条件は残します。受信承認待ちとファイル転送には別の設定があります。一時保存の期限なしは、再起動をまたぐオフライン送信箱や、切れたブラウザー要求の再開を保証しません。

## 履歴は確認して削除する

新しい文字の受信や整理目安の変更だけでは、過去の文字を削除しません。画面の整理プレビュー、または詳細コマンドを使います。

```sh
soba command message.history.preview '{}'
soba command message.history.cleanup '{"expectedRevision":"REVISION"}'
```

正確な対象と件数を確認してから `REVISION` を置き換えます。版は現在の履歴・ポリシー・削除候補に結び付き、変化したら再確認します。保存成功後にだけメモリー上の履歴を変えます。先に保存容量が尽きた場合は、古い記録を消す代わりに送受信が容量エラーになることがあります。相手の受信確認後にローカル履歴の保存が失敗しても、相手へ未到着とは限りません。失敗した段階を確認してから再試行してください。

転送履歴は別です。`soba forget TRANSFER_ID` は終了済み記録を明示的に消し、受信ファイルは消しません。一時保存を解放すると payload の予約容量は戻りますが、削除失敗分は成功するまで計上します。再起動後の転送進捗の再開はありません。[転送の動作](GENERIC.ja.md#再試行と片付け)
