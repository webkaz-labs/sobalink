# LAN の送信先を明示して制限する

[English](LAN_DESTINATIONS.en.md) · [LAN 設定](LAN.ja.md) · [中継の運用](RELAY_OPERATIONS.ja.md)

**alpha.5で公開済みです。同一ソースの自動試験・配布結果は[検証記録](VERIFICATION.md)を参照してください。実端末ネットワークでの受入は未完了です。** 任意の `allowed-lan-destinations` は、LAN transport の UDP 送信を明示した private／ULA／loopback の CIDR 内へ、DERP の TCP 接続をその範囲内の正確な設定済み数値中継先へ制限します。物理 NIC への固定、同一リンク、VPN 隔離、ホスト／プロセス全体の外部送信ゼロを保証しません。許可した宛先でも別の NIC や VPN へルーティングされる場合があります。

管理者権限と LAN ルーターの変更は不要で、実施しません。通常の userspace socket を使い、TUN driver、経路／ファイアウォール変更、UPnP／NAT mapping、ネットワーク方針の迂回は行いません。選択したネットワークで必要な通信が禁止されている場合は失敗を示し、別の明示的に許可された設定を選びます。

## 最初の接続前に選ぶ

ローカル Web 設定で、中継先と証明書 pin に合わせて送信先ポリシーを確認します。使うネットワーク prefix だけを明示選択してください。インターフェースから得た候補は自動では有効にしません。中継とポリシーは一緒に保存し、その後に transport を起動します。招待を受け取っても、ローカルの送信先許可は自動で広がりません。

架空の確認済みネットワークを使う例：

```sh
soba setup --network lan --host 192.168.50.10:48443 --policy-mode allowed-lan-destinations --prefix 192.168.50.0/24
```

現在の CLI の正確な host／relay 指定は `soba setup --help` を確認してください。prefix は宛先の範囲であり、送信元 NIC の指定ではありません。IPv6 ULA に対応し、zone 付き link-local と IPv4-mapped IPv6 は拒否します。loopback は同一端末内の確認専用です。

保存済みポリシーはバックエンドを停止して確認・編集します。

```sh
soba lan policy show
soba start --offline
soba lan policy set --mode allowed-lan-destinations --prefix 192.168.50.0/24 --prefix fd50::/64
```

offline 起動の前に稼働中の本体を停止します。確認したポリシーを保存したら通常起動で再開します。選択中とローカルで準備済みの全中継先が範囲内に必要です。相手から認証済みの経路候補もポリシーとの共通部分だけを使い、範囲外へ fallback しません。明示して `trusted-relay` に戻すと、通常の相手への direct 送信と選択中継の診断を許可します。公開中継の自動選択は行いません。

## 適用範囲と停止

- UDP の単発・batch・fallback・retry・探索・cookie 応答は共通の低位送信境界を通ります
- DERP TCP は数値 IP と port の完全一致で許可し、DNS fallback・環境 proxy・独自 dialer による迂回はありません
- 制限付き engine では netcheck の HTTP／HTTPS／ICMP、standalone probe、raw 探索、別経路の WireGuard ICMP pinger を socket 生成前に停止します
- port mapping・captive portal・proxy は既存の製品 build tag により除外します
- ポリシーは engine 世代ごとに固定します。退役時に新規送信・接続を失効させ、登録した UDP／TCP socket を閉じます。新世代には新しいポリシーを渡します。既に届いた packet は取り消せません
- 既存 Tailnet と通常の trusted-relay の動作を維持します。方式間の自動切替はありません

初期接続と探索には元の pin 付き中継が必要です。中継なしのペアリング、公開 rendezvous、一般的な NAT 越えを追加する機能ではありません。ペアの信頼、サービス許可、ファイル受信許可は別々です。

保存失敗は永続的な変更ではありません。同じプロセスでは不確かな許可で再開せず復旧が必要になります。新しいプロセスは実際の保存ファイルを読み、以前のポリシーが残っている可能性があります。保存エラー後の再開前に内容を確認してください。このポリシーを含む LAN ファイルは version 4 となり、以前のバイナリでは開けません。

## ビルドと確認範囲

固定 Tailscale module を生成用ディレクトリへコピーし、レビュー済みで hash を確認する差分だけを適用します。`go run ./cmd/prepare-engine` が準備し、mise と CI にも組み込まれます。module cache は書き換えません。準備なし／未変更の module ではコンパイルが失敗します。配布 inventory に適用後の source identity、元の checksum、patch／source hash とライセンスを残します。

正確なソースの結果は[確認範囲](VERIFICATION.md)へ記録します。自動 loopback 試験や架空宛先の拒否試験は、実 LAN／VPN 境界の証明ではありません。実端末、複数 NIC、VPN の重複、経路変更、休止復帰、IP 更新はリリース後の受入で、端末・ネットワークの選定と必要な操作許可を別途確認します。
