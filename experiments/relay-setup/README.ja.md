# 中継の配置・運用を簡単にする独立検証

[English](README.en.md)

最小案は、既存のローカルアドレスと高位ポートを選び、明示的に「この端末を中継にする」→ 相手を非公開の招待でペアリングする流れです。アドレス一覧、保存済みID、TLS pin、招待確認、アプリ全体の停止は既存機能です。この実験は試験の追加であり、遠隔インストール・自動選出・リリースへの機能追加ではありません。

## 検証内容と限界

- native lifecycle試験は一時的なloopback listenerだけを使用します。ポート競合、認証bootstrap必須、起動取消、管理用ポートの予約、TLS待受、停止、同じIDでの再起動を確認します。IP変更では旧証明書を拒否し、明示的なID更新ではpinが変わることも確認します。
- bootstrap試験は実装の関数をメモリ内TLSで実行します。数値IPの固定宛先、proof送信前のpin検証、redirect拒否、架空proxy設定の影響がないことを確認します。招待取消と仮の中継認証がアプリ権限を付与しないことも確認します。短時間のlease・slot解放は模擬試験です。
- 既存のopt-in統合試験は、実Tailcat/WireGuardとloopback TLS DERP、双方向TCP/UDP、未許可キー拒否、取消、停止、実際の2分間の接続leaseをまたぐ順序付き通信を確認します。この試験では直接UDP underlayをコンパイル時に無効化します。
- native試験は専用の非特権hosted CIで4環境を対象に実行します。成功はそのrunnerのloopback条件に限ります。実LAN、実端末の休止・ログイン起動、配布物の導入、別の実中継へのfailover、process全体の外部流出ゼロは証明しません。実際の結果はallowlist形式のartifactで確認し、本書自体を成功記録としません。

## 最小の操作フロー

1. 起動中の非loopback・privateアドレス候補を表示します。先頭候補を自動決定しません。VPNもprivate IPを持つためです。既存一覧はlink-local IPv6を除外しますが、物理NICとVPNの分類や、以後のOS経路のNIC固定は行いません。
2. 選んだアドレス・高位ポートへの中継起動を明示します。IDは一度だけ保存し、同じ設定の繰り返しでは保持します。「設定済み」と「待受可能」を分け、競合時は別ポートを提案します。firewallや他アプリを勝手に変更しません。
3. 相手の公開IDに対する短命な招待を作り、相手・期限・endpoint・pinを確認してから設定・参加します。アプリの信頼は別途承認します。LAN招待のQR表示は本実験で実装していません。検証対象はJSON/file/stdinです。招待JSONや将来のQRは秘密のcapabilityとして扱い、公開・ログ記録しません。
4. 設定・待受・相手への到達性を別表示します。内蔵中継はアプリ全体の停止で終了し、再起動では保存済みIDを使います。切れた通信はアプリが再接続します。2分間のsocket leaseは接続資源の上限であり、ホスト選出のleaseではありません。
5. ログイン時起動はOSごとの操作を示す別のopt-inです。この実験で遠隔導入、秘密鍵コピー、特権firewall変更、自動起動登録は行いません。

## IP変更・証明書更新・failover

保存証明書は単一IPに結び付き、有効期間は1年です。IP変更での流用は失敗します。起動中engineや保存済みpairがある場合、既存の選択変更には制約があり、明示的な置換は透明な自動更新ではありません。製品化には期限予告、認証済みpin/address更新、rollback、旧endpointへ届かない場合の復旧が必要です。pin検証を弱めて解決してはいけません。

承認済み経路候補と再接続は再利用できますが、この実験は別端末への中継配置や両端が同じ候補に再会することを証明しません。唯一の中継が休止・offlineなら到達不能を示し、未承認のpublic relayへ切り替えません。自動選出・遠隔配置には対象端末の管理権限、ホストごとの独立した秘密鍵、認証されたmembership、競合解決、撤退・healthの上限が別途必要です。

## 最小2端末受入手順（未実施）

承認済みの2台と隔離ネットワーク、架空データだけを使用します。変更前に対象端末・NIC/address・port・一時サービスを確認します。firewall、自動起動、packet capture権限、ネットワーク変更は別途承認が必要です。生captureや実addressは非公開とし、公開するのは架空例・集計結果だけです。

1. Aをoffline設定モードで開始し、`soba lan addresses`から対象を選んで`setup --network lan --host ADDRESS:PORT`を実行します。実際の待受を確認します。Bで`lan identity`を取得し、Aで`lan invite --to PUBLIC_ID --ttl 5m`を発行して非公開で渡します。
2. Bで`lan inspect --json-file INVITATION_FILE`を実行し、相手・中継を照合します。表示どおりのendpoint/pinを`setup --network lan --relay ADDRESS:PORT --certificate SHA256`で設定し、`lan join --json-file INVITATION_FILE`で参加します。対象アプリの信頼と範囲は別途承認します。期限切れ・取消済み・相手違い・pin違いが信頼を置換せず失敗することを確認します。各コマンドの先頭に`soba`を付けます。
3. Bだけに短期間の架空loopback TCP echo/fileサービスを共有し、既知payloadのhashを照合します。130秒後も繰り返し、接続維持と再接続を分けて記録します。ポート競合が復旧可能なエラーとなり、他設定を変更しないことを確認します。
4. A停止でBが到達不能となり、未承認のfallbackが起きないことを確認します。同じアドレス・保存状態でAを再起動し、新しいアプリ接続を確認します。別途承認後にAの休止・復帰を試し、検知・復帰時間を測ります。既存TCPの維持は実際に観測した場合だけ記録します。
5. 別途承認したIP変更・証明書更新では、旧address/pinの失敗を確認してから、明示的な認証済み更新または再pairを行います。唯一の中継ホストが眠った状態で2クライアントのfailoverを証明するには、3台目の承認済み常時稼働中継か別fixtureが必要です。
6. 両方の試験アプリを停止し、一時共有・招待を片付け、listenerが残っていないことを確認します。報告はOS/architecture、架空case名、件数、時間だけとし、hostname、NIC一覧、実endpoint、公開ID、招待、生captureを含めません。

## アプリ側の通信経路一覧

- `internal/lanlink/relay_bootstrap.go`は選択済み数値IPへのTLS通信でproxy/DNS/redirectを使用しません。内蔵中継には選択済みTCP listenerとloopbackの認証HTTP通信があります。
- `internal/core/peers.go`のHTTP/file通信は承認済みbackend dialerを使いredirectを拒否します。`internal/discovery/discovery.go`も渡されたpolicy dialerを使用し、既定HTTP transportを使いません。
- `internal/transport/inbound.go`と`internal/ranges/engine.go`は認証後に検証済み数値loopbackサービスへ接続します。意図されたOS loopback通信として別扱いが必要です。
- `internal/core/diagnostics.go`は転送側でbackend、共有側で許可済みloopbackを確認します。旧`internal/app/rules.go`の共有診断も`internal/config/rules.go`で`127.0.0.1`/`::1`に制限されます。
- `internal/control`、`internal/webui`、`internal/transport/{tcp,udp,socks}.go`はIPCまたはloopbackの管理・アプリ待受です。厳格なLAN policyでも必要なローカル経路を残します。
- `internal/identity/tsnet.go`は別のtailnet backendです。LAN/trusted-relayからの暗黙fallbackを許可しません。この一覧はソース監査であり、動的なprocess全体の無流出証明ではありません。
