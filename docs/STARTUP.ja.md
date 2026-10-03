# 明示的な起動時接続と非公開 SOCKS 設定

[English](STARTUP.en.md) · [利用ガイド](GENERIC.ja.md)

保存した定義だけでは自動開始しません。任意の起動時設定により、確認済みの正確な
送信接続だけを次回以降のオンライン起動で開始できます。受信共有やファイル転送は
自動開始しません。実装と模擬テストは完了していますが、インストール済みの実機、
登録、アプリの動作確認には別途実機検証が必要です。

## 保存済みの接続

1. 接続サービスやグループを保存し、範囲を確認します。
   `soba startup preview --name example --group example`
2. 返されたサービス範囲と有効期間をすべて確認し、結果の `revision` と
   `storeRevision` を指定して許可します。
   `soba startup save --name example --group example --review REVISION --store-review STORE_REVISION`
3. `soba startup list` で許可を確認します。以降の自動開始を無効にするには:
   `soba startup disable --name example --store-review STORE_REVISION`

個別のサービスは `--group` の代わりに `--ids ID,ID` で指定できます。保存操作自体は
接続を開始しません。定義の編集・取込み、グループの構成変更、ネットワーク・端末名の
変更、相手の許可取消しにより古い許可は無効になります。変更後の範囲は明示的に
再確認してください。プロファイルの取込みから起動許可を追加することはできません。

オンライン起動時に有効な許可を読み込み、ネットワークが準備できた後で各対象を
一度だけ開始します。明示的なグループ開始と同じ範囲確認、入口の準備確認、失敗時の
取消しを使います。失敗した開始は、明示的な操作または次の本体起動まで再試行しません。
明示的に停止した接続をすぐに自動開始することもありません。`soba start --offline`
では、その後同じ本体内でネットワークを設定しても起動時接続を抑止します。

明示的な本体起動では、有限の有効期間を新たに開始します。通信の復旧では既存の
絶対期限を維持し、期限切れや明示的に停止した許可を更新しません。OS ログイン時の
本体起動は独立した任意設定です。`soba autostart --help` を参照してください。

## 非公開 SOCKS 設定

通常の `soba proxy start` は実行時だけの設定です。任意の保存には非公開ファイルまたは
パイプを使います。

- `soba proxy preview --name example --peer PEER_ID --ports 443,8443`
- `soba proxy saved`
- `soba proxy save --json-file PRIVATE_FILE` または `soba proxy save --stdin`
- 強力な認証情報の明示的な生成: `soba proxy generate --json-file PRIVATE_FILE`

save の入力は確認済みの `scope`、preview の `expectedRevision`、`proxy saved` の
`expectedStoreRevision`、`username`、`password`、明示的な `startOnLaunch: true`
または `false` です。generate は認証情報を入力せず、同じ確認項目を指定します。
144 ビットのランダムなユーザー名と 256 ビットのランダムなパスワードを生成し、
この明示的な操作でのみ保存します。通常の応答には認証情報を含めません。保存・生成
操作はプロキシを開始しません。

`proxy saved` に表示される各設定の `revision` で操作します。

- `soba proxy saved-start NAME --review REVISION`
- `soba proxy saved-disable NAME --review REVISION`
- `soba proxy saved-delete NAME --review REVISION`
- `soba proxy reveal NAME --review REVISION --private-file PATH`

reveal は指定した非公開ファイルに認証情報を直接保存します。汎用コマンドで通常の
出力先へ認証情報を表示することはできません。確認したファイルも非公開にしてください。
削除すると、その保存設定で開始した接続も停止します。disable は以降の自動開始と
その保存許可からの復旧を無効にします。稼働中の入口は `soba proxy stop ID` で停止できます。

保存する範囲は正確な端末 ID、TCP ポート、ループバックの入口、ネットワーク、
端末名、有効期間です。現在の端末 ID の検証、netstack、ループバックの制約を維持します。
許可取消しは再起動後も古い許可を無効にし、相手を再承認しても復活しません。
取消しの永続保存を確認できない場合は関連する接続を停止し、現在の本体内では
起動時接続を抑止します。エラーの案内に従い、再起動前に非公開保存領域を修復してください。
保存に失敗した状態を、永続的な取消し完了とは表示しません。

起動許可、取消し記録、認証情報はプロファイルの書き出しと別の非公開保存領域に
置きます。各ファイルの容量には有限の `profileBytes` を使い、原子的に非公開保存します
（Windows は現在のユーザーだけの ACL）。通常の状態表示、履歴、ログ、エラー、
dry-run に認証情報は含まれません。引数、共有設定、ソースコード、書き出した
プロファイルに認証情報を含めないでください。

## 検証範囲

Core/CLI の模擬テストは、固定した範囲、古い確認結果、グループ変更、受信共有の拒否、
オフライン時の抑止、一度だけの開始、無効化、停止、永続的な相手の取消し、失敗処理、
認証情報の生成、非公開確認、秘匿、既存の期限を維持したプロキシ復旧を対象にします。
実際の登録、OS ログイン・復帰、インストール後の ACL、アプリ互換性の証明ではありません。
ACL の検証にはネイティブ Windows CI も必要です。
