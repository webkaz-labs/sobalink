package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func TestPromptsPreserveUTF8AndLineEndings(t *testing.T) {
	for _, input := range []string{"日本語の名前\n", "日本語の名前\r\n", "日本語の名前", " \t日本語の名前\t \n"} {
		t.Run(input, func(t *testing.T) {
			for _, lang := range []string{"en", "ja"} {
				var output bytes.Buffer
				p := newPrompts(iotest.OneByteReader(strings.NewReader(input)), &localeWriter{out: &output, language: lang})
				got, err := p.ask("Rule name [web]: ")
				if err != nil || got != "日本語の名前" || !utf8.ValidString(output.String()) {
					t.Fatalf("language %s: got %q, err %v, output %q", lang, got, err, output.String())
				}
				if lang == "ja" && !hasJapanese(output.String()) {
					t.Fatal("Japanese prompt was not localized")
				}
			}
		})
	}
}

func TestPromptsRejectCorruptedInputAndRetry(t *testing.T) {
	for _, invalid := range []string{"\xe6\x97\xa5\xe6\x9c", "name\x1b[D", "name\x00", "name\x08", "name\u009b31m"} {
		for _, lang := range []string{"en", "ja"} {
			var output bytes.Buffer
			p := newPrompts(strings.NewReader(invalid+"\n日本語\n"), &localeWriter{out: &output, language: lang})
			got, err := p.ask("Rule name [web]: ")
			if err != nil || got != "日本語" {
				t.Fatalf("language %s: got %q, err %v", lang, got, err)
			}
			if !utf8.ValidString(output.String()) || strings.Contains(output.String(), invalid) || strings.Contains(output.String(), "\x1b") {
				t.Fatal("rejected input was echoed")
			}
			if lang == "ja" && (strings.Contains(output.String(), "Input is") || strings.Contains(output.String(), "Input contains")) {
				t.Fatal("retry instructions were not localized")
			}
		}
	}
}

func TestPromptsCancellationAfterCorruptedInput(t *testing.T) {
	for _, cancel := range []string{"q", "キャンセル", "取消"} {
		p := newPrompts(strings.NewReader("\xff\n"+cancel+"\n"), io.Discard)
		if _, err := p.ask("Rule name: "); err == nil || err.Error() != "canceled; no changes made" {
			t.Fatalf("cancellation %q: %v", cancel, err)
		}
	}
}

type promptFailWriter struct{ err error }

func (w promptFailWriter) Write([]byte) (int, error) { return 0, w.err }

type promptCountingReader struct{ reads int }

func (r *promptCountingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

func TestPromptsDoNotReadWhenQuestionCannotBeWritten(t *testing.T) {
	writeErr := errors.New("output closed")
	for _, writer := range []io.Writer{promptFailWriter{writeErr}, shortLocaleWriter{}} {
		input := &promptCountingReader{}
		_, err := newPrompts(input, writer).ask("Rule name: ")
		if err == nil || input.reads != 0 {
			t.Fatalf("question failed but input consumed: err %v, reads %d", err, input.reads)
		}
	}
}

func TestPromptsPropagateInputErrorsAndKeepUnreadAnswers(t *testing.T) {
	readErr := errors.New("input closed")
	if _, err := newPrompts(iotest.ErrReader(readErr), io.Discard).ask("Name: "); !errors.Is(err, readErr) {
		t.Fatal(err)
	}
	p := newPrompts(strings.NewReader("一つ目\r\n二つ目\n"), io.Discard)
	for _, want := range []string{"一つ目", "二つ目"} {
		if got, err := p.ask("Name: "); err != nil || got != want {
			t.Fatalf("got %q, want %q, err %v", got, want, err)
		}
	}
	if _, err := p.ask("Name: "); err == nil {
		t.Fatal("end of input did not cancel")
	}
}
