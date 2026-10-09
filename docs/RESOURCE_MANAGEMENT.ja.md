# 相手1台の転送設定を確認して管理する

[English](RESOURCE_MANAGEMENT.en.md) · [参照のみの許可](RESOURCE_INSPECTION.ja.md)

このソース開発では、ペアになった DirectLAN の相手1台について、`transferConcurrentFiles` と `transferConcurrentPerPeer` の2項目だけを、明示的な管理許可で変更できます。統合レビュー中です。純粋なフェンス・世代の試験だけでは、遠隔管理の全体動作、配布、実端末、アプリ、OS ログイン、スリープからの復帰の受入は確認できません。公開版の検証状況は[検証記録](VERIFICATION.md)に従います。

## 対象端末で正確な範囲を許可する

1. 保存状態を所有する本体を起動し、`soba resource list` で対象のリソース ID を確認します。
2. 有効な参照許可または管理許可がある場合、現在の ID・変更番号を使って先に明示的に失効させます。範囲は自動で変更されません。
3. `soba resource grant preview --management --id RESOURCE_ID --peer PEER_KEY --expires-at RFC3339_EXPIRY` を実行します。
4. 正確な対象と相手の鍵、不変のペア接続関係、転送の両項目、`inspect`・`preview`・`apply`・`operation.status` の全範囲、有限の期限、`initializesState`・`upgradesFormat` を確認します。この全範囲は正の有限値と既定値の選択の両方を許可します。変更していない確認用 JSON 全体を非公開ファイルに保存します。
5. `soba resource grant confirm --management --review-file REVIEW.json --confirm` を実行します。
6. `soba resource grant inspect --id RESOURCE_ID` を確認します。保存済みの許可・`activation`・`listenerReady` は異なる状態です。接続口の準備完了は遠隔利用の成功を保証しません。

管理許可の確定時には、必要な場合に許可の保存形式をバージョン3へ更新し、保持済みの記録と元の有効期限を維持します。以降はバージョン2だけを扱う旧版本体で読めません。preview や再起動では形式を更新しません。失効させても形式は戻りません。旧版へ戻したり許可を復旧したりするために非公開の保存状態を削除しないでください。

管理には両端末で通信方式 v2 が必要です。専用の管理用接続口は、指定内容を読む前に v1 を明示的に拒否します。明示的に失効して管理許可を再発行した後は、旧参照クライアントではこの接続口を使えません。`resource remote manage` を使い、自動で旧方式へ切り替えません。従来の参照のみの許可と `resource remote inspect` の v1 動作は維持します。

## 許可された相手から適用前に確認する

対象端末が示した正確なリソース ID・管理許可 ID・現在の許可の変更番号を使います。

```text
soba resource remote manage inspect --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION
soba resource remote manage preview --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --concurrent-files default --concurrent-per-peer 2
```

inspect は指定値と実効値だけを返します。preview は読み取り専用で、操作枠を予約しません。両項目の指定値・実効値を確認してください。不透明な `operationId`・`baseRevision`・`reviewRevision` は範囲・現在の状態・認証済みの世代に結び付き、特定の TCP 接続には依存しません。

```text
soba resource remote manage apply --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --operation-id OPERATION_ID --base-revision BASE_REVISION --revision REVIEW_REVISION --concurrent-files default --concurrent-per-peer 2 --confirm
```

preview の正確な値と両項目を指定します。`--revision` は `reviewRevision` です。apply は勝手に新しい preview を作ったり現在の変更番号へ差し替えたりしません。他の操作・再起動・関連する認証済み世代の変更で、未使用の確認内容が無効になることがあります。項目を変える場合は確認し直します。`--dry-run` はローカルの入力検査のみで、相手側の許可や準備完了を証明しません。JSON は言語に依存せず、ヘルプとエラーは日本語・英語に対応します。

初めて受理した新規の遠隔適用は、操作記録を v1 から v2 に変換し、保持済みのローカル記録と通算番号を維持します。これは許可の保存形式の更新とは別です。変換は新しい操作予定より先に永続化し、変換だけでは設定変更の実行を示しません。v1 だけを扱う旧版本体では読めなくなります。通常のローカル操作・inspect・preview・status では変換しません。

## 応答が不明な場合に確認する

```text
soba resource remote manage status --peer TARGET_PEER_KEY --id RESOURCE_ID --grant-id GRANT_ID --grant-revision REVISION --operation-id OPERATION_ID
```

apply の応答を失った場合や結果が不明な場合は、同じ不透明な操作 ID を使います。失敗したと決め付けたり、別の操作を勝手に作ったり、確認せず適用し直したりしないでください。保持済みの同一要求は過去の記録を返し、設定変更を再実行しません。同じ ID に別の内容を指定すると拒否します。現在の status の指定で、保存済みの元の許可主体を書き換えません。

操作応答は過去の結果と `evidenceDurable` だけを含みます。このフラグは確定した記録の永続化を示し、設定適用やファイル転送の成功を保証しません。`unknown` は成功でも元への復元でもありません。status は現在の設定を追加しません。別途 inspect で確認します。未作成・保持対象外・ローカル主体・別範囲の記録は、同じ取得不可として扱います。新しい許可や別のペアは以前の範囲の履歴を引き継ぎません。

非公開の操作記録は、ローカルと遠隔で1つの通算番号と固定上限を共有します。未解決の操作予定や unknown は削除対象にしません。保存が不確かな場合、新規適用を停止します。保存状態を削除したり、取得不可を操作の作り直しの許可と解釈したりしないでください。

## 期限・取り消し・境界

- 通常のメッセージ・ファイルの許可で管理許可を作成しません。相手への許可全体を取り消す revoke は管理許可も失効させます。メッセージ・ファイルの一時停止やサービス共有の停止は対象外です。
- 元の有限の期限と起動時の単調時計の上限を再接続後も維持します。再ペアリングで許可を再利用しません。検出した時計の巻き戻りや保存状態の不確実性・置換により、新規処理と開示を拒否します。停止中の期限は対象端末の時計に依存し、保存状態全体の巻き戻しは確実に検出できません。
- 操作予定を永続化した後、設定変更の実行を1回だけ受け付けます。受付前の失効や取り消しは実行を拒否します。受付済みの作業は失効・切断後も完了して結果を同期的に保存する場合があります。別途確認する応答は、その後に開示されない場合があります。
- 接続の復旧を含め、固定の合計15秒以内にアプリケーション要求を最大1件だけ送ります。apply の自動再送や OS の通信経路への切り替えは行いません。
- 所有するユーザー空間の接続口は参照用とポート54546を共有し、既存の所有者を維持します。ローカル管理 API は公開しません。任意のコマンド・パス・認証情報・主体の自己申告・一覧・操作記録全体の件数・ローカル操作 ID は受け付けたり開示したりしません。
- 失効の保存失敗は永続的な成功ではなく、再起動後に失効を維持できない場合があります。保存が不明な場合、再起動前に非公開の保存状態を確認してください。
