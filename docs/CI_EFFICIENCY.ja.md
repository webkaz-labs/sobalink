# 変更範囲に合わせたCI

[English](CI_EFFICIENCY.en.md) · [検証](VERIFICATION.md) · [開発原則](DEVELOPMENT_PRINCIPLES.ja.md)

変更の影響がある検証を実行します。通常の文章修正にアプリのビルドや4環境の試験は不要です。フロント変更ではフロントとブラウザーを確認し、通信・共通処理・影響不明の変更では全件を実行します。プレリリースは引き続き、独立した全件検証と配布物・署名・実インストールの検証を行います。

## 実行範囲

| 変更全体 | 実行する検証 |
| --- | --- |
| 通常のルートREADME/SECURITY、直下の `docs/*.md` の説明文 | 軽いGit差分判定と必須文書の存在確認。アプリ試験・ビルド・配布物作成は実行しない |
| `web/src` のTypeScript/TSX/CSS、既存の `web/browser/*.mjs` のブラウザー試験コード。通常文書との混在も可 | Linuxでフロント単体試験、同一lockからの2回の再生成一致、fixture安全性試験、実ブラウザー試験 |
| 上記に伴う生成済み `web/dist` の変更 | フロントソース変更がある場合のみフロント範囲。再生成の一致は必須 |
| `internal/servicepresets` または `internal/boundedlog` の確認済みGo変更。通常文書との混在も可 | 変更パッケージと、テストからの参照を含む推移的な逆依存をLinuxでrace検出・vet |
| 下記の確認済みCLI表示ファイルだけを変更するPR。通常文書との混在も可 | 4ターゲットの短時間・安全性試験、ブラウザー、配布物・manifest検証。実時間lifecycle/leaseの3工程は未実行 |
| 通信、認証、Core、設定、タイマー、共通helper、依存・lock、ビルド設定、CI判定・workflow、その他不明なパス | Linux amd64/arm64、macOS arm64、Windows amd64の全native試験、ブラウザー、配布物・manifest検証 |
| フロントとGoの混在、OS固有Go、cgo、未確認のimport、方針・出所情報のMarkdown、不正なmode/type、判定できない入力 | 全件 |
| 手動の全件確認、すべてのプレリリース | 全件 |

### PR限定native-shortの段階導入

`native-short` の対象は `cmd/soba/help.go`、`cmd/soba/errors.go`、`cmd/soba/errors_test.go`、`cmd/soba/human_output.go`、`cmd/soba/human_output_test.go` の5ファイルだけです。新しいパス・import・build directive、他のソース区分との混在は全件に戻します。変更前後の両方の内容を確認します。確認済みPR mergeの差分全体が条件を満たす必要があり、最後の表示修正だけで先行するruntime変更を隠せません。

この段階で省くのは、direct-LANの自然rekey/idle lifecycle、guarded relayの実時間lease継続、relay-onlyの実時間lease/idle継続の3工程だけです。4ターゲットのrace/vet、IPC反復、Windows directory barrier、TCP/TLSのcontext制御、managed activation/restart、direct/relayの機能・復旧、合成expiry/rekey、ブラウザー、配布物、manifest検証は残します。独立したWeb 7件とproduct 2件のジョブも既存の実行条件と失敗扱いを維持します。試験の反復回数や製品タイマーは変更しません。

main pushでは、この新しい区分を全件へ戻します。既存の文書・フロント・限定Goの扱いは変えず、定期実行も追加しません。今後mainの短時間検証を広げる前には、短時間workflowの緑色だけを認めず、正確な同一ソースの全件検証を要求するリリース入口が必要です。この段階では通信・lifecycle変更は全件のままです。関連する長時間試験だけの選択や、定期全件検証は別の段階で扱います。

Goの対象リストは小さく限定しています。任意のGo変更を1環境だけで済ませる意味ではありません。OS固有ファイル、ビルド制約、確認済み境界を超える依存は全環境の対象です。限定Go実行は、統合試験のbuild tagも含む現在のimport関係から逆依存を選びます。実行するのは通常のLinux試験で、長時間native統合試験ではありません。依存関係を確定できない場合は通常Goパッケージ全件を実行します。コマンド失敗や実際のテスト成功が確認できない場合は失敗です。

開発方針や上流ソースの出所情報として使うMarkdownは、通常の説明文に含めません。必須のルート文書は存在している必要があります。ガイドが配布物に同梱されるという理由だけで、文章修正のたびにアプリを再ビルドしません。コード変更時とリリース時には配布内容を確認します。別の資源・relay計測workflowも、報告文書の文章変更ではなく計測実装の変更で起動します。

## 差分全体と必須チェック

対象のPR/main pushごとにworkflowを起動します。必須チェックが未完了のまま残り得る、workflow全体の `paths-ignore` は使いません。

PRでは確認済みbase親と試験対象のmerge commit間の変更全体を確認します。main pushではイベントの `before` から `after` まで、push内の全commitを確認します。最後の1commitだけでは判断しません。Core変更を含むPRは、最後のcommitが画面試験の修正でも全件対象です。以前の成功したジョブを再利用する仕組みではありません。

完全なGit tree一覧、renameの両端、ファイルmode、リポジトリとイベントの同一性を確認します。履歴不足、不完全・不正な差分、不明な変更は全件に戻します。集約側も差分判定を再実行するため、artifactの記載だけでは検証範囲を縮小できません。判定処理の失敗によってアプリ検証を無条件に省略しません。

必須ステータスは引き続き `ci-required` です。アプリジョブが対象外でも必ず起動し、今回のattemptで必要なジョブと各工程が実際に成功したことを確認します。文書だけの場合は **「Documentation only; application tests, builds and packages NOT RUN」** と表示します。フロントと限定Goも実行した範囲を明示します。全native・ブラウザー・配布物を実行した場合のみ `full_native=true` とします。schema 3の `native-short` 記録は各targetを `short_checks_passed`、各targetの `long_checks` を `not_run`、`full_native=false` とし、ブラウザーとmanifestは実際に成功した場合だけ成功を記録します。他の未実行範囲は `not_run` のままです。短時間検証を再利用可能な全件検証として扱いません。省略した実時間3工程も明示的なskipを要求し、失敗・欠落・未実行を成功に読み替えることは認めません。

このworkflowの採用にリポジトリ保護設定の変更は不要です。`ci-required` の必須指定を維持します。mainの各実行は別のconcurrency識別子を使うため、後の文書pushが実行中のコード検証を中止しません。同じPRの更新では、古いPR実行の中止を引き続き許可します。

全件を実行するには **Actions → Cross-platform CI → Run workflow** で **force_full** を有効のまま実行します。強制省略の指定はありません。リリースworkflowは変更範囲の判定を参照しません。今回のattemptに必要な結果が揃わない一部ジョブの再実行では、全件検証記録を発行できません。

## 文書だけの実行結果の読み方

対象PRの正確なheadに対応する最新の **Cross-platform CI** を開き、実行の **Summary** ページで **ci-required** のジョブ要約を確認します。影響判定と `ci-coverage` 記録のscopeが `docs`、必須チェックが成功、アプリ試験・ビルド・配布物が **NOT RUN（未実行）** と明記されていることが条件です。nativeジョブの緑色のskip表示だけでは必須チェックの結果になりません。記録は `full_native=false`、アプリの各範囲は `not_run` です。

この経路は変更全体が通常の文章であることを確認し、以前のアプリ試験の成功を借用しません。workflow変更そのものの全件検証は、導入時の別の確認です。PRにソースや設定の変更も含まれる場合は、最後のcommitだけで判断せず、差分全体と上の範囲表で確認します。

## キャッシュと実測

コンパイルキャッシュはビルドを高速化します。以前の試験結果を今回の成功に置き換えるものではありません。native開発用、限定Go用、trusted-main用は別の名前空間です。trusted-mainへの保存はmainでの全native検証成功時に限ります。リリースは同一ソースのキャッシュ復元と全件の再検証を維持します。限定Goも `-count=1` で試験を実行します。

計測記録には固定のsuite/job名、経過秒、成否、終了コードだけを保存します。コマンド引数、環境変数、ファイル内容、生ログは含めません。計測保存に失敗しても、コマンド失敗を成功へ変更しません。coldとwarmは分けて比較し、並列ジョブの時間を足して待ち時間と説明しません。

以前の「実時間待機だけを省く」方式では、全件mainが29分11秒、通常native・ブラウザー・配布物を残した文書PRが開発キャッシュ4件missで24分57秒、その後4件fallback hitで12分57秒でした。後者2回はいずれも実時間試験12工程を省略し、`full_native=false` を明記しました。今回とは検証範囲が異なる過去の観測であり、このworkflowの所要時間を保証するものではありません。[全件](https://github.com/webkaz-labs/sobalink/actions/runs/37456053817) · [cold](https://github.com/webkaz-labs/sobalink/actions/runs/37460427507) · [warm](https://github.com/webkaz-labs/sobalink/actions/runs/37464624901)

warmの最長Windowsジョブは11分46秒で、そのうち通常・native試験が7分31秒、フロント工程が1分41秒、キャッシュ復元・保存が1分09秒でした。そのため、文章修正ではキャッシュ追加よりもアプリ検証自体を対象外にする方針へ変更しています。

## native fixtureとリリース検証

全native検証ではrace検出、vet、Windows retirement barrier、IPC反復、合成時刻による鍵の試験、engineの負例、native discovery、direct/relay復旧、元の実時間lifecycle/lease待機を維持します。範囲判定のために製品タイマー、payload、アサーション、試験timeoutを短縮しません。

TCPとUDPに同じendpointが必要なfixtureは、正確なloopbackアドレスで両方を予約し、回数を限定した実bindと後片付けを行います。helperはテストからだけimportします。ブラウザー終了時は処理中のinterceptを完了してからfixtureを停止し、失敗は伝播します。必要なtargetや工程が失敗すれば `ci-required` も失敗します。

自動CIの結果は、実機でのenrollment、ネットワーク、OS-login、suspendの確認とは分けて扱います。
