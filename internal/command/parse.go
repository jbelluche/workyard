package command

import (
	"errors"
	"strings"
)

func Parse(input string, shell bool) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("command is empty")
	}
	if shell {
		return []string{"sh", "-c", input}, nil
	}
	if strings.Contains(input, "$(") || strings.Contains(input, "`") {
		return nil, errors.New("command substitution is not allowed unless shell mode is enabled")
	}
	for _, op := range []string{"&&", "||", "|", ";", "<", ">"} {
		if strings.Contains(input, op) {
			return nil, errors.New("shell operators are not allowed unless shell mode is enabled")
		}
	}
	return split(input)
}

func split(input string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}

	for _, r := range input {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if escaped {
		cur.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in command")
	}
	flush()
	if len(args) == 0 {
		return nil, errors.New("command is empty")
	}
	return args, nil
}
