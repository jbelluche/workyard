package output

import (
	"encoding/json"
	"fmt"
	"io"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Envelope struct {
	OK    bool   `json:"ok"`
	Error *Error `json:"error,omitempty"`
}

type CommandError struct {
	Code     string
	Message  string
	Hint     string
	ExitCode int
}

func (e *CommandError) Error() string {
	return e.Message
}

func NewError(code, message, hint string) *CommandError {
	return &CommandError{Code: code, Message: message, Hint: hint, ExitCode: 1}
}

func NewExitError(code, message, hint string, exitCode int) *CommandError {
	return &CommandError{Code: code, Message: message, Hint: hint, ExitCode: exitCode}
}

func WriteJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func WriteJSONLine(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

func WriteErrorJSON(w io.Writer, err error) error {
	ce := AsCommandError(err)
	return WriteJSON(w, Envelope{
		OK: false,
		Error: &Error{
			Code:    ce.Code,
			Message: ce.Message,
			Hint:    ce.Hint,
		},
	})
}

func AsCommandError(err error) *CommandError {
	if err == nil {
		return nil
	}
	if ce, ok := err.(*CommandError); ok {
		return ce
	}
	return &CommandError{Code: "WORKYARD_ERROR", Message: err.Error(), ExitCode: 1}
}

func HumanError(w io.Writer, err error) {
	ce := AsCommandError(err)
	if ce.Hint != "" {
		_, _ = fmt.Fprintf(w, "error: %s\nhint: %s\n", ce.Message, ce.Hint)
		return
	}
	_, _ = fmt.Fprintf(w, "error: %s\n", ce.Message)
}
