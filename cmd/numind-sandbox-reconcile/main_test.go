package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"numind-server/internal/numind/sandboxreconcile"
)

func TestRunDefaultsToDryRunAndDoesNotApply(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	fake := &fakeRunner{}
	code := runWithFactory(
		[]string{"--config=config_dev.yaml"},
		&stdout,
		&stderr,
		fake.factory(nil),
	)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "mode=dry-run") ||
		strings.Contains(stdout.String(), "mode=apply") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if fake.apply {
		t.Fatal("dry-run unexpectedly applied")
	}
	if fake.runs != 1 {
		t.Fatalf("runs = %d", fake.runs)
	}
}

func TestRunRequiresExplicitApply(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	fake := &fakeRunner{}
	code := runWithFactory(
		[]string{"--apply", "--config=config_dev.yaml", "--limit=5"},
		&stdout,
		&stderr,
		fake.factory(nil),
	)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "mode=apply") ||
		!strings.Contains(stdout.String(), "limit=5") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !fake.apply {
		t.Fatal("apply flag was not passed to factory")
	}
}

func TestParseOptionsRejectsUnsafeLimit(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseOptions(
		[]string{"--config=config_dev.yaml", "--limit=0"},
		&stderr,
	); err == nil {
		t.Fatal("expected error")
	}
	if _, err := parseOptions(
		[]string{"--config=config_dev.yaml", "--broker-socket="},
		&stderr,
	); err == nil {
		t.Fatal("expected broker socket error")
	}
	if _, err := parseOptions(nil, &stderr); err == nil {
		t.Fatal("expected config error")
	}
}

func TestRunReturnsFailureOnServiceError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithFactory(
		[]string{"--config=config_dev.yaml"},
		&stdout,
		&stderr,
		(&fakeRunner{}).factory(errors.New("boom")),
	)
	if code != 1 || !strings.Contains(stderr.String(), "reconcile failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

type fakeRunner struct {
	apply bool
	runs  int
	err   error
}

func (r *fakeRunner) factory(err error) runtimeFactory {
	r.err = err
	return func(_ context.Context, opts options, _ io.Writer) (serviceRunner, func(), error) {
		r.apply = opts.apply
		return r, nil, nil
	}
}

func (r *fakeRunner) Run(context.Context) (sandboxreconcile.Result, error) {
	r.runs++
	return sandboxreconcile.Result{Scanned: 1, WouldApply: 1}, r.err
}
