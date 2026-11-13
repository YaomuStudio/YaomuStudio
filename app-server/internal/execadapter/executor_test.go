package execadapter

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type brokenTransport struct {
	executes atomic.Int32
	kills    atomic.Int32
	killErr  error
	done     chan struct{}
	once     sync.Once
}

func (t *brokenTransport) Send(frame Frame) error {
	if frame.Op == OpExecute {
		t.executes.Add(1)
	}
	return errors.New("injected broken pipe")
}
func (t *brokenTransport) Receive() (Frame, error) { <-t.done; return Frame{}, io.EOF }
func (t *brokenTransport) Kill() error             { t.kills.Add(1); return t.killErr }
func (t *brokenTransport) Close() error            { t.once.Do(func() { close(t.done) }); return nil }

func TestExecutorConfirmsStopOnTransportFailure(t *testing.T) {
	tr := &brokenTransport{done: make(chan struct{})}
	client := NewClient(tr, nil)
	t.Cleanup(func() { _ = client.Close() })
	executor := &Executor{Client: client}
	result := executor.Execute(context.Background(), testRequest(), nil, nil)
	if result.TransportErr == nil || tr.kills.Load() == 0 {
		t.Fatalf("transport failure skipped tree stop confirmation: result=%+v kills=%d", result, tr.kills.Load())
	}
	_ = executor.Execute(context.Background(), testRequest(), nil, nil)
	if tr.executes.Load() != 1 {
		t.Fatal("broken executor accepted another execution")
	}
}

func TestExecutorQuarantinesUnconfirmedStop(t *testing.T) {
	tr := &brokenTransport{done: make(chan struct{}), killErr: errors.New("injected stop not confirmed")}
	client := NewClient(tr, nil)
	t.Cleanup(func() { _ = client.Close() })
	executor := &Executor{Client: client}
	_ = executor.Execute(context.Background(), testRequest(), nil, nil)
	_ = executor.Execute(context.Background(), testRequest(), nil, nil)
	if tr.executes.Load() != 1 {
		t.Fatal("unconfirmed execution was followed by another dispatch")
	}
}
