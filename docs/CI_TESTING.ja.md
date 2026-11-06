# CI回帰検査の実行頻度

[English](CI_TESTING.en.md)

CIの共通fixture検査はLinuxの独立jobで1回実行し、native製品検査と並列に進めます。
各native laneには実際のファイル、子process、権限、終了処理を確認するPython検査を残します。
通常CIで選択されたscope内のGo製品race検査、native discovery、ブラウザー受入検査、パッケージ検証は削減しません。
別の[native UI限定scope](CI_EFFICIENCY.ja.md#ネイティブuiのみの変更)は4対象のcompanion全race suiteと
Windows/macOS製品パッケージを維持し、関連しないバックエンド全件検証とブラウザーjobを省略します。

- 通常の製品ソース変更では、共通jobがPython全suiteを検出・実行します。
  nativeは確認済みのOS契約を選びます。内部で再実行するfixtureを除き、
  Windowsは49件、Linuxは53件、macOSは65件です。
- CIスクリプト/workflow、依存manifest、engine adaptation、engine準備処理の変更では、
  各targetでPython全suiteを実行します。
- 既存のnightly、強制full、不明なパス、不正または不完全な変更証拠でも、
  各targetで全suiteへ戻ります。
- 共通Ubuntu x64 jobが同targetの全suiteを担います。そのnative laneはOS契約subsetを
  維持し、定期検証で5回目の全実行を増やさないようにします。
- 文書のみ、frontendのみ、限定Go scopeの変更には、新しいPython jobを追加しません。
  既存のscope別検査を維持します。

両方の実行側が、現在のeventに対応する完全なGit差分を独立して再検証します。
通常CIでは過去のテスト結果を省略の根拠にはしません。nativeが全て成功しても、共通jobが失敗すれば
集約判定とリリース証明は成功しません。cache復元の成功をテスト合格として扱いません。

選択するOS契約は `.github/scripts/ci-regressions.py` にあります。
macOSでは実POSIX子processの所有権とTerminal終了処理を維持し、Linuxの結果で代替しません。
Windows/Darwin metadataを模擬する共通fixtureだけなら、そのOSでの実行は必須ではありません。

残るリスクとして、hosted runnerの更新が、通常の製品変更では繰り返さないOS上の
共通Python fixtureに影響する可能性があります。nightlyの全matrixで検出します。
管理対象のtoolchainやhelper方針の変更では、直ちに全検査へ戻します。
梱包、署名、同一ソースに結び付いたリリース検証は、それぞれの独立検査を維持します。

今後さらに削減する場合は、実ソースの契約検査とhelperだけのfixtureを分け、
新規・未分類の検査は全実行へ戻るようにします。実装言語だけを変えても、
子process、Git履歴、繰り返すbuild、意図した実時間protocol試験の時間はなくなりません。

## フロントエンド試験の反復

フロントエンド検査を含む全scopeで、必須のbrowserジョブが移植可能なVitest一式を
1回実行します。CI・ツールチェーン・依存の変更、夜間・明示的な全件検証、不明または
検証できない変更も同じです。文書のみ・scoped-Goの変更は従来の限定検査を維持します。

native CIとprereleaseは `python .github/scripts/check-frontend.py --build-only` を使います。
各native対象では引き続き、ロックファイルに従うクリーンな依存インストールとビルドを
2回行い、双方の出力とチェックイン済み配布用ファイルの一致、ロックファイルが
変更されていないことを確認します。browser・ローカルでの引数なし呼び出しは、引き続き
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

通常のCIでは、Linuxの完全なrace suite、またはWindows/macOSの互いに重ならない
whole-Coreと残りのpackageの組で、通常のGo試験を1回実行します。同じケースが既に含まれる
`backendworker`、`connectionroute`、Coreの `TestMixed` 部分集合のcount-one再実行は除きます。
タグ付き統合検査、IPC・DirectLANの反復、vet、梱包検査は維持します。

product 2件とWeb 7件のactivationジョブは、同一repositoryのPRで文書のみと証明できた場合に
省略します。impact判定の失敗・不明時は実行します。限定Go変更では、service presetsがCoreの
snapshotに、bounded loggingがCLIの起動終了処理に使われるため、両方を残します。
強制fullと定期検証は、このPR向け最適化とは独立して両方のジョブを要求します。


## リリースのソース証拠と配布物検証

prereleaseは、現在のmainと正確に同じコミットに対する、既存の手動full CI証拠を
監査して使います。通常のrace・vet、タグ付きruntime検査、companionのソース試験は
繰り返しません。4対象と必須CI工程すべての成功は引き続き必要で、ソース方針を変更した
場合は新しい同一ソースfull実行が必要です。入口・staging・公開前の再確認で、欠落・
古いソース・失敗の証拠を拒否します。cache、限定scope、異なるrunの成功の組み合わせで
代用しません。

リリースでは引き続き、locked frontendの再現、正確なソースとtoolchainの確認、
リリース版パッケージの2回build、実際のarchiveと導入済みbinaryの検査を行います。
署名・provenance・公開ダウンロードは独立した検査のままです。native IPC終了処理の
25回反復は、CIの5回とは別の追加stress工程として維持します。これはリリース版の
情報を書き込んだbinaryの検査ではありません。短いリリースguard自己試験も残します。

実行間にhosted runner image、Python、system libraryが変わる可能性があります。
ソース証拠の再利用は実行環境の完全一致や実機受入の完了を意味しません。
今回のnative配布物検証と既存の定期full CIは引き続き必要です。この変更では
実行頻度を変えず、実測していない待ち時間の短縮量を断定しません。
