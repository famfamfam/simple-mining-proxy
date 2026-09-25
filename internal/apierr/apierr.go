// Package apierr describes errors that the admin API returns to the client.
//
// The server does not translate anything. Every operator-facing message has a
// stable Key and Params that the web UI translates, and an English Text for
// API clients and logs.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Msg is one operator-facing message.
type Msg struct {
	Key    string         `json:"key"`
	Params map[string]any `json:"params,omitempty"`
	Text   string         `json:"message"`
}

// M builds a message from an English template. Each {name} placeholder is
// replaced with the value that follows name in kv; the pairs also become
// Params. A Msg value is rendered with its Text and sent as a nested message.
func M(key, template string, kv ...any) Msg {
	m := Msg{Key: key, Text: template}
	if len(kv) == 0 {
		return m
	}
	m.Params = make(map[string]any, len(kv)/2)
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		name := fmt.Sprint(kv[i])
		m.Params[name] = kv[i+1]
		pairs = append(pairs, "{"+name+"}", fmt.Sprint(kv[i+1]))
	}
	m.Text = strings.NewReplacer(pairs...).Replace(template)
	return m
}

func (m Msg) IsZero() bool { return m.Key == "" }

func (m Msg) String() string { return m.Text }

// Error is an error with an HTTP status, a machine-readable class (Code), a
// message and per-field validation messages.
type Error struct {
	Status int    `json:"-"`
	Code   string `json:"error"`
	Msg
	Fields map[string]Msg `json:"fields,omitempty"`
}

func (e *Error) Error() string { return e.Text }

func Validation(m Msg, fields map[string]Msg) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "validation", Msg: m, Fields: fields}
}

func NotFound(m Msg) *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Msg: m}
}

func Conflict(m Msg) *Error {
	return &Error{Status: http.StatusConflict, Code: "conflict", Msg: m}
}

func PoolCheckFailed(m Msg) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "pool_check_failed", Msg: m}
}

func Internal(m Msg) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Msg: m}
}

// PoolNotFound is shared by every pool operation.
func PoolNotFound(id string) *Error {
	return NotFound(M("pool_not_found", "pool not found: {id}", "id", id))
}

// From converts any error to *Error; unknown errors become 500.
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(M("internal", "{error}", "error", err.Error()))
}
