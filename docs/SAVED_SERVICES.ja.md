# 保存済みサービス・グループ・タスク

[English](SAVED_SERVICES.en.md) · [利用ガイド](GENERIC.ja.md)

本体が停止中でも、選択したネットワークがオフラインでも設定を保存できます。保存・読込みだけで入口を開いたり、以前の許可を復活させたりしません。共通指定の `--offline` はプロファイルを排他ロックし、本体を起動せず保存設定だけを編集します。ネットワークの認証情報を生成せず、転送やペアリングの状態も読み込みません。本体が稼働中ならこの指定を省略してください。`soba start --offline` はネットワークに接続せずローカル管理画面を開く別の操作です。

```sh
soba --offline service save share --backend tailnet --name example-web --ports 8080 --peers PEER_ID
soba --offline service save connect --backend tailnet --name example-ssh --preset ssh --peer PEER_ID
soba --offline service show SERVICE_ID
```

結果の ID が保存設定を識別します。`service save ... --replace SERVICE_ID` は現在のリビジョンを確認し、停止中の設定を明示的に置き換えます。保存設定を1件開始するときは `soba service restart SERVICE_ID` を使います。現在のネットワークと相手の ID を再確認します。`soba service copy SERVICE_ID` は未使用の名前で別の設定を作り、開始します。


本体停止中の一覧は `soba --offline rules` を使います。[画面の保存一覧](WEB_CONTROLS.ja.md)では、保存した相手が不在でも保存のみの作成・編集・コピー・確認付き削除ができます。共通の接続先ヘルパーは[アプリ設定・RustDesk](CLIENT_HELPERS.ja.md)を参照してください。

## グループと準備完了

```sh
soba --offline group save example SERVICE_ID_1 SERVICE_ID_2
soba --offline group list
soba group start example --confirm
soba services wait SERVICE_ID_1 SERVICE_ID_2
soba group stop example
```

グループの保存だけでは何も開始しません。既存グループの置換には `--replace` が必要です。開始時には選択した設定全体を確認し、既定では保存済みの有効期間を使います。`services start`・`group start`・`task` の `--ttl` では、正の整数秒の期間を今回だけ指定でき、保存済み定義とその版は変更しません。共有は確認が必要で、非対話操作では `--confirm` を指定します。新しく開始するサービスのどれかに失敗した場合、その操作で新しく開始したサービスをすべて停止します。同じ所有者・設定ですでに稼働中のサービスは維持し、有効期間も延長しません。

準備完了は、許可された入口が利用可能になった状態です。アプリの互換性・遠隔ログインの成功・遠隔ジョブの完了は示しません。`services start`・`services stop`・`services wait` は個別の ID または `--group NAME` でも選択できます。`wait-ready` は `services wait` の別名です。`services`・`group` の開始・停止・待機に `--owner NAME` を指定すると同じ所有者だけを対象にし、更新式タスクリースを作りません。例は[案内付きCLI手順](CLI_GUIDE.ja.md)を参照してください。

## コマンドが所有する一時サービス

```sh
soba task --group example --confirm -- APPLICATION ARGUMENT...
```

確認した対象を開始して入口の準備完了を待ち、指定したローカルコマンドと引数を直接実行します。タスクは選択したサービスだけを所有し、30秒のリースを10秒ごとに更新します。コマンドの終了・失敗・取消時には所有するサービスの停止を試みます。ラッパーが消失したり、本体に停止要求が届かなかったりしても、取得済みの許可は最後の更新から最長30秒で失効します。今回の許可期間も維持し、`--ttl` を指定しなければ保存済みの期間を使います。タスクリースの更新で許可期間は延長しません。

Unix では取消時にローカルのプロセスグループを終了します。Windows では直接の子プロセスを終了します。通信を停止しても、遠隔側ですでに開始されたジョブの取消は保証されません。対象アプリ独自の取消操作を使ってください。

## 停止・削除

`soba stop-shares` はタスク所有分を含むすべての稼働中の共有を取り消し、本体と外向きの接続を維持します。`soba stop-service SERVICE_ID` は手動で所有するサービスを停止します。タスクの後片付けでは所有者を確認し、別タスクのサービスを停止しません。

```sh
soba service delete SERVICE_ID
soba service delete SERVICE_ID --apply --review REVISION
```

削除前に稼働状態と影響するグループを表示します。稼働中の許可も停止する場合は `--stop-active`、グループから参照も削除する場合は `--remove-from-groups` を追加してください。その削除で空になるグループも削除します。設定やグループが変わった場合は再確認が必要です。保存に失敗した場合、稼働中の許可は維持します。

## 非公開の書き出し・読込み

```sh
soba --offline profile export --output definitions.json
soba --offline profile import definitions.json
soba --offline profile import definitions.json --apply --review REVISION
```

書き出すのは保存済みサービスの範囲とグループだけです。ローカル端末の ID・認証情報・ログイン状態・信頼設定・LAN ペアリング情報・受信先・メッセージ・タスクの所有者やリースは含みません。サービス名・相手の参照・ポートは設定の一部として残るため、共有前に内容を確認して必要に応じて伏せてください。書き出し先は本体のデータフォルダー外の新しい非公開ファイルです。アップロードや既存ファイルの上書きはしません。

読込み前に保存設定の置換内容を表示します。確認は現在の保存先と読込み内容の両方に対応し、どちらかが変われば再確認が必要です。適用前に稼働中のサービスを停止してください。既存のローカル ID・ネットワーク設定・信頼・受信データは維持し、読込み後のサービスはすべて停止状態です。

グループ数とメンバー数には設定済みの `groups`・`groupMembers` の受入れ条件を適用します。値を下げても既存のグループやメンバーを削除せず、増加を制限します。停止状態の永続保存・リビジョン確認・開始失敗時の停止・所有権・リース失効・プロセス取消はネイティブテストで検証します。実際の登録やアプリ互換性には別途実機での確認が必要です。
