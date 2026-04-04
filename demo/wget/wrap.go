package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/u-root/u-root/pkg/core"
)

type input struct {
	Input string `json:"input"`
}

func Run(fn func(args string) (string, error)) {
	if err := run(fn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(fn func(args string) (string, error)) error {
	var in input

	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("decoding input: %w", err)
	}

	out, err := fn(strings.TrimSpace(in.Input))
	if err != nil {
		return err
	}

	return json.NewEncoder(os.Stdout).Encode(map[string]string{"output": out})
}

func RunCommand(cmd core.Command) {
	Run(func(args string) (string, error) {
		var stdout, stderr bytes.Buffer
		cmd.SetIO(os.Stdin, &stdout, &stderr)

		if err := cmd.Run(splitArgs(args)...); err != nil {
			if stderr.Len() > 0 {
				return "", fmt.Errorf("%w: %s", err, stderr.String())
			}

			return "", err
		}

		return stdout.String(), nil
	})
}

func splitArgs(s string) []string {
	if s == "" {
		return nil
	}

	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := range len(s) {
		c := s[i]

		switch {
		case inQuote:
			if c == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(c)
			}
		case c == '"' || c == '\'':
			inQuote = true
			quoteChar = c
		case c == ' ' || c == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}
