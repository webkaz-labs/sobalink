# LAN送信境界の分離実験

[English](README.en.md) · [中継配置・運用の検証](../relay-setup/README.ja.md)

固定した `tailscale.com v1.104.0` のコピーで、一部の送信境界を検証する独立fixtureです。
**製品機能、物理LAN／VPNの隔離、process全体の外部送信ゼロの証明ではありません。**
アプリの依存・リリースは変更しません。各OSの実際の合否はCIの許可項目限定artifactで
確認します。この説明自体は成功記録ではありません。以前のLinux限定UDP実証はPR #9に残ります。

## 検証する範囲

- 通常UDP、batch、disco用transport、netcheck送信、lazy-endpointのcookie-style入口。
  高速batch／fallback、policy未設定、retry時の失効を含む
- IPv4／IPv6のfake writerでprivate／ULA、別prefix、global、mapped IPv4、zone、portを判定
- native IPv4／IPv6 loopback UDPを各familyで許可7datagram・拒否5入口・失効1件確認。
  OS設定を変えずに動かすため、同じloopbackアドレスの異なる一時portを利用。
  別アドレスの拒否はfakeで確認し、以前のLinux実証では別アドレスを使用
- DERP URL／region TCPを選択済み数値relayへ限定。family違い、hostname／DNS fallback、
  proxy、独自URL dialerによる迂回、失効後の次回dialを検証。native IPv4／IPv6で合計
  許可4接続・拒否4入口・失効2件。TCP接続の検証でありTLS／DERP handshakeの検証ではない
- HTTPS／HTTP-only／ICMP診断とstandalone UDP初期化は、client／resolver／pinger／socket
  構築前に停止。DNS fallbackはcache有無の両方で拒否。数値STUN宛先もmagicsockの送信guardが必要
- Linux raw-discoveryはraw socket構築前に無効化。ICMP packetやraw socketは実際には使わない
- UDP／TCPのguardを取り除いたnegative controlがfake-boundary試験で失敗することを必須にする

専用workflowはLinux x64／ARM64、macOS ARM64、Windows x64でrace付きnative試験を実行します。
必須試験のskip・欠落は失敗。IPv6が利用できなくても成功扱いにしません。
namespace・route・FW・capability・起動設定・アカウント・OSセキュリティ設定は変更しません。

## 分離と再現

`prepare_engine.py` は変更対象の各上流ファイルのSHA-256を確認し、固定module全体を新しい
ディレクトリにコピーして試験hookを追加します。3packageの上流テストを除き、限定fixtureを配置。
製品package全体と実依存はcompileしますが、上流の全回帰試験ではありません。
constructorへpolicyを接続していないため、そのまま配布できる実装ではありません。

Go 1.27.1／Python 3で[英語版の再現手順](README.en.md#isolation-and-reproduction)を利用します。
native socket試験は `LAN_GUARD_REAL_SOCKETS=1` による明示的有効化が必要です。
確認済みの実行経路はhosted CI。artifactはhash、架空の試験名、固定OS名、集計結果だけです。
端末一覧、実アドレス、資格情報、ユーザー設定、生ログは公開しません。

## 残る通信経路の静的監査

- UDPは低位single／batch retry境界。netcheckのSendPacketもmagicsock writerへ接続
- DERP URLはdialURL、regionはdialNode／dialContextを介し、各接続を数値relay exact-matchで判定
- HTTP CONNECTはdialNodeUsingProxy入口で拒否。製品のtrusted-relay buildもproxy機能をomit
- netcheckのHTTPS／HTTP-only、ICMPの全体・個別入口は停止。nodeAddrPortはDNS fallback拒否、
  Standaloneも停止して独立UDP writerを作らない
- LinuxのlistenRawDiscoはAF_PACKETと独立loopback self-testより前に停止
- portmapper／captiveportalは製品のValidateBuildがomit tagを要求。logtailはアプリ初期化で無効化。
  このfixtureはそれら全体の動的な無通信証明ではない
- bootstrap、overlay HTTP、明示的loopbackサービス橋渡し、ローカル管理は
  [別のアプリ送信元一覧](../relay-setup/README.ja.md)とbootstrap試験で整理
- tailnet backendは別経路であり、LAN modeから無断で切り替える対象ではない

これは全依存・callback・将来の上流変更の完全性証明ではありません。製品化では全constructorへ
統一policyを接続し、更新時の再監査とprocess全体の観測が必要です。診断を早期停止する試験は、
全診断経路のpacket captureと同じ証拠ではありません。

## interface／VPN／実端末で残る条件

prefixや数値アドレスだけでは送信NICを保証できません。socket-freeモデルは同じCIDRでもVPNへ
流れる例を明示しています。今回のsocketは選択NICへ固定せず、route lookupだけでも判定後の
経路変更との競合が残ります。ULAも同一リンクを意味しません。link-local zoneは別設計が必要で、
この試作では拒否します。

物理LAN限定を主張する前に、承認した検証topology／端末で、選択NICと他NIC、on-linkとrouted、
重複VPN、判定と書込みの間のroute変更、NIC消失、アドレス更新、失効、IPv4／IPv6、zone、
休止復帰、中継停止を確認します。架空payload・拒否側receiver・観測器の陽性対照を用い、
全egress interfaceと相手側を同時に観測する必要があります。

実端末へ進むには端末・ネットワーク・port・一時binary／serviceを明示して承認が必要です。
capture権限、route／VPN／FW変更、ログイン時起動はそれぞれ別承認。このPRでは変更しません。
生captureや実環境情報は非公開のまま、確認済みの一般的な結果だけを共有します。
[2台の中継受入手順](../relay-setup/README.ja.md)は運用確認であり、guardの製品統合やNIC隔離の証明ではありません。
