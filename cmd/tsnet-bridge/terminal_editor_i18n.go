package main

func init() {
	for source, target := range map[string]string{
		"terminal event reader could not be stopped; exit and retry the command":                                            "端末の入力処理を停止できません。終了してコマンドを再実行してください",
		"terminal input contains U+FFFD, which the paste decoder cannot preserve; retype without the replacement character": "入力に置換文字 U+FFFD が含まれています。貼り付け処理で保持できないため、置換文字を含めずに再入力してください",
		"←→ edit Enter=OK Esc=cancel":                                                        "←→ 編集 Enter=決定 Esc=中止",
		"Enter=OK Esc=cancel":                                                                "Enter=決定 Esc=中止",
		"↑↓ select ←→ edit Enter/Esc":                                                        "↑↓ 選択 ←→ 編集 Enter/Esc",
		"↑↓ Enter=OK Esc=cancel":                                                             "↑↓ Enter=決定 Esc=中止",
		"Up/Down select · Left/Right edit · Enter accepts · Esc cancels":                     "↑↓ 選択 · ←→ 編集 · Enter 決定 · Esc キャンセル",
		"unset TEA_TRACE before interactive input; terminal input logging is disabled":       "対話入力の前に TEA_TRACE を解除してください。端末入力のログ記録は無効です",
		"terminal input is not valid UTF-8; retry with a UTF-8 terminal":                     "端末の入力が正しい UTF-8 ではありません。UTF-8 の端末で再実行してください",
		"Arrow-key editing is unavailable in this terminal. Type an answer and press Enter.": "この端末では矢印キーによる編集を利用できません。回答を入力して Enter を押してください",
		"Up/Down selects one option; you can also type an answer.":                           "↑↓ で1項目を選択。回答を直接入力することもできます",
		"Left/Right edit · Enter accepts · Esc cancels":                                      "←→ 編集 · Enter 決定 · Esc キャンセル",
		"Paste plain single-line text; control characters were not inserted.":                "改行のない通常の文字列を貼り付けてください。制御文字は入力していません",
		"Input is too long (maximum 16 KiB); shorten it and retry.":                          "入力が長すぎます（最大16 KiB）。短くして再入力してください",
		"terminal input limit reached; retry the command with a shorter answer":              "端末入力の上限に達しました。回答を短くしてコマンドを再実行してください",
		"terminal input could not be stopped; exit and retry the command":                    "端末入力を停止できません。終了してコマンドを再実行してください",
		"terminal editor failed; no changes made":                                            "端末での入力処理に失敗しました。変更はありません",
		"terminal output failed":                                                             "端末への表示に失敗しました",
		"interactive terminal unavailable":                                                   "対話用の端末を利用できません",
	} {
		japaneseCatalog[source] = target
	}
	japanesePatterns = compileTranslations(japaneseCatalog)
}
