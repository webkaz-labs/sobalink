# CI回帰検査の実行頻度

[English](CI_TESTING.en.md)

共通Linux jobはPython回帰検査を全件検出し、native製品検査と並列に実行します。
native側には実ファイル、process、権限、終了処理の検査を残します。
Go race、native discovery、ブラウザー受入、パッケージ検証は変更しません。
別の[native UI限定scope](CI_EFFICIENCY.ja.md#ネイティブuiのみの変更)は4対象のcompanion全race suiteと
Windows/macOS製品パッケージを維持し、関連しないバックエンド全件検証とブラウザーjobを省略します。

- 通常の製品変更では、内部のfixture検査を除き、Windowsは53件、Linuxは57件、
  macOSは69件のnative検査を選びます。実Nearby子processの出力量制限、
  timeout時の終了処理、stdin pipeを確認する3件を含みます。
- CI・toolchain・依存変更、nightly、強制full、不明なパス、不正・不完全な変更証拠では、
  引き続きnative検査を広く検出します。hosted Cross-platform CIのこの経路だけで、
  個別に印を付けた `FullProofTests` のソース・receipt方針検査64件を除外します。
  64件は必須の共通jobで実行します。このworkflow以外の全件検出では除外しません。
- 共通Ubuntu x64が同targetの全件検出を担い、native laneはOS subsetを維持します。
  文書のみ、frontendのみ、限定Go変更では、Python jobを追加せず従来の限定検査を維持します。

両方の実行側が現在のeventの完全なGit差分を独立して再検証します。
同一実行の集約判定と同一ソースのリリース証明は、nativeが全て成功しても、
共通jobとその工程の成功を必須にします。cacheや過去の実行では代用できません。
native jobは共通jobを待たず、最後の集約判定で両方を確認します。

`test_ci_full_validation.py` の `common_only` はmethod単位の印で、classやmodule全体を
除外しません。新しい未指定method・class、継承したmethod、未指定でoverrideした検査は、
広いnative検出に残ります。これは限定的な中間改善であり、完全な分類ではありません。
他のmatrix重複と通常時のselectorは残ります。新しいOS依存検査は、通常native subsetへの
追加も引き続き確認する必要があります。

OS selectorは `.github/scripts/ci-regressions.py` にあります。macOSの実POSIX子process
所有権とTerminal終了処理の検査は維持し、Linuxで代用しません。hosted runnerの更新が
未指定helper fixtureに影響する可能性があるため、それらはnightly matrixに残します。
指定したFullProofはソースと模擬receiptの検査で、native process動作の検査ではありません。
hosted環境での時間短縮はまだ主張しません。梱包、署名、同一ソースのリリース検証は独立に維持します。

## フロントエンド試験の反復

フロントエンド検査を含む全scopeで、必須のbrowserジョブが移植可能なVitest一式を
1回実行します。CI・ツールチェーン・依存の変更、夜間・明示的な全件検証、不明または
検証できない変更も同じです。文書のみ・scoped-Goの変更は従来の限定検査を維持します。

native CIのintegration laneとprereleaseは
`python .github/scripts/check-frontend.py --build-only` を使います。
4つのnative対象すべてで引き続き、ロックファイルに従うクリーンな依存インストールと
ビルドを2回行い、双方の出力とチェックイン済み配布用ファイルの一致、ロックファイルが
変更されていないことを確認します。CIのunit laneは、この重複ビルド工程だけを省略します。
集約判定では同じソース・run・有効なattemptの全integration laneで工程の成功を要求します。
広範なPython回帰試験が実際のNode helperを使うため、unitでも固定版Nodeのsetupを維持します。
prereleaseは変えません。browser・ローカルでの引数なし呼び出しは、引き続き
フロントエンド試験を全件実行します。Vitestを実行するかどうかの判断に、Python回帰検査の
選択処理やimpact planは使いません。

現在のVitestはjsdom、純粋なロジック、モック境界を検査します。Nodeのファイル読み取りは
静的なソース・CSS・WASMやfixtureのバイト列の確認であり、OSごとの反復が必要な
実process・権限・socket・ファイルシステム動作の検査ではありません。
定期的な全OS Vitestジョブは追加しません。今後、実際のOS契約を扱う試験が加わる場合は、
移植可能な全試験を反復するのではなく、その契約と必要な対象を確認します。

集約判定と完全検証の証拠には、同一ソースのbrowserジョブとそのフロントエンド工程の
成功が必要です。prereleaseでは、正確な `tested_commit` に対する手動の強制full CIと、
その実行内のbrowser試験の成功を要求します。browserジョブ・工程の欠落・省略・失敗を、
nativeのビルド成功や別コミットのcache済み試験結果で代用しません。実ブラウザー受入と
独立したnative・リリース検査は維持し、テストケース、アサーション、製品タイマーは
削除しません。

## 通常Go検査とactivation

CIとprereleaseでは、Linuxの完全なrace suite、またはWindows/macOSの互いに重ならない
whole-Coreと残りのpackageの組で、通常のGo試験を1回実行します。同じケースが既に含まれる
`backendworker`、`connectionroute`、Coreの `TestMixed` 部分集合のcount-one再実行は除きます。
タグ付き統合検査、IPC・DirectLANの反復、vet、梱包検査は維持します。

product 2件とWeb 7件のactivationジョブは、同一repositoryのPRで文書のみと証明できた場合に
省略します。impact判定の失敗・不明時は実行します。限定Go変更では、service presetsがCoreの
snapshotに、bounded loggingがCLIの起動終了処理に使われるため、両方を残します。
強制fullと定期検証は、このPR向け最適化とは独立して両方のジョブを要求します。

## ブラウザー準備工程の重ね合わせ

Go engineの準備をbackgroundで進める間に、Node・フロントエンドの検査を行います。
この準備処理の依存先は標準ライブラリとadaptation補助処理だけで、Web資産はcompileしません。
フロントエンド再現が完了してから明示的なwaitで合流します。その後、Chromium・fontの導入と
Go fixture診断を重ね、実ブラウザー受入の前にもう一度waitします。背景2工程と両waitの
成功を実行証拠として要求します。

Go診断をフロントエンド完了前へ移してはいけません。`soba-e2e` はViteが削除・再生成する
`web/dist` をembedします。診断ケース同士の直列順序と専有listenerも維持します。
追加のbrowserやrunnerを同時起動せず、試験ケースやtimeoutを削りません。Go準備とnpmが
CPU・downloadを競合する可能性は残るため、効果はhosted実行で測ります。背景工程の
environment・outputsを使う場合も、対応するwaitの後に限ります。
