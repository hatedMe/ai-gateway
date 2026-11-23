package translate

import (
	"context"
	"errors"
	"io"
)

// ConversionError contains safe diagnostics, never source payloads.
type ConversionError struct{ Code, Stage, Field, Description string }

func (e *ConversionError) Error() string { return e.Stage + ": " + e.Code + ": " + e.Description }

func conversionError(err error, stage string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, context.Canceled) {
		return err
	}
	var typed *ConversionError
	if errors.As(err, &typed) {
		copy := *typed
		copy.Stage = stage
		return &copy
	}
	code := "unsupported_capability"
	if errors.Is(err, io.ErrUnexpectedEOF) {
		code = "incomplete_stream"
	}
	return &ConversionError{Code: code, Stage: stage, Description: "protocol conversion could not be completed"}
}
