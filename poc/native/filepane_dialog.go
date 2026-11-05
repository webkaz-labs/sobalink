package main

import "github.com/egoist/mygo"

// Construct on the UI thread: workers use the captured parent, never query UI
// state. Keeping dialog options here leaves the screen definition declarative.
func nativeFilePicker(t fileText) func(bool) ([]string, error) {
	parent := mygo.FocusedWindow()
	return func(directory bool) ([]string, error) {
		title := t("Choose files to review", "送信を確認するファイルを選択")
		if directory {
			title = t("Choose destination", "保存先を選択")
		}
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: title, Multiple: !directory, Directory: directory})
	}
}
