package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsContextCancellationRecognizesWrappedAndDriverErrors(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		fmt.Errorf("fetch usage: %w", context.Canceled),
		errors.New("read provider pause state: context canceled"),
	} {
		if !isContextCancellation(err) {
			t.Errorf("isContextCancellation(%q) = false", err)
		}
	}

	for _, err := range []error{
		nil,
		context.DeadlineExceeded,
		errors.New("context canceled while describing an upstream failure"),
		errors.New("provider unavailable"),
	} {
		if isContextCancellation(err) {
			t.Errorf("isContextCancellation(%v) = true", err)
		}
	}
}
