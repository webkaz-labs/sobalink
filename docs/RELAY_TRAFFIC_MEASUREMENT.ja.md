# ソースビルドによる relay 通信量の観測

[English](RELAY_TRAFFIC_MEASUREMENT.en.md) · [公開バイナリのオフライン観測](RESOURCE_MEASUREMENT.ja.md)

この workflow は、合成 TCP echo で stock Tailcat/DERP transport を測定します。
ソース `8e6cbb00d60757f701d7d453adb92590cc5d2544` にテスト専用計測を加え、
OS UDP transport を除外してビルドします。公開済み実行ファイル、sobalink の
ファイル・メッセージ protocol、discovery、Web UI polling は対象外です。
測定値を報告するには、完了した hosted 実行と集計 artifact の確認が必要です。

## Fixture と区間

job は指定ソースを別に checkout し、テスト用ファイル 3 個だけを追加して、
追跡対象の本体ソースが未変更であることを確認します。既存の pairing、入場確認、
WireGuard/Tailcat transport、証明書を固定した TLS をそのまま使います。
全 listener と計測 proxy の唯一の転送先は、数値指定の loopback です。
外部 relay、永続 node、アカウント login、OS の network 変更、配布物の変更は
ありません。合成 fixture の鍵はメモリ内だけに保持します。

TCP proxy は TLS を終端・変更せず転送します。成功した stream write を proxy
境界で 1 回だけ数え、relay 向けと relay からの方向、両 peer と relay の間の
2 区間を集計します。同じ転送の read と write を両方数えると、その区間を
二重計上することになります。

次の区間を制限時間付きで観測します。

1. Pairing、relay 起動、認証済み overlay TCP echo stream 1 本の接続
2. Transport が待機する 60 秒間
3. 同じ stream で各 1 MiB、16 MiB、64 MiB の合成 payload を送信し echo を受信
4. 256 バイトの合成 TCP request/echo frame を 1,000 回
5. 同じアプリケーション接続で最低 130 秒間 echo を行い、2 分の lease をまたぐ
   実際の relay 再接続と入場確認を要求
6. Lease 区間後の待機 30 秒間

毎回、echo のバイト数と streaming SHA-256 が一致する必要があります。
アプリケーションの再接続・retry で TCP stream の切断を隠しません。Bulk I/O の
期限は各 60 秒で、小さな frame と lease 中の echo にも期限があります。
Payload 区間は終了後の 200 ミリ秒の待機も観測時間に含め、payload 時間は別記します。
Context は 9 分、プロセスは 10 分の制限で、後片付けにも期限があります。
キャンセル時は全 relay・peer・echo socket を所有する fixture プロセス 1 個を停止します。

## カウンターの意味

P は正常に届き、digest が一致した payload の双方向合計です。N バイトの送信と
その echo なら P は 2N です。R は同じ観測時間内で、relay の 2 区間・双方向を
通る暗号化 stream の実測バイト合計です。有効な payload は通常 2 区間を通るため、
基準値は 2P です。

P、R、R/P、R/(2P)、R−2P を報告します。R/P には 2 区間の経路構成も含みます。
R−2P はこの境界内での余剰暗号化 stream 通信です。負数なら計測失敗・区間不一致で、
ゼロに丸めません。待機中の payload 比は null とし、毎秒バイト数を示します。
起動・待機・payload・lease/再接続を分け、推定した待機通信量を差し引きません。

含むものは TLS handshake/record、DERP framing、WireGuard/overlay 通信、
合成 payload です。外側の TCP/IP/Ethernet header、ACK 専用 packet、カーネルの
再送、ICMP 診断、direct UDP、relay の別接続のローカル入場確認 HTTP は含みません。
Packet capture や WAN 課金通信量ではありません。時間には loopback proxy と
fixture の負荷を含み、実際の WAN/NAT 性能は示しません。

sobalink のファイル転送 framing、メッセージ保存・確認応答、peer/service の
discovery、browser polling は動かしません。End-to-end のファイル転送 overhead や
アプリケーション全体の overhead と表現しないでください。

## 証拠とプライバシー

artifact `source-built-relay-only-linux-amd64-traffic` は JSON 1 ファイルです。
ソース SHA、fixture とテストバイナリの SHA-256、build tags、ネイティブ toolchain、
区間ごとのカウンター・時間、再接続、後片付けの結果を含みます。証拠区分は
`source_built_relay_only`、workload は `tailcat_tcp_echo`、packet bytes は null です。
Runner は許可した集計 schema だけを受け取り、他の出力を破棄します。Endpoint、
パス、鍵、招待、payload 本文、生のログはアップロードしません。不完全・不正な
結果は成功になりません。

既存のクロスプラットフォーム CI、stock relay 受入検証、release workflow、
公開配布物は未変更です。この job は既存の結果を置き換えず、リポジトリや
信頼済み main cache にも書き込みません。
