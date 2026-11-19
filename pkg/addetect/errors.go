// SPDX-License-Identifier: Apache-2.0

package addetect

import (
	"context"
	"errors"
	"fmt"
)

// ErrorKind classifies analysis failures (API-04).
type ErrorKind uint8

const (
	// KindInternal is an unexpected internal failure.
	KindInternal ErrorKind = iota
	// KindInvalidConfig is an invalid configuration, asset or argument.
	KindInvalidConfig
	// KindUnsupported is an unsupported input or capability.
	KindUnsupported
	// KindMedia is a media-adapter (demux/decode) failure.
	KindMedia
	// KindTiming means no reliable presentation timeline could be established.
	KindTiming
	// KindIO is an I/O failure.
	KindIO
	// KindCanceled means the context was canceled.
	KindCanceled
	// KindResourceLimit means a configured resource limit was reached.
	KindResourceLimit
)

var kindNames = [...]string{
	KindInternal:      "internal",
	KindInvalidConfig: "invalid_config",
	KindUnsupported:   "unsupported_input",
	KindMedia:         "media_failure",
	KindTiming:        "unusable_timing",
	KindIO:            "io",
	KindCanceled:      "canceled",
	KindResourceLimit: "resource_limit",
}

// kindSeparator joins an error kind and its detail in Error().
const kindSeparator = ": "

// String returns the stable machine-readable name of the kind.
func (k ErrorKind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return kindNames[KindInternal]
}

// Error is the error type returned by this package. Use errors.Is with the
// Err* sentinels, or errors.As to inspect Kind.
type Error struct {
	Kind ErrorKind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	// Msg already contains the cause text when built by Errorf.
	detail := e.Msg
	if detail == "" && e.Err != nil {
		detail = e.Err.Error()
	}
	if detail == "" {
		return e.Kind.String()
	}
	return e.Kind.String() + kindSeparator + detail
}

// Unwrap returns the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// Is matches sentinel errors of the same kind.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Msg == "" && t.Err == nil && t.Kind == e.Kind
}

// Sentinels for errors.Is.
var (
	ErrInternal      = &Error{Kind: KindInternal}
	ErrInvalidConfig = &Error{Kind: KindInvalidConfig}
	ErrUnsupported   = &Error{Kind: KindUnsupported}
	ErrMedia         = &Error{Kind: KindMedia}
	ErrTiming        = &Error{Kind: KindTiming}
	ErrIO            = &Error{Kind: KindIO}
	ErrCanceled      = &Error{Kind: KindCanceled}
	ErrResourceLimit = &Error{Kind: KindResourceLimit}
)

// Errorf builds an *Error of the given kind. A wrapped %w cause is preserved.
func Errorf(kind ErrorKind, format string, args ...any) *Error {
	err := fmt.Errorf(format, args...)
	return &Error{Kind: kind, Msg: err.Error(), Err: errors.Unwrap(err)}
}

// KindOf returns the ErrorKind of err (KindCanceled for context errors,
// KindInternal for foreign errors).
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return KindCanceled
	}
	return KindInternal
}

func wrapCtx(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindCanceled, Msg: "analysis canceled", Err: err}
	}
	return &Error{Kind: KindInternal, Err: err}
}
