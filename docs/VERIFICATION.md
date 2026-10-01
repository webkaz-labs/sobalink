# 日本語の導入・動作確認手順

[README に戻る](../README.md) · [English verification report](VERIFICATION.en.md)

更新: 2026-10-01

**いま配布できるのは開発・検証用の実験版です。** 自動テストは成功していますが、実際の tailnet への参加、Windows 標準ユーザーでの認証、RustDesk の双方向遠隔操作は未確認です。このページは、その確認を行うための手順です。成功済みの実機試験を説明するものではありません。

まず [準備](#準備) を確認し、[Windows](#windows) または [Mac](#mac) の手順を行ってください。両方で接続準備ができたら、[RustDesk の設定](#rustdesk) に進みます。[Linux](#linux) の手順もあります。

## <a id="準備"></a>1. 先に用意するもの

- 動作中の RustDesk サーバー。ID サーバーを **hbbs**、中継サーバーを **hbbr** と呼びます
- そのサーバーが参加している Tailscale のネットワーク（tailnet）へ、試験用ノードを追加できること
- 下表のサーバー接続情報。**公開してよい手順と、手元だけで使う実際の設定値は分けてください**
- Mac と Windows の両方で RustDesk を起動できること。ここで確認した画面の項目名は RustDesk **1.4.9** のものです。違う版では表示が変わる場合があります
- 現在の RustDesk のサーバー、プロキシ、UDP、WebSocket の設定を、他人に見えない場所へ控えておくこと

| 必要な値 | 入手先・入力方法 |
| --- | --- |
| ID サーバーの tailnet 名または IP | サーバーの管理者、または Tailscale 管理画面のサーバー詳細で確認します。通常の LAN アドレスではなく Tailscale のアドレスです。`https://` や `:21116` を付けずに入力します |
| 中継サーバーの tailnet 名または IP | hbbs と hbbr が同じホストなら、別の入力は不要です。別ホストなら管理者に確認し、後述の `--relay-host` を使います |
| RustDesk サーバーの公開鍵 | hbbs のデータ保存先にある **`id_ed25519.pub` の中身の1行**を、管理者から受け取ります。ファイル名やパスではなく、その文字列を入力します |

**`id_ed25519`（`.pub` なし）は秘密鍵なので使用・共有しません。** Tailscale の認証キーや、RustDesk の接続パスワードも、ここで求める公開鍵とは別です。公開鍵の生成場所は [RustDesk 公式説明](https://rustdesk.com/docs/en/self-host/client-configuration/#set-key) にあります。

この手順は既定の転送先ポート（hbbs の TCP 21115、TCP/UDP 21116、hbbr の TCP 21117）を使います。サーバー側の待受と、試験ノードからこれらへの最小限の通信許可が必要です。異なるポート構成、サーバーの relay アドレス書換え、設定変更権限の有無は、始める前に管理者へ確認してください。ファイアウォールを無効化して試す手順ではありません。

このツールは端末ごとに**別の Tailscale ノード**を作ります。すでに入っている Tailscale アプリのログインは引き継ぎません。参加を承認してよい端末・tailnet でだけ、後述の認証を実施してください。

## <a id="ダウンロード"></a>2. 成功済みの開発版を入手する

現時点で正式 Release はありません。**以下では Go のビルド環境や mise のインストールは不要です。** 将来用の `mise use` コマンドは、まだ実行しないでください。

1. GitHub にサインインし、[成功済み CI #5](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36857522576) を開きます
2. コミットが **`0f7ec79`**、結果が緑色の成功であることを確認します
3. ページ下部の **Artifacts**（生成ファイル）から、自分の OS と CPU に合う名前をクリックします

| 使う端末 | ダウンロードする Artifact |
| --- | --- |
| Apple Silicon の Mac | `package-darwin-arm64` |
| Intel の Mac | `package-darwin-amd64` |
| Windows x64 | `package-windows-amd64` |
| Linux x64 | `package-linux-amd64` |
| Linux ARM64 | `package-linux-arm64` |

Mac の CPU は Apple メニュー →「この Mac について」で確認できます。`darwin` は macOS、`amd64` は x64 の意味です。Windows ARM64 向けのネイティブ配布はありません。

この CI の版番号は **`0.0.0-dev.5`** です。同梱文書は作成当時の内容なので、日本語の説明はこのページを参照してください。Artifact は 2026-10-15 に期限切れになります。消えていたら、この手順に別の版を混ぜず、次の成功済み版の案内を確認してください。

ダウンロードは **二重の圧縮ファイル**です。外側の `package-…zip` を展開すると、中に本体の `.tar.gz` または `.zip`、チェックサム、ビルド情報などが入っています。本体をさらに展開すると、`bin` フォルダーに実行ファイルがあります。

保存先を「ダウンロード」から変更している場合や、ブラウザーが名前に `(1)` を付けた場合は、以下のダウンロード元パスを実際の名前へ置き換えてください。展開先に同名のフォルダーがすでにある場合は、上書きせず、空の別フォルダーを選びます。その場合は、窓Bを開くときも含め、以降の全コマンドの保存先・移動先を同じフォルダーへ読み替えてください。

**開発版の OS 署名・公証は保証していません。SmartScreen、Gatekeeper などで止まったら、警告を回避せず、その段階で中断してください。**

## <a id="windows"></a>3. Windows で設定・起動する

### 3-1. 普通の PowerShell で展開する

スタートメニューから **PowerShell** を普通に開きます。「管理者として実行」は選びません。標準ユーザーでの受入確認には、管理者グループに属さないアカウントでの実行が必要です。管理者アカウントから普通に開くだけでは、その条件を満たしたとは記録できません。

以下は1行ずつ実行します。`$HOME` は現在のユーザーのホームフォルダーです。

```powershell
Expand-Archive -LiteralPath "$HOME\Downloads\package-windows-amd64.zip" -DestinationPath "$HOME\tsnet-bridge-test\artifact"
Set-Location "$HOME\tsnet-bridge-test\artifact"
Get-FileHash .\tsnet-bridge-0.0.0-dev.5-windows-amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS-windows-amd64 | Select-String 'tsnet-bridge-0\.0\.0-dev\.5-windows-amd64\.zip$'
```

上の2つの結果に出る **64桁のハッシュ値が同じ**か確認します。大文字・小文字の違いは無視できます。違う、または対象行が出ない場合は実行せず中断します。この照合はダウンロード破損の確認であり、正式な署名の確認ではありません。

一致したら本体を展開します。

```powershell
Expand-Archive -LiteralPath .\tsnet-bridge-0.0.0-dev.5-windows-amd64.zip -DestinationPath ..\app
Set-Location ..\app\bin
.\tsnet-bridge.exe version
```

`tsnet-bridge 0.0.0-dev.5` と表示されれば、版と実行開始を確認できています。

### 3-2. 初回の設定を保存する

```powershell
.\tsnet-bridge.exe setup
```

英語で2回だけ入力を求められます。

1. `Tailnet ID-server name or IP:` → [準備](#準備)で確認した **サーバーの tailnet 名または IP** を入力し、Enter
2. `RustDesk public key:` → **`id_ed25519.pub` の中身の1行**を貼り付け、Enter

`Profile saved. No tailnet enrollment has occurred.` は「設定を保存した。まだ tailnet に参加していない」という意味です。続けて出る `ID server`、`Relay server`、`Key` は後で RustDesk に入力する値です。

hbbr が別ホストの場合だけ、通常の `setup` の代わりに、最初から次を使います。`relay.example.ts.net` は**架空の例**なので、確認した中継サーバー名に置き換えてください。

```powershell
.\tsnet-bridge.exe setup --relay-host relay.example.ts.net
```

設定がすでにあると上書きせず止まります。これは正常な保護動作です。[困ったとき](#問題)を参照してください。

### 3-3. 起動用の窓を残す

同じ PowerShell で次を実行します。

```powershell
.\tsnet-bridge.exe run
```

この窓を **窓A** とします。終了せず待ち続けるのが正常です。表示が増えなくても、それだけでは失敗ではありません。ここから Tailscale への通信を始めます。認証前でも、完全なオフライン試験にはなりません。

### 3-4. もう1つの PowerShell で認証する

同じユーザーで、普通の PowerShell をもう1つ開きます。これを **窓B** とします。

```powershell
Set-Location "$HOME\tsnet-bridge-test\app\bin"
.\tsnet-bridge.exe login --no-browser
```

`Private sign-in URL (do not share):` の後ろに、**この端末用の非公開認証 URL** が出ます。自分のブラウザーで開き、正しい Tailscale アカウント・tailnet で、この新規ノードの参加を自分で承認してください。URL、パスワード、認証キーをチャットや Issue に貼らないでください。

`--no-browser` はブラウザーを自動で開かない指定です。認証や外部通信を無効にする指定ではありません。待機は最大約5分です。`approval-required` は管理画面でのノード承認待ちを意味し、接続完了ではありません。

### 3-5. 状態と RustDesk 用の値を確認する

窓A は開いたまま、窓B で実行します。

```powershell
.\tsnet-bridge.exe status --json
.\tsnet-bridge.exe doctor
.\tsnet-bridge.exe settings
```

[状態の読み方](#状態)で判定します。Windows の準備ができたら、Mac でも次の手順を行います。

## <a id="mac"></a>4. Mac で設定・起動する

### 4-1. ターミナルで展開する

「アプリケーション」→「ユーティリティ」→ **ターミナル**を開きます。`sudo` は使いません。

以下は **Apple Silicon** 用です。Intel の場合は、コマンド内の `arm64` をすべて `amd64` に置き換えてください。

```sh
mkdir -p "$HOME/tsnet-bridge-test/artifact" "$HOME/tsnet-bridge-test/app"
unzip "$HOME/Downloads/package-darwin-arm64.zip" -d "$HOME/tsnet-bridge-test/artifact"
cd "$HOME/tsnet-bridge-test/artifact"
shasum -a 256 -c SHA256SUMS-darwin-arm64
```

すべて `OK` ならダウンロード破損の照合は成功です。`FAILED`、対象ファイルなし、展開時の上書き確認が出たら、そのまま続けないでください。チェックサムは正式な署名や公証の代わりではありません。

```sh
tar -xzf tsnet-bridge-0.0.0-dev.5-darwin-arm64.tar.gz -C ../app
cd ../app/bin
./tsnet-bridge version
```

`tsnet-bridge 0.0.0-dev.5` と表示されることを確認します。ブラウザーが外側の ZIP を自動展開していたら、その展開済みフォルダー内のファイルを上の `artifact` に置き、チェックサムの行から進めます。

### 4-2. 初回設定する

```sh
./tsnet-bridge setup
```

1. `Tailnet ID-server name or IP:` → [準備](#準備)で確認した **サーバーの tailnet 名または IP** を入力し、Enter
2. `RustDesk public key:` → **`id_ed25519.pub` の中身の1行**を貼り付け、Enter

Windows と同じサーバー・公開鍵を使います。`Profile saved. No tailnet enrollment has occurred.` が出れば設定保存は成功です。

hbbr が別ホストの場合だけ、通常の `setup` の代わりに次を使います。`relay.example.ts.net` は架空の例なので置き換えます。

```sh
./tsnet-bridge setup --relay-host relay.example.ts.net
```

### 4-3. 起動用のターミナルを残す

```sh
./tsnet-bridge run
```

この **窓A** は開いたままにします。コマンドが終了しないのが正常です。この段階から Tailscale へ通信します。

### 4-4. 新しいターミナルで認証する

ターミナルの「シェル」→「新規ウインドウ」で **窓B** を開きます。

```sh
cd "$HOME/tsnet-bridge-test/app/bin"
./tsnet-bridge login --no-browser
```

表示された非公開の認証 URL を、自分のブラウザーで開きます。Mac 用にも新しいノードの参加を承認します。Windows での承認だけでは Mac は参加しません。URL は共有しないでください。

認証後、窓B で確認します。

```sh
./tsnet-bridge status --json
./tsnet-bridge doctor
./tsnet-bridge settings
```

[状態の読み方](#状態)を確認したら、両方の RustDesk を設定します。

## <a id="状態"></a>5. 接続準備ができたか判定する

`status --json` では、次を確認します。

| 項目 | この段階で期待する値 |
| --- | --- |
| `state` | `ready` |
| `tailnet_state` | `Running` |
| `mode` | `forward` |
| `listeners` | `127.0.0.1:32115`、`127.0.0.1:32116`、`127.0.0.1:32117`。32116 は TCP と UDP のため2回出ます |
| `rustdesk` | `unverified` のままで正常 |

`doctor` の最初の行にも `ready:` が出るか確認します。**コマンドがエラーなく終わっただけでは合格ではありません。** `ready` は設定先の TCP ポートへ届いたという意味で、UDP 登録や画面表示・入力が成功したという意味ではありません。

`settings` は設定を表示するだけです。RustDesk への反映や接続試験は自動では行いません。以下の作業中も、両端の窓A を開いたままにしてください。

## <a id="rustdesk"></a>6. 両方の RustDesk を設定する

ここでは **固定 TCP/UDP 転送**を使います。SOCKS モードは使いません。RustDesk 1.4.9 と OSS server 1.1.16 の組合せでは、プロキシ経由の TCP 登録だけでは操作される側の登録を満たせないためです。[技術的な根拠](ARCHITECTURE.md#rustdesk-proof-requirements)

次の操作を **Windows と Mac の両方**で行います。

1. RustDesk の現在のサーバー設定とプロキシ設定を、手元で控えます。設定を変えると、従来の接続へ影響します
2. ホーム画面の ID 付近にある「⋮」から設定を開き、**「ネットワーク」**へ進みます
3. 設定がロックされている場合は「ネットワーク設定のロックを解除」の権限要件を確認します。管理者承認が必要なのに許可がない場合は、そこで中断します。tsnet-bridge が非管理者設計でも、RustDesk 側の設定権限は別です
4. **「認証/中継サーバー」**を開き、下表を入力して保存します。版によって英語表示なら括弧内の項目を使います

| RustDesk の項目 | 入力する値 |
| --- | --- |
| 認証サーバー（ID Server） | `127.0.0.1:32116` |
| 中継サーバー（Relay Server） | `127.0.0.1:32117` |
| Key（キー） | `tsnet-bridge settings` の `Key:` に表示された、サーバーの公開鍵 |
| API サーバー | この OSS 検証では空欄。既存値があれば控えてから変更 |

5. 「ネットワーク」の **「Socks5/Http(s) プロキシ」**を開き、既存設定を控えてからプロキシの接続先・資格情報を空欄にし、保存します。`127.0.0.1:1080` を入れないでください
6. **「WebSocket を使用する」**を **オフ**にします
7. **「UDP を無効化する」**を **オフ**にします。「UDP を有効にする」ではなく、**無効化スイッチをオフ**にする点に注意してください。カスタムサーバー設定後に表示される項目です。項目が見当たらなければ版・設定制限を確認し、確認済みと扱わないでください
8. ホーム画面へ戻り、各端末自身の ID があり、状態が **「準備完了」**になるか確認します。必要なら RustDesk を終了・起動し直します

上の項目名と経路は [RustDesk 1.4.9 の設定画面ソース](https://github.com/rustdesk/rustdesk/blob/1.4.9/flutter/lib/desktop/pages/desktop_setting_page.dart#L1525-L1713) と [日本語表示](https://github.com/rustdesk/rustdesk/blob/1.4.9/src/lang/ja.rs) で確認しています。画面を実機操作して動作確認したという意味ではありません。

**全参加端末で、中継サーバーは同じ `127.0.0.1:32117` にそろえます。** 片側だけ変える、片側だけツールを使う、実サーバーの tailnet アドレスを片側の RustDesk に直接入れる構成は、この検証と混ぜないでください。サーバーが relay アドレスを書き換える構成では成立しない場合があり、管理者による確認が必要です。

Mac が操作される側になる場合、RustDesk 自体に画面録画・アクセシビリティなどの許可が必要です。未許可なら、その端末の利用者が [RustDesk の Mac 向け説明](https://rustdesk.com/docs/en/client/mac/)を確認して判断します。権限を無断で変更したり、管理者権限へ切り替えて結果を混ぜたりしないでください。

## <a id="双方向"></a>7. 画面表示と操作を、両方向で試す

### 7-1. Mac から Windows を操作する

1. Windows の RustDesk に表示された ID を、手元で確認します
2. Mac の RustDesk の接続先欄へ **Windows の ID の末尾に `/r` を付けて**入力し、「接続」を押します。`/r` はリレー接続を選ぶ指定です
3. Windows 側で、その接続を通常の RustDesk の確認画面から許可するか、接続する本人が RustDesk 内だけで必要なパスワードを入力します。パスワードを Issue・チャット・ログに残しません
4. Windows の画面が実際に表示されるか確認します
5. Windows で空のメモ帳を開き、Mac からマウス移動・クリック・無害な試験文字の入力を行います。表示だけでなく、**入力も届いたか**確認します
6. 接続を切断し、もう一度接続できるか確認します

### 7-2. Windows から Mac を操作する

1. Mac の RustDesk の ID を確認します
2. Windows の RustDesk で **Mac の ID + `/r`** を入力し、接続します
3. Mac 側で接続を許可し、画面が表示されるか確認します
4. Mac で空のテキストエディット書類を開き、Windows からマウス・クリック・試験文字の入力を確認します
5. 切断して再接続します

両方向で、実際の画面と入力の両方が必要です。ID が見えるだけ、`ready` だけ、片方向だけの成功では「双方向の遠隔操作に成功」としません。通常権限のウィンドウを対象にしてください。UAC 画面や権限の高いウィンドウの操作可否は別の確認です。

RustDesk の接続情報で「中継接続」と表示されるかも確認します。`/r` だけで直接通信の試行がすべてなくなるとは保証しません。接続方式や実際に伝播した relay アドレスを確認できなければ、その項目は未確認のまま記録します。

### 7-3. 起動直後・待機後・再起動後を試す

- 両方の RustDesk を終了して起動し直し、登録と接続を確認します
- 両方を起動したまま5分以上待ち、新しい接続を両方向で試します。待機後の通知が届くかを確認する目安であり、UDP の全条件の保証にはなりません
- 下の `stop` と再起動を試し、ログイン状態が再利用されるか、RustDesk が再登録して接続できるか確認します
- 許可された試験環境でだけ、ネットワークの一時切断・復帰を試します。復帰後の `doctor` と実際の接続をそれぞれ確認します

## <a id="終了"></a>8. 停止・再開・ログアウトする

### 一時的に止める（ログインを保存）

窓B で実行します。

Windows:

```powershell
.\tsnet-bridge.exe stop
.\tsnet-bridge.exe status --json
```

Mac / Linux:

```sh
./tsnet-bridge stop
./tsnet-bridge status --json
```

`state` が `stopped` になり、窓A の `run` が終了します。窓A で **Ctrl+C** を押しても停止できます。**停止しても Tailscale の保存ログインや RustDesk の設定は消えません。**

### 保存したログインで再開する

窓A で再び Windows は `.\tsnet-bridge.exe run`、Mac / Linux は `./tsnet-bridge run` を実行し、窓B で `status --json` と `doctor` を確認します。期限切れや承認条件の変更がなければ再認証なしで戻るかを試します。

初回確認後にバックグラウンド起動を使いたい場合は、前面の `run` を停止してから、Windows は `.\tsnet-bridge.exe`、Mac / Linux は `./tsnet-bridge` を実行します。こちらは起動後に入力待ちへ戻ります。終了には `stop` を使います。

### 転送だけ作り直す

稼働中に Windows は `.\tsnet-bridge.exe reconnect`、Mac / Linux は `./tsnet-bridge reconnect`。ログインを保存したまま転送を作り直します。実行中の接続は切れます。tsnet 自体の再起動や再認証をするコマンドではありません。

### 試験ノードを使い終わったらログアウトする

**ツールが稼働していて通信できる間に**、窓B で実行します。停止済みなら、先に `run` で再開します。

Windows:

```powershell
.\tsnet-bridge.exe logout
```

Mac / Linux:

```sh
./tsnet-bridge logout
```

成功時は `logged-out` と表示され、ツールが終了します。その後の `status` が `stopped` になるのは正常です。`local forwarding stopped; server-side logout unconfirmed` は「手元の転送は停止したが、ログアウトを確認できていない」という意味です。通信復旧後に再開してやり直し、成功したことにしないでください。

Tailscale 管理画面からノードを削除する操作は別です。必要なら管理者が対象を確認して行います。プロフィールや認証状態のフォルダーを丸ごと他の端末へコピーしないでください。

最後に、控えた RustDesk のサーバー・プロキシ・UDP・WebSocket 設定へ戻します。このツールは自動で復元しません。

## <a id="linux"></a>Linux で試す場合

ログイン中の一般ユーザーで、`sudo` やサービス登録を使わずに実行します。端末で `uname -m` を実行し、`x86_64` なら `amd64`、`aarch64` なら `arm64` の Artifact を選びます。

以下は x64 用です。ARM64 では、すべての `amd64` を `arm64` に置き換えます。ダウンロード先は実際の場所に合わせてください。`unzip` がなければ、デスクトップの圧縮ファイル管理アプリで外側 ZIP を同じ `artifact` フォルダーへ展開できます。

```sh
mkdir -p "$HOME/tsnet-bridge-test/artifact" "$HOME/tsnet-bridge-test/app"
unzip "$HOME/Downloads/package-linux-amd64.zip" -d "$HOME/tsnet-bridge-test/artifact"
cd "$HOME/tsnet-bridge-test/artifact"
sha256sum --check SHA256SUMS-linux-amd64
```

すべて `OK` であることを確認してから進みます。hbbr が別ホストの場合は、以下の `./tsnet-bridge setup` の行を、最初から `./tsnet-bridge setup --relay-host relay.example.ts.net` に置き換えてください。`relay.example.ts.net` は架空の例なので、確認した中継サーバー名に置き換えます。

```sh
tar -xzf tsnet-bridge-0.0.0-dev.5-linux-amd64.tar.gz -C ../app
cd ../app/bin
./tsnet-bridge version
./tsnet-bridge setup
./tsnet-bridge run
```

`setup` の2つの入力は Mac と同じです。`run` の窓を残し、同じユーザーの別の端末を開きます。

```sh
cd "$HOME/tsnet-bridge-test/app/bin"
./tsnet-bridge login --no-browser
./tsnet-bridge status --json
./tsnet-bridge doctor
./tsnet-bridge settings
```

非公開認証 URL の扱い、参加承認、[状態の判定](#状態)、[RustDesk の設定](#rustdesk)、[停止・ログアウト](#終了)は共通です。Linux を操作側・操作される側にする試験は、それぞれ別に記録します。画面のあるセッションが必要で、Wayland / X11 や RustDesk 側の画面共有許可によって結果が変わるため、Linux バイナリの CI 成功だけでは遠隔操作成功を意味しません。

既定の保存先は `$XDG_CONFIG_HOME/tsnet-bridge`、未設定なら `$HOME/.config/tsnet-bridge` です。Mac は `$HOME/Library/Application Support/tsnet-bridge`、Windows は `%AppData%\tsnet-bridge` です。認証状態を含むため、公開したり同期・共有フォルダーへ置いたりしないでください。

## <a id="問題"></a>困ったとき

| 表示・症状 | 確認すること |
| --- | --- |
| 実行ファイルが見つからない | 外側と内側の2回を展開したか、`bin` に移動したか。Windows は先頭の `.\`、Mac / Linux は `./` も必要です |
| `profile already exists` | 初回設定済みです。同じ設定を再利用するなら `setup` を省きます。修正するなら停止し、保存先の `profile.json` を手元でバックアップして必要箇所だけ編集し、保存後に `run` で再起動します。`reconnect` では設定ファイルを読み直しません。ノードの認証状態は削除しません。ローカル relay ポートは全端末で一致させます |
| `RustDesk public key must be base64 encoding of 32 bytes` | `.pub` の中身を貼ったか、ファイル名や秘密鍵を入れていないか。前後の引用符や余分な改行を入れていないか |
| `needs-login` | 窓B で `login --no-browser`。約5分で待機が終了したら再実行します。待機を Ctrl+C で止めても窓A のツールは残るため、終了したいなら `stop` も行います |
| `approval-required` | Tailscale のノード承認待ち。権限のある管理者へ対象を確認してもらいます |
| `blocked` | `reason` を手元で読みます。サーバーが現在の許可されたピアか、ホスト名が正しいか、ローカルの 32115～32117 が他アプリと競合していないかを確認します |
| `recovering` / `TCP ... unreachable` | サーバーの待受、必要ポートの通信許可、ネットワークを確認。直した後に `doctor`。TCP が通っても UDP 成功とは限りません |
| `stopped` | 同じユーザー・同じ設定保存先で窓A の `run` を起動したか確認します |
| `background startup did not become reachable` | `status` を確認し、既存プロセスがない状態で `run` を使って直接エラーを確認します。プロフィールは消さないでください |
| RustDesk の「キーが一致しません」 | hbbs の公開鍵と両端の Key 欄が一致するか確認します |
| 準備完了にならない / 相手がオフライン | 両端のツールが `ready` か、RustDesk のプロキシが空欄か、「UDP を無効化する」がオフか、サーバー側 UDP 21116 が許可されているか確認します |
| 準備完了だが接続できない | 両端の relay が同一の `127.0.0.1:32117` か、`/r` を付けたか、サーバーの relay 書換えがないかを確認します。実際の互換性が未成立の可能性も残ります |
| 画面が黒い / 入力だけ届かない | RustDesk 自体の画面共有・入力権限、受信許可、通常権限のウィンドウかを確認します。転送の成功とは別の判定です |
| OS のセキュリティ警告 | そこで中断します。保護の無効化や警告回避を検証の条件にしません |

別の保存先を使う上級者向け指定は `--state-dir PATH` です。**必ずコマンドより前**に置き、窓A・窓Bと全操作で同じパスを指定します。初回は既定の保存先のまま進める方が確実です。

### 不具合を報告するとき

OS・CPU・ツールの版、失敗した手順番号、`state`、一般的なエラー文、画面と入力のどちらが失敗したかだけを、必要に応じて伏せて報告してください。実サーバー名/IP、ユーザー名を含むパス、RustDesk ID、実際の設定値、公開鍵の実値、認証 URL、パスワード、キー、トークンは公開 Issue に含めません。

`settings` の結果、ログ、スクリーンショット、状態フォルダーを丸ごと投稿しないでください。`settings --show-secrets` はこの手順では不要です。非公開認証 URL が tsnet のローカルログに残る場合もあるため、ログの一括アップロードは避けてください。

## 検証記録と、まだ残っていること

公開用の記録には固有情報を入れず、次の項目を **成功・失敗・未実施**で区別します。

- [ ] Windows 標準ユーザーでの起動、個別認証、状態保存、再利用、停止、ログアウト
- [ ] Mac での同じ一連の操作
- [ ] Mac → Windows の登録・画面表示・入力・切断・再接続
- [ ] Windows → Mac の登録・画面表示・入力・切断・再接続
- [ ] 待機後の新規接続、アプリ再起動、ツール再起動からの復帰
- [ ] 実際の relay アドレス伝播、サーバー書換え設定との整合
- [ ] Linux x64 / ARM64 の非 root 認証と、両方の役割での相互接続
- [ ] スリープ、ネットワーク変化、UDP 制限、Tailscale の直接経路・DERP 経路、ノード失効や許可取り消し
- [ ] 正式に署名された公開版での mise / Packslip 導入、更新、固定、巻き戻し

**すでに自動試験で確認したこと:** [CI #5](https://github.com/webkaz-labs/tsnet-bridge/actions/runs/36857522576) では Linux x64/ARM64、Mac ARM64/Intel、Windows x64 の全5対象で、競合検出付きテスト、ローカル IPC、vet、整形、再現ビルド、圧縮物・ライセンス・SBOM、同梱バイナリの版・ヘルプ表示が成功しました。Packslip の試験用署名と全対象ファイルの検証も成功しています。実 tailnet の資格情報は使っていません。

Windows の GitHub ホスト実行環境は [管理者として動く](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#administrative-privileges)ため、この成功を標準ユーザーでの成功とは扱いません。試験用署名は正式な配布署名ではありません。上の未確認項目を、自動試験の成功で置き換えることはしません。

詳しい自動試験・セキュリティレビューの記録は [英語版](VERIFICATION.en.md) に残しています。
