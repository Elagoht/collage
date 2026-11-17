package types

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The first caller for a key fetches under its own context. When that caller is a
// speculative prefetch whose slot was never rendered, its context is cancelled,
// and its "context canceled" must not become the answer for a caller that is
// still wanted and asked for the same key.
func TestOnce_ACancelledFirstCallerDoesNotPoisonTheKey(t *testing.T) {
	root := NewRenderContext(context.Background(), nil, nil, "en", nil)
	key := NewKey[string]("user")

	speculative, cancel := context.WithCancel(root.Context())
	started := make(chan struct{})
	fetch := func(ctx context.Context) (string, error) {
		select {
		case <-started:
		default:
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "ada", nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := Once(root.WithContext(speculative), key, fetch)
		firstDone <- err
	}()
	<-started

	got := make(chan string, 1)
	gotErr := make(chan error, 1)
	go func() {
		v, err := Once(root, key, fetch)
		got <- v
		gotErr <- err
	}()
	time.Sleep(20 * time.Millisecond) // let the second caller begin waiting
	cancel()

	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled caller got %v, want context.Canceled", err)
	}
	select {
	case v := <-got:
		if err := <-gotErr; err != nil || v != "ada" {
			t.Fatalf("the live caller got %q, %v; want ada, nil", v, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live caller never got an answer")
	}
}

// A fetch that panics must release the callers waiting on it, with an error,
// rather than leaving them to wait out their own deadlines; the panic still
// reaches the caller that ran the fetch.
func TestOnce_APanickingFetchReleasesWaiters(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	key := NewKey[string]("boom")
	started := make(chan struct{})
	release := make(chan struct{})

	panicked := make(chan string, 1)
	go func() {
		defer func() {
			got, _ := recover().(string)
			panicked <- got
		}()
		_, _ = Once(rc, key, func(context.Context) (string, error) {
			close(started)
			<-release
			panic("kaboom")
		})
	}()
	<-started

	waiterErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := Once(rc.WithContext(ctx), key, nil)
		waiterErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case err := <-waiterErr:
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the waiter got %v, want the panic as an error, promptly", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was left waiting after the fetch panicked")
	}
	if got := <-panicked; got != "kaboom" {
		t.Fatalf("the fetching caller recovered %v, want its own panic", got)
	}
}
