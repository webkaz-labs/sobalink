# Endpoint metadata contracts

This package is an isolated foundation for planned authenticated endpoint
updates. The application does not call it. It does not change pairing,
permissions, saved profiles, listeners or transport behavior.

It provides strict, bounded wire encodings, context-bound signatures and fixed
synthetic vectors, plus candidate records and persistence-result
models. A saved eligibility result describes the supplied model; it is not
current application authority or evidence that a connection is ready.

Callers must still implement and verify current authorization, exclusive
lifecycle ownership, real profile migration, truthful durable storage outcomes,
transport retirement and activation. No runtime or crash-recovery guarantee is
established by this package's synthetic storage tests. Restoring an entire old
profile is also outside its rollback-detection guarantees.

Focused checks: `go test -race ./internal/endpointmeta` and
`go vet ./internal/endpointmeta`. Test keys and addresses are public test vectors
or synthetic data; they are not credentials for a running service.

## 日本語

このpackageは、今後の認証済み接続先更新に向けた独立した基盤です。
アプリからの呼出しはなく、ペアリング、許可、保存プロファイル、待受け、通信動作を
変更しません。

厳密で容量制限のある交換形式、組合せに結び付く署名と固定テストデータ、変更候補の
記録と保存結果のモデルを提供します。保存済みの利用条件を満たすという判定は、
渡されたモデルについての結果です。現在のアプリの許可や接続準備完了を示しません。

現在の許可確認、ライフサイクルの排他的な管理、実プロファイルの移行、永続保存結果の
正確な扱い、古い通信処理の終了と新しい通信の有効化は、呼出し側で別途実装・検証する
必要があります。合成データの保存試験は実通信や異常終了後の復旧保証ではありません。
古いプロファイル全体の復元を検出できる保証もありません。

対象の確認は `go test -race ./internal/endpointmeta` と
`go vet ./internal/endpointmeta` です。テスト用の鍵とアドレスは公開テストベクトルまたは
架空のデータで、稼働中のサービスの認証情報ではありません。
