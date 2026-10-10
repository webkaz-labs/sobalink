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
| PRまたはmainの確認済みCLI表示ファイル変更。通常文書との混在も可 | 4ターゲットの短時間・安全性試験、ブラウザー、配布物・manifest検証。実時間lifecycle/leaseの3工程とnative resource inspection・management・fixed-group catalogは未実行 |
| 通信、認証、Core、設定、タイマー、共通helper、依存・lock、ビルド設定、CI判定・workflow、その他不明なパス | Linux amd64/arm64、macOS arm64、Windows amd64の全native試験、ブラウザー、配布物・manifest検証 |
| 確認済みCLI表示・フロント・限定Goの組み合わせ | 4ターゲットの短時間・安全性試験、ブラウザー、配布物・manifest検証。実時間lifecycle/leaseの3工程とnative resource inspection・management・fixed-group catalogは未実行 |
| OS固有Go、cgo、未確認のimport、方針・出所情報のMarkdown、不正なmode/type、判定できない入力 | 全件 |
| 毎日の定期全件確認、手動の全件確認、すべてのプレリリース | 全件 |

### 確認済み変更のnative-short

`native-short` の対象は `cmd/soba/help.go`、`cmd/soba/errors.go`、`cmd/soba/errors_test.go`、`cmd/soba/human_output.go`、`cmd/soba/human_output_test.go` の5ファイルだけです。新しいパス・import・build directive、未確認の区分との混在は全件に戻します。既存のフロント・限定Goとの組み合わせは4ターゲットのnative-shortで確認します。文書・フロント・限定Goだけの場合は従来の限定範囲を維持します。変更前後の両方を確認し、混在時も変更対象の限定Goパッケージ全体を調べます。確認済みPR mergeまたはmain pushの差分全体が条件を満たす必要があり、最後の表示修正だけで先行するruntime変更を隠せません。生成assetには引き続き対応するフロントのソース変更が必要です。

この区分では、direct-LANの自然rekey/idle lifecycle、guarded relayの実時間lease継続、relay-onlyの実時間lease/idle継続に加え、別枠の全件専用native resource inspection・management・fixed-group catalogを省きます。4ターゲットのrace/vet、IPC反復、Windows directory barrier、TCP/TLSのcontext制御、managed activation/restart、direct/relayの機能・復旧、合成expiry/rekey、ブラウザー、配布物、manifest検証は残します。独立したWeb 7件とproduct 2件のジョブも既存のPR・手動実行条件と失敗扱いを維持し、定期全件実行では両方を要求します。試験の反復回数や製品タイマーは変更しません。

正当性を確認したmain pushもPRと同じ限定native-short判定を使えます。新規作成・削除・force pushされたmainや未対応イベントは全件に戻します。このmain短縮を導入する前から、下記の同一ソース全件検証を要求するリリース入口を維持します。短時間workflowの緑色だけではリリース条件を満たしません。通信・lifecycle変更は全件のままです。対象ファイルの追加、関連する長時間試験だけの選択、試験回数の削減は行いません。毎日の全件実行は下記のとおりです。

Goの対象リストは小さく限定しています。任意のGo変更を1環境だけで済ませる意味ではありません。OS固有ファイル、ビルド制約、確認済み境界を超える依存は全環境の対象です。限定Go実行は、統合試験のbuild tagも含む現在のimport関係から逆依存を選びます。実行するのは通常のLinux試験で、長時間native統合試験ではありません。依存関係を確定できない場合は通常Goパッケージ全件を実行します。コマンド失敗や実際のテスト成功が確認できない場合は失敗です。

開発方針や上流ソースの出所情報として使うMarkdownは、通常の説明文に含めません。必須のルート文書は存在している必要があります。ガイドが配布物に同梱されるという理由だけで、文章修正のたびにアプリを再ビルドしません。コード変更時とリリース時には配布内容を確認します。別の資源・relay計測workflowも、報告文書の文章変更ではなく計測実装の変更で起動します。

## 差分全体と必須チェック

対象のPR/main pushごとにworkflowを起動します。必須チェックが未完了のまま残り得る、workflow全体の `paths-ignore` は使いません。

PRでは確認済みbase親と試験対象のmerge commit間の変更全体を確認します。main pushではイベントの `before` から `after` まで、push内の全commitを確認します。最後の1commitだけでは判断しません。Core変更を含むPRは、最後のcommitが画面試験の修正でも全件対象です。以前の成功したジョブを再利用する仕組みではありません。

PRの試験対象はActionsの `GITHUB_SHA` と、正確な `refs/pull/<number>/merge` のcheckoutです。実commitの親は2つに限り、順序もイベントのbase、headと一致する必要があります。PRの背景mergeability計算による `merge_commit_sha` の参考値は、形式が正しくても古い場合があり、この証明の代わりにはしません。不正な参考値、誤ったref、実際の親の不一致は引き続き全件へ戻します。変化する最新PRのAPI応答ではなく、固定されたイベントとGitオブジェクトで判定します。GitHubの[Actions merge branchの識別](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)と[mergeability項目の説明](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request)を参照してください。

完全なGit tree一覧、renameの両端、ファイルmode、リポジトリとイベントの同一性を確認します。履歴不足、不完全・不正な差分、不明な変更は全件に戻します。集約側も差分判定を再実行するため、artifactの記載だけでは検証範囲を縮小できません。判定処理の失敗によってアプリ検証を無条件に省略しません。

必須ステータスは引き続き `ci-required` です。アプリジョブが対象外でも必ず起動し、今回のattemptで必要なジョブと各工程が実際に成功したことを確認します。文書だけの場合は **「Documentation only; application tests, builds and packages NOT RUN」** と表示します。フロントと限定Goも実行した範囲を明示します。全native・ブラウザー・配布物を実行した場合のみ `full_native=true` とします。schema 3の `native-short` 記録は各targetを `short_checks_passed`、各targetの `long_checks` を `not_run`、`full_native=false` とし、ブラウザーとmanifestは実際に成功した場合だけ成功を記録します。他の未実行範囲は `not_run` のままです。短時間検証を再利用可能な全件検証として扱いません。省略した実時間3工程と別枠のnative resource 3工程（inspection・management・fixed-group catalog）には、それぞれcompleted/skippedの記録が正確に1件必要です。失敗・欠落・実行済みの結果は認めません。既存の `long_checks` は引き続き実時間3工程だけを示し、native resource工程はリテラルの `FULL_ONLY_STEPS` 方針と名前付き工程の時間記録で別に追跡します。

このworkflowの採用にリポジトリ保護設定の変更は不要です。`ci-required` の必須指定を維持します。mainの各実行は別のconcurrency識別子を使うため、後の文書pushが実行中のコード検証を中止しません。同じPRの更新では、古いPR実行の中止を引き続き許可します。

全件を実行するには **Actions → Cross-platform CI → Run workflow** で **force_full** を有効のまま実行します。強制省略の指定はありません。リリースworkflowは変更範囲の判定を参照しません。今回のattemptに必要な結果が揃わない一部ジョブの再実行では、全件検証記録を発行できません。

## リリース前の正確な同一ソース全件検証

確認済みcommitがmainに入った後、その正確なcommitで **Cross-platform CI → Run workflow → force_full=true** を実行します。プレリリース入口はmainの明示的な `workflow_dispatch` 全件検証を要求します。CIの手動dispatchは保守的にすべて全件実行の意思として扱い、`force_full=false` で必要な試験を省いた実行は条件を満たしません。通常のmain pushは、成功していてもこの明示的なリリース証拠にはなりません。

入口はrun IDや過去の成功だけでなく、attempt開始時刻で最新の手動試行を判定します。新しい全件試行の失敗・取消を古い成功で無視しません。進行中の手動検証は完了させるか、取り消した後に新しい全件検証を成功させます。新しい通常CIの成功は、同一ソースの全件証拠を無効化しません。新しい通常CIの失敗・取消・進行中は、解決するか、それより後の全件検証が成功するまで公開を止めます。時系列が不明、API一覧が不完全な場合も止めます。

失敗したジョブだけを再実行すると、GitHubは成功済みの処理を現在attemptの有効なジョブ記録として引き継ぐことがあります。この現在attemptの記録は、元の時刻を保持していても受け入れます。異なるrunの成功を組み合わせたり、欠落したジョブを古いattemptから手作業で埋めたりはしません。過去の失敗はGitHubの履歴に残ります。

証拠は正確なcommit/tree、workflow・判定ソースのhash、現在の有効attempt、4つのnative runner targetと必須工程すべてを結び付けます。実時間試験、各targetのnative resource inspection・management・fixed-group catalog、ブラウザー、manifest、ci-required、Web 7件、product 2件も対象です。Goイベント検証helperの正確なソースもhashに結び付けます。全件専用のリテラル3工程tupleと各工程の確認済み直列workflow呼び出しを静的に検証し、native resource工程の欠落・重複・skip・成功以外の結果では条件を満たせません。入口と公開直前の両方で、読み取り専用GitHub APIを独立に確認します。保存JSONだけで成功を許可しません。同じソースの新しい全件成功は公開時に使えますが、新しい失敗は無視しません。証拠artifactの保持は90日です。監査記録であり、追加の署名済みリリース資産ではありません。配布物の署名・実インストール検証は独立したまま変更しません。

文書・フロント・限定Go・native-shortの成功は、明示的な全件検証が揃うまでは全件リリース条件を満たしません。定期実行は検証を追加しますが、この手動リリース証拠の代わりにはしません。

## 毎日の全件検証

確認済みworkflowは `.github/workflows/ci.yml` の `0 18 * * *` で、**毎日18:00 UTC、翌日の03:00 JST（UTC+09:00）**に1回の全件実行を予約します。このworkflowがdefault branch（`main`）へmergeされて初めて有効になります。変更がなくても、GitHubはdefault branchの最新commitを使います。判定は明示的に `full` とし、空の差分や文書だけの差分から `native-short` にはしません。

定期実行は4つのnative target、全実時間工程、別枠のnative resource inspection・management・fixed-group catalog、ブラウザー、配布物・manifest、Web 7件、product 2件を要求します。`nightly-full-check` は `ci-required` と両acceptanceジョブを待ち、必須工程の実行結果も確認します。未実行・欠落・中止・失敗を成功の証拠にはしません。既存のPR・手動acceptanceの入口条件と通常の `ci-required` の依存関係は維持します。短時間PRの結果は、この定期専用aggregateを待ちません。実行ごとに別のconcurrency識別子を使うため、定期実行がpushや明示的な手動リリース監査を中止することはありません。キャッシュの信頼範囲と読み取り専用のrepository権限も変えません。

長時間native・acceptanceジョブを含む全件CIが毎日1回増えます。頻度を減らす場合は安定性・runner時間・費用の実績を確認し、cronの1行をレビューして変更します。自動で頻度を減らしません。定期実行の成功だけでは、上記の手動リリース証拠を満たしません。新しい定期実行が失敗・保留中なら、解消するか同じソースの後続手動全件検証が成功するまでリリースを止めます。

GitHubの混雑時、特に毎時00分には遅延や実行の欠落があり得ます。03:00 JSTは開始希望時刻であり、正確な開始時刻の保証ではありません。公開repositoryでは60日間の活動がないと定期workflowが無効になる場合があります。[GitHubの定期実行条件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule)を参照してください。外部schedulerや新しい認証情報は使いません。

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

## 全件専用のnative remote resource inspection

`Verify native remote resource inspection` は、通常のdirect有効transportの復旧試験後、
実時間3工程の並列区間より前に置く1つの直列ステップです。同じ全件条件を使いますが、
`LONG_STEPS` には追加しません。判定対象リスト、3工程のbackground区間、必須wait、
Node 24の固定、`setup-go` の `cache: false` 方針は変更しません。

正確なGo `go1.27.1` とmatrixのGOOS/GOARCHを確認した後、`ci-go-test.py --exact` を
1回だけ呼び出します。`internal/core` の正確な5件で、最初のremote inspection、元の
有効期限を維持する両方向のCore再起動、新しいinspectionのrevoke拒否、既存ownerを
維持するポート競合を確認します。5件だけに一致するselector、race検出、`-count=1`、
`-timeout=8m`、確認済みの5つのbuild tagをソース方針で固定します。helperは各caseの
run/passとpackageのstart/passをそれぞれ1回要求し、追加・skip・重複・欠落は失敗です。
warmupや再試行は追加しません。子プロセスは10分、外側のステップは12分を上限とします。

proxy指定7変数（`HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`http_proxy`、
`https_proxy`、`all_proxy`、`TS_PROXY`）は空または未設定が必要です。値があれば
ツールチェーン確認前に失敗し、黙って削除したり値を表示したりしません。2つの明示的な
fixture opt-inは子プロセスの環境だけに設定します。fixtureが所有する数値
`127.0.0.1` のpeerと合成一時profileだけを使い、実ユーザーのgrantを有効化せず、
OSのsecurity・route・firewall方針は変更しません。

ソースレビューとオフライン方針試験が示すのは接続関係だけです。ローカルLinuxのfixture
結果はLinux ARM64・macOS・Windowsの受け入れや、正確な同一ソースの4対象CIを示しません。
自動化したCoreのclose/openは、OSプロセス再起動、実機、enrollment、sign-in、suspend、
アプリケーションの受け入れ確認とは別です。正確なソースと対象で実際に得た結果だけを
そのnative確認の根拠とし、配布物・実インストールの検証は別に維持します。

## 全件専用のnative managementとfixed-group catalog

`Verify native remote resource management` はinspectionの直後に置き、正確な3件、
確認済みの6つのbuild tag、race検出、既定のvet、8分の試験上限、子プロセスだけの
management/activation opt-inを維持します。`Verify native fixed-group resource catalog`
は、その直後かつ既存の3工程並列区間の前に、FULL専用の直列ステップを1つだけ追加します。
`FULL_ONLY_STEPS` はinspection・management・fixed-group catalogの順で正確に3工程です。
`LONG_STEPS` やnative-shortの実行対象には追加しません。

新工程は正確なGo `go1.27.1` とmatrixのGOOS/GOARCHを確認し、`ci-go-test.py --exact`
で `internal/core:TestResourceGroupCatalogNativeFixedTwoTargetOneShot` を1回だけ呼びます。
1件だけに一致するselector、`-race`、既定のvet、`-count=1`、`-timeout=4m` を固定します。
7つのbuild tagは `ts_omit_portmapper`、`ts_omit_captiveportal`、`ts_omit_useproxy`、
`directlan_activation_native`、`resource_inspection_native`、`resource_management_native`、
`resource_group_catalog_native` です。子プロセスは6分、外側の工程は8分が上限です。
再試行やwarmupは追加しません。exact-event helperはtestのrun/passとpackageの
start/passを正確に1回要求し、追加test/package・skip・重複・欠落を拒否します。

子プロセスの環境だけで上記proxy指定7変数と、継承したinspection/management opt-inを
削除します。`SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE=reviewed-three-core-loopback-v1`
と `SOBALINK_RUN_ACTIVATION_NATIVE=1` を設定し、環境値の表示や親環境の変更は行いません。
fixtureが所有する同一プロセス内の3つのCore、正確な数値loopback endpoint、合成一時
profileを使います。固定2対象のpreview/apply、ローカルcatalog、同一要求の再実行と
statusの証拠、不一致の確認要求の拒否を、明示的に同意した合成grantの範囲で観測します。

得られているローカルN1結果は、race検出なしのLinux `CGO_ENABLED=0` 実行1回です。
このソース接続とmocked Python検証だけでは、新しいhosted race実行、4環境、browser、
別プロセス、group再起動・復旧、cancel/fault、実機の受け入れ完了を示しません。
同意前のcontroller close/openは準備であり、group復旧の証拠ではありません。
実際のhosted結果の正確なソースと対象を確認してから、その範囲の成功を扱います。
手動リリース証拠は全native対象でこの名前付き工程の一意な成功を要求し、workflow・
判定ソース・exact-event helperのソースも結び付けます。配布物・署名・実インストールの
ゲートと既存acceptance suiteは維持します。

## 独立した実時間ステップの並列実行

既存の自然なライフサイクル、guarded lease、relay-only lease の3組を GitHub
Actions の `background` で並列実行し、パッケージ作成の直前に名前付きの必須
`wait` を置きます。各ステップ名、Go の厳密な成功イベント、タイムアウト、
race オプション、native-short のスキップ条件は変えず、ネイティブ4対象を維持します。
完全CI・夜間CIの証跡には wait と各検証の成功が必要です。失敗・キャンセル・
スキップ・欠落を完全実行の成功として扱いません。

重ねるのは待ち時間の長い3組だけです。同じタグ付きパッケージをビルドする
直列のネイティブ検証後に開始しますが、キャッシュが削除されれば追加ビルドは
必要です。パッケージ作成、アップロード、キャッシュ保存、時間集計は wait 後です。
この区間で既存の時間計測 JSONL に書くのは natural-lifecycle だけなので、同時書き込みは
ありません。計測を追加するときは共有追記ではなく、保存先の分離と wait 後の検証付き
統合が必要です。

各組は別プロセスの合成フィクスチャです。アプリのポートは独立したユーザー空間
ネットワークスタックに属し、ホストのソケットは一時ポートを要求します。状態は
メモリ内またはテスト専用です。guarded エンジンの通常の UDP ソケット動作と
ループバック宛先限定ポリシーは維持します。実機・物理ネットワークの受け入れ完了を
示すものではありません。ホスト資源の競合と既存の一時ポート予約・再bind間の競合は、
4対象のCIで確認が必要です。

ソースレビューやオフラインテストから高速化を実測済みとはしません。同じ対象・
キャッシュ条件の完全実行で、ジョブ全体の経過時間と各ステップ時間を比較します。
native-short でスキップされたステップの wait も別途確認します。
[GitHub の background / wait 構文](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsbackground)を参照してください。
