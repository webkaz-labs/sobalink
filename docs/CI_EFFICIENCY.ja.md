# 変更内容に応じたネイティブCI

[English](CI_EFFICIENCY.en.md) · [確認範囲](VERIFICATION.md) · [開発方針](DEVELOPMENT_PRINCIPLES.ja.md)

短い安全性・ロジック確認、4対象すべてのネイティブ試験、実ブラウザー、再現可能なfrontend、package／manifestの確認は毎回維持します。省略できるのは、保守的な変更影響判定で不要と証明できた自然な鍵更新・中継lease・中継登録のidle境界の実時間待機だけです。プレリリースでは実時間試験と従来の配布・署名・実導入の確認をすべて実行します。

## 毎回実行する確認

- ネイティブrace、vet、整形、Windowsの受信廃棄バリア、反復IPC cleanup
- syntheticな鍵期限切れ・flush・明示rekey、direct LANとCoreの統合確認
- guarded／relay-only両方のペア設定、相手識別、TLS／pin、TCP／UDP、取消、終了処理
- engineの許可・否定試験、native STUN、従来の経路復旧試験
- frontendテストと同一lockでの2回一致build、実ブラウザー、archiveとmanifestの確認

新しい短いrelay試験は、長い試験と同じfixtureと安全性の確認を使います。自然なlease境界を越えた証拠とは扱いません。長い試験では、元の130秒の継続確認、同じTCP接続、境界後の最後のframe、再許可の確認を維持します。複数中継の登録試験も元の80秒のidle境界を残し、別の短い試験で同じ機能上の復旧を確認します。短い試験でidle境界を確認したとは扱いません。direct LANの自然rekey・期限切れも元のtimerを維持し、製品のtimerは短縮しません。

## 実時間試験を実行する条件

初期版では、小さい許可リストだけを省略対象にします。

| 基準からの累積変更全体 | 実時間試験 |
| --- | --- |
| rootのREADME／SECURITYまたは通常の `docs/**/*.md` だけ | 検証済みfull基準がある場合に限り省略可能 |
| `web/src/**/*.css` と、それに伴う `web/dist` の生成物 | 検証済みfull基準がある場合に限り省略可能。frontendの再生成一致は必須 |
| 検証済みfull基準と同じtree | 省略可能 |
| Go、`internal/**`、`cmd/**`、Core／config／認証／policy／timer、helper、fixture、workflow、toolchain、依存、lock、不明なpath | full |
| UIのTS／TSX、API client、event handler、経路・設定・loginの操作 | full。見た目だけの変更とは扱わない |
| 安全なCSS変更を伴わない生成物 | full |
| 開発・agent方針文書、file mode／typeの変更、安全と確認できないrename前後のpath | full |
| 入力欠落・破損・不完全・未commit変更・shallow履歴・確認不能 | full |
| 手動の `force_full=true`、すべてのプレリリース | full |

renameの変更前後を確認し、取得した変更一覧をGit treeの完全な一覧と照合します。workflow全体のpath filter、最後のpushだけ、cache hit、AIによる差分解釈には依存しません。省略対象を広げるには方針変更のreviewが必要で、その変更自体もfull対象です。

## 基準と必須結果

基準には、祖先commitに対する正規の `main` のCross-platform CI成功が必要です。ソース・tree・判定方針のfingerprintと成功したrun attemptが一致し、`ci-coverage` の証跡で4対象・browser・manifest・実時間試験の全成功を確認します。PRの結果や選択実行だけの結果はfull基準を更新できません。証跡の欠落・期限切れはfullへ戻るため、初回導入時は最適化より先にfullを実行します。

差分は、このfull基準から実際に試験するcheckoutまでの累積です。PRではmerge treeも含めます。通信部分の変更が失敗した後にCSSだけを変更しても、未検証の通信変更は隠れません。基準の検索は最近の履歴に範囲を限定し、使える証跡が見つからなければ省略しません。

`ci-required` は現在のattemptの実際のjobとstepを必ず確認します。必要な確認の失敗・取消・欠落・予期しないskipは不合格です。正当な選択実行は **「この変更範囲では実時間試験を未実行」** とfull基準へのlinkを示し、全検証成功とは表示しません。条件付きCIをmerge判定に使う前に、branch protection／rulesetで **`ci-required`** を必須にしてください。このworkflowがrepositoryの保護設定を変更することはありません。

全試験を要求する場合は **Actions → Cross-platform CI → Run workflow** で対象refを選び、既定の **force_full** を有効のまま実行します。強制skipの指定はありません。release workflowは変更影響の判定を参照しません。失敗jobだけの再実行で現在のattemptに完全な一覧がない場合は、全jobを再実行してください。不完全なattemptからfull証跡は発行しません。

## キャッシュと計測

developmentとtrusted-mainのcacheは分離したままです。実時間試験をすべて選択して成功したmainのnative jobだけがtrusted-mainへ保存できます。releaseは正確なソースのcacheだけを復元し、分離したsynthetic鍵試験を含めてnative確認を再実行します。

集約結果には固定名のjobとcache stepの所要時間も含めます。各native jobは、固定suite名・経過秒・結果・終了codeだけを記録します。引数、環境変数、file内容、生logは計測記録へ含めません。計測の書込みに失敗しても、試験の失敗を成功へ変えません。cold／warmを分け、runner時間の合計と全体の待ち時間も区別して比較します。

従来のfull CIはソース `308d252` で27分10秒、cache missのWindowsが26分26秒でした。cache hitのLinux amd64／arm64／macOSは14分08秒／13分31秒／16分16秒です。これは観測基準であり、試験構成変更後の所要時間を保証する値ではありません。実時間待機を省略した短縮は選択実行として記録し、同じfull coverageでの性能改善とは区別します。[基準run](https://github.com/webkaz-labs/sobalink/actions/runs/37412218933)

受入ではfullと見た目だけの選択実行の両方、手動force-full、不明・helper・依存・renameの変更、target証跡の欠落・失敗・skip注入を確認します。実端末のネットワーク受入は別に残ります。

## ネイティブport fixtureの移植性

同じ端点にTCPとUDPが必要なfixtureは、正確なIPv4／IPv6のloopbackアドレスで両方を予約します。共通のテストhelperは最大100個の分散した候補を実bindで確認し、実際の起動直前まで両socketを保持します。アプリケーションが予約したportの除外を維持し、候補不足やcleanupの失敗を返します。TCPとUDPで異なる除外範囲と、有限のcleanupを決定的なテストで確認します。WireGuard単体のfixtureでは、2つのUDP予約を同時に保持して相手ごとの端点を区別します。

helperはテストfileだけがimportします。製品のlistener方針・選択した端点・時間制限・assertion失敗の扱いは変更しません。ネイティブ対象が失敗した場合は引き続き `ci-required` が失敗し、full成功証跡は発行しません。
