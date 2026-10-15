// Package interrupt turns SIGINT, SIGTERM and SIGHUP into a cancelled
// context, so localllm stops at the next step and tears down as on an error
// instead of dying with containers and host files left behind.
package interrupt

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// ErrInterrupted is returned by steps skipped or cut short by a signal.
var ErrInterrupted = errors.New("interrupted")

var ctx, cancel = context.WithCancel(context.Background())

// Catch starts handling the signals. The first one cancels Context; later
// ones are ignored, so teardown can finish. Without Catch, Context is never
// cancelled.
func Catch() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-c
		cancel()
		for range c {
		}
	}()
}

// Context is cancelled on the first signal. Commands for steps run with it;
// teardown commands must not.
func Context() context.Context { return ctx }

// Err returns ErrInterrupted once a signal has arrived, else nil.
func Err() error {
	if ctx.Err() != nil {
		return ErrInterrupted
	}
	return nil
}

// Ask runs prompt, which reads an answer, and returns false as soon as a
// signal arrives. The abandoned prompt keeps waiting for input; localllm
// exits soon after.
func Ask(prompt func() bool) bool {
	answer := make(chan bool, 1)
	go func() { answer <- prompt() }()
	select {
	case yes := <-answer:
		return yes
	case <-ctx.Done():
		return false
	}
}
