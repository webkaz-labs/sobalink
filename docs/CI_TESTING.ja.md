# CI回帰検査の実行頻度

[English](CI_TESTING.en.md)

CIの共通fixture検査はLinuxの独立jobで1回実行し、native製品検査と並列に進めます。
各native laneには実際のファイル、子process、権限、終了処理を確認するPython検査を残します。
Go製品のrace検査、native discovery、ブラウザー受入検査、パッケージ検証は削減しません。

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
