package render

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// PanicError is the error a panic is converted into by Execute. It carries the
// recovered value and the stack captured at the point of recovery, so a panicking
// data handler or template function is reported like any other fragment failure
// instead of taking the process down.
type PanicError struct {
	// Value is the value passed to panic. Its type is any by language definition:
	// a program may panic with anything at all.
	Value any // any: a recovered panic value is any by the language definition
	// Stack is the goroutine stack captured when the panic was recovered.
	Stack []byte
}

// Error renders the recovered value.
func (e *PanicError) Error() string {
	return fmt.Sprintf("collage: panic: %v", e.Value)
}

// Unwrap returns the recovered value when the code panicked with an error, so
// errors.Is and errors.As reach through to it, and nil otherwise.
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}

// Execute runs fn with an optional timeout and converts a panic into a *PanicError.
// When timeout is greater than zero, fn receives a context derived from ctx with that
// deadline; otherwise it receives ctx unchanged. A nil ctx is treated as
// context.Background(). If the context is already done before fn would run, Execute
// returns its error without calling fn at all.
//
// fn runs on the calling goroutine, deliberately. Running it on a spawned goroutine
// would let Execute return the moment the deadline passed, but the spawned goroutine
// could not be killed: a handler that blocks forever would leak one goroutine per
// call, permanently. The cost of that choice is that the timeout bounds only the
// context fn is handed, not fn itself — a handler that never consults ctx.Done() or
// passes ctx on to the calls it makes can still run past its deadline, and Execute
// will wait for it. Handlers are expected to honour their context.
//
// When fn returns a context error and the context did in fact expire, the error is
// wrapped with the timeout that caused it; errors.Is against context.DeadlineExceeded
// still matches.
func Execute(ctx context.Context, timeout time.Duration, fn func(context.Context) error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = &PanicError{Value: recovered, Stack: debug.Stack()}
		}
	}()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	err = fn(ctx)
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		if timeout > 0 {
			return fmt.Errorf("collage: execution exceeded %s: %w", timeout, err)
		}
		return fmt.Errorf("collage: execution stopped by context: %w", err)
	}
	return err
}
