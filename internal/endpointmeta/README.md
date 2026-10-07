# Endpoint metadata contracts

This package is an isolated foundation for planned authenticated endpoint
updates. The application does not call it. It does not change pairing,
permissions, saved profiles, listeners or transport behavior.

It provides strict, bounded wire encodings, context-bound signatures and fixed
synthetic vectors, plus candidate records, persistence-result models and an
unused private-file adapter for isolated snapshots. A saved eligibility result
describes the supplied model; it is not current application authority or evidence
that a connection is ready.

`ReadSnapshotFile` and `SaveSnapshotFile` require an explicit positive byte
budget and use the existing canonical snapshot encoding. Reads return evidence
without initializing missing files, reconciling pending changes or confirming
durability. Saves use `config.AtomicWritePrivate` once and preserve its error:
`ErrAtomicCommitted` reports published bytes with uncertain durability; other
errors report no publication. Use `ResolveSave` to retain the corresponding
model recovery outcome. A later successful read does not clear that recovery
requirement. No identity secrets or current application grants belong in these
files.

Atomic publication is not a logical compare-and-swap. The caller must exclusively
own model and file lifecycle access, perform current authorization checks and
reconcile uncertain outcomes. The reader rejects symlink and nonregular targets
and bounds reads independently of file size checks; it does not establish safety
against malicious concurrent pathname substitution. This adapter introduces no
application caller, migration or new live profile schema.

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
記録と保存結果のモデル、および独立したスナップショット用の未使用の非公開ファイル
アダプターを提供します。保存済みの利用条件を満たすという判定は、
渡されたモデルについての結果です。現在のアプリの許可や接続準備完了を示しません。

`ReadSnapshotFile` と `SaveSnapshotFile` は明示的な正のバイト上限を必要とし、
既存の正規化されたスナップショット形式を使います。読み込みは記録を返すだけで、
存在しないファイルの初期化、保留中の変更の整合確認、永続性の確認は行いません。
保存は `config.AtomicWritePrivate` を1回呼び、そのエラーを保持します。
`ErrAtomicCommitted` は書き換え済みで永続性が未確認、それ以外のエラーは
書き換え未完了を示します。`ResolveSave` で対応するモデルの復旧状態を保持して
ください。後の読み込みが成功しても、復旧の必要性は解除されません。
識別用の秘密鍵や現在のアプリの許可情報は、これらのファイルに含めません。

ファイルのアトミックな書き換えは、モデルの論理的な比較更新ではありません。
呼出し側はモデルとファイルのライフサイクルを排他的に管理し、現在の許可確認と
結果が不確かな場合の整合確認を行う必要があります。読み込みはシンボリックリンクと
通常ファイル以外を拒否し、ファイルサイズ確認とは別に読み込み量を制限します。
悪意ある同時のパス差し替えに対する安全性は保証しません。このアダプターは、
アプリからの呼出し、移行、新しい実プロファイル形式を導入しません。

現在の許可確認、ライフサイクルの排他的な管理、実プロファイルの移行、永続保存結果の
正確な扱い、古い通信処理の終了と新しい通信の有効化は、呼出し側で別途実装・検証する
必要があります。合成データの保存試験は実通信や異常終了後の復旧保証ではありません。
古いプロファイル全体の復元を検出できる保証もありません。

対象の確認は `go test -race ./internal/endpointmeta` と
`go vet ./internal/endpointmeta` です。テスト用の鍵とアドレスは公開テストベクトルまたは
架空のデータで、稼働中のサービスの認証情報ではありません。
