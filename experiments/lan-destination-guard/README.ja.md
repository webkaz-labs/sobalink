# LAN宛先ガードの分離実験

[English](README.en.md)

これは製品機能やリリース変更ではなく、分離した検証用fixtureです。
成功しても「物理LAN限定」「プロセス全体の外部送信ゼロ」の証明にはなりません。
固定した `tailscale.com v1.104.0` の実パッケージに小さな試験用変更を加え、
送信境界の挙動とLinux CI上の実loopback UDP通信を確認します。

## 検証内容

1. socket-freeポリシーモデルでIPv4/ULA、global・別private宛先の拒否、無効・空の
   ポリシー、宛先変更、batch、relay TCPのexact-match、並行更新、経路不明時の拒否を確認
   経路証明callbackはfakeであり、OSの経路判定は実装していません
2. `prepare_engine.py` が上流 `rebinding_conn.go` のSHA-256を確認して固定moduleを
   新しいディレクトリへコピーし、低位のsingle/batch送信retry境界にhookを追加
   コピー内の上流magicsockテストは除き、限定fixtureのみを配置します。製品コード全体と
   実依存をcompileし、ソース抽出shimsは使いません。上流の全回帰試験ではありません
3. 実パッケージ内のfake writerで通常送信、disco用transport経路、UDP netcheck、batch、
   lazyEndpoint cookie-style返信、高速batch/fallback、nilポリシー、retry中の失効を確認
4. 明示的に有効にしたnative CIでは、権限不要のloopback listenerを利用。
   `127.0.0.1` の許可先に7datagramが届き、`127.0.0.2` の拒否先への5entrypointが
   ポリシーエラーになり、拒否側受信が規定時間内で空のままtimeoutになることを確認
   失効後の送信拒否も確認します。実kernel用batch adapterの利用可否を結果に記録します
5. ガードを外したnegative controlではfake entrypointテストが失敗することを必須にします
   negative controlでは実ソケットや公開宛先への送信を実行しません

アプリのgo.mod、製品constructor、リリースworkflowは変更しません。
namespace・route・firewall・capability・セキュリティ設定・アカウント・端末の変更は
不要です。実通信はloopbackのみで、global/privateの試験アドレスはfakeにだけ渡します。

## 再現

Go 1.27.1とPython 3を用い、[英語版の手順](README.en.md#reproduce)を実行します。
コピー先は未作成のディレクトリにしてください。
`LAN_GUARD_REAL_SOCKETS=1` がnative loopback試験の明示的な有効化です。
workflowは `run_native.py` で試験を実行し、許可した項目だけの集計JSONを保存します。
生ログ、端末一覧、実環境アドレス、ユーザー設定、資格情報は公開しません。

## 低位の2境界が必要な理由

- lazyEndpointのcookie返信はsendUDP/sendUDPBatchを通らず低位batchに直接到達
- 高速batchはsingle writerを通らず、fallbackは各packetでsingle writerを利用
- 接続交換後のretryでも、送信直前のポリシー再判定が必要

試験hookは製品用APIとして未完成です。constructorへの接続はなく、未設定時は拒否し、
判定とOS経路変更の間の競合は解決していません。製品化ではポリシーの有効期間、
同期、エラー分類、rebind設計を別途レビューする必要があります。

## 未証明の範囲

- 物理的な同一リンク、重複するVPN経路、route lookup後の経路変更
- 2台のLAN端末間のpairingと暗号化アプリ通信
- 実discoメッセージ生成やhandshake負荷。今回は対応する送信entrypointを直接呼出し
- 独立したDERP TCP、bootstrap、HTTPS/ICMP診断、Linux raw-disco self-test
- プロセス全体のsyscallと全interfaceのpacket capture
- macOS、Windows、ARM64、実ネットワーク切替
- 上流・アプリ全体の回帰試験

より強い検証には、interface強制の設計と専用topology、または明示的に承認された
使い捨てrunnerのネットワーク構成変更が必要です。この限定試験の成功を、その証明に
読み替えないでください。
