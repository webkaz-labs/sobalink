package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

type prompts struct {
	ctx               context.Context
	in                io.Reader
	scanner           *bufio.Scanner
	out               io.Writer
	terminalHintShown bool
}

func newPrompts(in io.Reader, out io.Writer) *prompts {
	ctx := context.Background()
	if input, ok := in.(interface{ Context() context.Context }); ok {
		ctx = input.Context()
	}
	return &prompts{ctx: ctx, in: in, scanner: bufio.NewScanner(in), out: out}
}

func (p *prompts) readLine(message string) (string, error) {
	if p.choiceTerminal() {
		return p.readTerminalLine(message, nil, "")
	}
	return p.readPlainLine(message)
}

func (p *prompts) readPlainLine(message string) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	if _, _, tty := promptTerminalFiles(p.in, p.out); tty && !p.terminalHintShown {
		if _, err := fmt.Fprintln(p.out, "Arrow-key editing is unavailable in this terminal. Type an answer and press Enter."); err != nil {
			return "", err
		}
		p.terminalHintShown = true
	}
	restore, err := preparePromptInput(p.in)
	if err != nil {
		return "", err
	}
	defer restore()
	// Never wait for input when the question itself could not be displayed.
	if n, err := io.WriteString(p.out, message); err != nil {
		return "", err
	} else if n != len(message) {
		return "", io.ErrShortWrite
	}
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", errors.New("canceled; no changes made")
	}
	// Scanner may already hold later answers from an earlier read. Do not
	// accept a buffered confirmation after an interrupt.
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	return p.scanner.Text(), nil
}
func (p *prompts) ask(message string) (string, error) { return p.askChoice(message, nil, "") }

func (p *prompts) askChoice(message string, choices []promptChoice, current string) (string, error) {
	for {
		var line string
		var err error
		if p.choiceTerminal() {
			line, err = p.readTerminalLine(message, choices, current)
		} else {
			line, err = p.readPlainLine(message)
		}
		if err != nil {
			return "", err
		}
		answer := strings.TrimSpace(line)
		var problem string
		if !utf8.ValidString(line) {
			problem = "Input is not valid UTF-8. Use a UTF-8 terminal and enter the value again, or q to cancel."
		} else if strings.ContainsFunc(answer, unicode.IsControl) {
			problem = "Input contains terminal control characters. Enter plain text, or q to cancel."
		}
		if problem != "" {
			// Do not echo rejected bytes: escape sequences can alter the display,
			// and replacement characters would hide the original encoding problem.
			if _, err := fmt.Fprintln(p.out, problem); err != nil {
				return "", err
			}
			continue
		}
		if cancelInput(answer) {
			return "", errors.New("canceled; no changes made")
		}
		return answer, nil
	}
}
func (p *prompts) confirm(message string, yes bool) error {
	if yes {
		return nil
	}
	answer, err := p.ask(message + " [y/N]: ")
	if err != nil {
		return err
	}
	if !affirmativeInput(answer) {
		return errors.New("canceled; no changes made")
	}
	return nil
}
