package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWatchStdinShutdownCancelsOnEOF(t *testing.T) {
	t.Setenv("YAOMU_SHUTDOWN_ON_STDIN", "1")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = old
		_ = reader.Close()
		_ = writer.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	watchStdinShutdown(cancel)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stdin EOF did not cancel the core context")
	}
}

func TestWatchStdinShutdownIgnoredWithoutEnv(t *testing.T) {
	t.Setenv("YAOMU_SHUTDOWN_ON_STDIN", "")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = old
		_ = reader.Close()
		_ = writer.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watchStdinShutdown(cancel)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("stdin EOF canceled the core context without YAOMU_SHUTDOWN_ON_STDIN=1")
	case <-time.After(200 * time.Millisecond):
	}
}
