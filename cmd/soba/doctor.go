package main

import (
	"errors"
	"io"
)

func doctorCommand(args []string, ja bool, out io.Writer, request actionRequest) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, text(ja, doctorHelpEN, doctorHelpJA)+"\n")
		return err
	}
	f := commandFlags("doctor", ja, out)
	id := f.String("service", "", text(ja, "active reviewed service ID", "開始済みの確認したサービス ID"))
	port := f.Int("port", 0, text(ja, "one approved port; required for a range", "許可範囲内の 1 ポート（範囲指定のサービスでは必須）"))
	tcp := f.Bool("tcp", false, text(ja, "explicitly attempt one TCP connection; application health stays unverified", "TCP 接続を 1 回試す（アプリの正常動作は未確認のまま）"))
	if err := parseFlags(f, args, ja); err != nil {
		return err
	}
	if (*tcp && *id == "") || (!*tcp && (*id != "" || *port != 0)) || *port < 0 || *port > 65535 {
		return errors.New(text(ja, "Use doctor for stored observations, or doctor --service ID --tcp [--port PORT] for one explicit TCP check", "保存済みの観測は doctor、明示的な TCP 確認は doctor --service ID --tcp [--port PORT] を使ってください"))
	}
	return request("diagnostics.run", map[string]any{"serviceId": *id, "port": *port, "probeTCP": *tcp})
}

const doctorHelpEN = `Service diagnostics

  soba doctor
  soba doctor --service ID --tcp [--port PORT]

Without --tcp, show current service observations and the last failure code/time.
An explicit TCP check opens and closes one connection to an active service's
approved target. A range requires one --port. No application data is sent.
TCP success proves transport acceptance only; application behavior, TLS and
credentials remain unverified. Generic UDP health cannot be established this way.
Results include stable codes, check times and Japanese/English next steps.
Diagnostics do not renew grants or change saved service configuration.`
const doctorHelpJA = `サービスの診断

  soba doctor
  soba doctor --service ID --tcp [--port PORT]

--tcp なしでは現在の観測と直近の失敗コード・時刻を表示します。
明示的な TCP 確認では、開始済みサービスの許可した接続先への接続を 1 回開いて
閉じます。ポート範囲には --port を 1 個指定してください。アプリのデータは送信しません。
成功は TCP の受付だけを示します。アプリの動作、TLS、認証情報は未確認です。
汎用的な UDP の正常動作は、この方法では確認できません。
結果には共通のコード、確認時刻、日本語と英語の次の操作を含みます。
診断で許可期限を延長したり、保存済みサービス設定を変更したりしません。`
