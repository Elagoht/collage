package render

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var errHandler = errors.New("collage: test handler failure")

func TestExecute_PassesResultsThrough(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		called := false
		err := Execute(context.Background(), 0, func(context.Context) error {
			called = true
			return nil
		})
		if err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
		if !called {
			t.Error("Execute() did not call fn")
		}
	})

	t.Run("fn error is returned unchanged", func(t *testing.T) {
		err := Execute(context.Background(), time.Second, func(context.Context) error {
			return errHandler
		})
		if !errors.Is(err, errHandler) {
			t.Fatalf("Execute() error = %v, want errHandler", err)
		}
		// Identity, not just Is: a plain failure must not be re-wrapped.
		if err != errHandler {
			t.Errorf("Execute() wrapped a non-context error: %v", err)
		}
	})

	t.Run("nil context is usable", func(t *testing.T) {
		var got context.Context
		//lint:ignore SA1012 a nil context is what this test exercises.
		err := Execute(nil, 0, func(ctx context.Context) error {
			got = ctx
			return nil
		})
		if err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
		if got == nil {
			t.Fatal("fn received a nil context, want a usable one")
		}
	})
}

func TestExecute_TimeoutBoundsTheContext(t *testing.T) {
	t.Run("a handler that honours ctx.Done is cut off", func(t *testing.T) {
		start := time.Now()
		err := Execute(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		})
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute() error = %v, want context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "20ms") {
			t.Errorf("Execute() error = %q, want it to name the timeout that fired", err)
		}
		if elapsed > time.Second {
			t.Errorf("Execute() took %s, want it to return near the 20ms deadline", elapsed)
		}
	})

	t.Run("no timeout leaves the context without a deadline", func(t *testing.T) {
		var hasDeadline bool
		if err := Execute(context.Background(), 0, func(ctx context.Context) error {
			_, hasDeadline = ctx.Deadline()
			return nil
		}); err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
		if hasDeadline {
			t.Error("fn received a deadline although timeout was zero")
		}
	})

	t.Run("an already-cancelled context skips fn entirely", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		called := false
		err := Execute(ctx, time.Second, func(context.Context) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context.Canceled", err)
		}
		if called {
			t.Error("Execute() called fn although the context was already done")
		}
	})
}

// TestExecute_WaitsForAHandlerThatIgnoresItsContext pins the documented limitation of
// running fn on the calling goroutine: the timeout bounds the context fn is handed,
// not fn itself. Spawning a goroutine would let Execute return at the deadline, but
// the abandoned goroutine could never be reclaimed.
func TestExecute_WaitsForAHandlerThatIgnoresItsContext(t *testing.T) {
	const work = 40 * time.Millisecond

	finished := false
	start := time.Now()
	err := Execute(context.Background(), 5*time.Millisecond, func(context.Context) error {
		time.Sleep(work)
		finished = true
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil: fn ignored its context and succeeded", err)
	}
	if !finished {
		t.Error("Execute() returned before fn finished, so fn was abandoned on another goroutine")
	}
	if elapsed < work {
		t.Errorf("Execute() returned after %s, want at least %s: it must wait for fn", elapsed, work)
	}
}

func TestExecute_RecoversPanics(t *testing.T) {
	t.Run("a non-error panic value is carried on PanicError", func(t *testing.T) {
		err := Execute(context.Background(), 0, func(context.Context) error {
			panic("kaboom")
		})

		var panicErr *PanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("Execute() error = %v, want a *PanicError", err)
		}
		if panicErr.Value != "kaboom" {
			t.Errorf("PanicError.Value = %v, want %q", panicErr.Value, "kaboom")
		}
		if len(panicErr.Stack) == 0 {
			t.Error("PanicError.Stack is empty, want the stack captured at recovery")
		}
		if !strings.Contains(panicErr.Error(), "kaboom") {
			t.Errorf("PanicError.Error() = %q, want it to name the panic value", panicErr.Error())
		}
		if panicErr.Unwrap() != nil {
			t.Errorf("PanicError.Unwrap() = %v, want nil for a non-error panic value", panicErr.Unwrap())
		}
	})

	t.Run("an error panic value stays reachable through the chain", func(t *testing.T) {
		err := Execute(context.Background(), 0, func(context.Context) error {
			panic(errHandler)
		})

		var panicErr *PanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("Execute() error = %v, want a *PanicError", err)
		}
		if !errors.Is(err, errHandler) {
			t.Errorf("errors.Is(err, errHandler) = false, want the panicked error to be unwrappable")
		}
	})

	t.Run("a panic in a timed call still cancels the derived context", func(t *testing.T) {
		// The deferred cancel must survive the recover, or every panicking handler
		// would leak its timer until the parent context was done.
		var derived context.Context
		err := Execute(context.Background(), time.Hour, func(ctx context.Context) error {
			derived = ctx
			panic("kaboom")
		})
		var panicErr *PanicError
		if !errors.As(err, &panicErr) {
			t.Fatalf("Execute() error = %v, want a *PanicError", err)
		}
		if derived.Err() == nil {
			t.Error("derived context was never cancelled after the panic")
		}
	})
}

// A context cancelled above Execute is not Execute's timeout, and is not reported
// as one: the message would send someone looking for a slow handler.
func TestExecute_ParentCancellationIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Execute(context.WithoutCancel(ctx), 5*time.Second, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("WithoutCancel: %v", err)
	}

	parent, cancelParent := context.WithCancel(context.Background())
	err = Execute(parent, 5*time.Second, func(ctx context.Context) error {
		cancelParent()
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "exceeded") {
		t.Errorf("parent cancelled: %v", err)
	}

	err = Execute(context.Background(), 10*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "exceeded 10ms") {
		t.Errorf("own timeout: %v", err)
	}

	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelShort()
	err = Execute(short, 5*time.Second, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "exceeded 5s") {
		t.Errorf("a shorter deadline above: %v", err)
	}
}
