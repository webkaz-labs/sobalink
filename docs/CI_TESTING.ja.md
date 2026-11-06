# CI回帰検査の実行頻度

[English](CI_TESTING.en.md)

CIの共通fixture検査はLinuxの独立jobで1回実行し、native製品検査と並列に進めます。
各native laneには実際のファイル、子process、権限、終了処理を確認するPython検査を残します。
選択されたscope内のGo製品race検査、native discovery、ブラウザー受入検査、パッケージ検証は削減しません。
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
過去のテスト結果を省略の根拠にはしません。nativeが全て成功しても、共通jobが失敗すれば
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

`native-short` または `full` が選ばれる通常の製品ソース変更では、必須のbrowserジョブが
移植可能なVitest一式を1回実行します。native側では重複する `npm run test` の呼び出しだけを
省略します。scoped-Goの変更は従来のGo専用検査を維持します。各native対象では引き続き、
ロックファイルに従うクリーンな依存インストールとビルドを2回行い、双方の出力と
チェックイン済み配布用ファイルの一致、ロックファイルが変更されていないことを確認します。
実ブラウザーの受け入れ検査も必須です。

CI・ツールチェーン・依存の変更、夜間・明示的な全件検証、不明または検証できない変更では、
全native対象でもVitestを実行します。native用planを渡さないbrowser・リリース・ローカルの
呼び出しは常に全件実行です。完全検証の証拠には、同一ソースのbrowserジョブとその
フロントエンド工程の成功が必要です。欠落・省略・失敗をnativeのビルド成功で代用しません。

残余リスクとして、移植可能な単体試験のOS固有の問題は、通常の製品PRではなく夜間の
全対象検証で見つかる場合があります。各OSのロック済みビルドと独立したnative・リリース
検査は維持し、テストケース、アサーション、製品タイマーは削除しません。
