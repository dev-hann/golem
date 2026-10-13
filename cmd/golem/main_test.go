package main

import (
	"context"
	"errors"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
)

// stubDeps records the launch call instead of starting a real TUI.
func stubDeps(err error) (deps, *cli.AppOptions, *bool) {
	var (
		opts   cli.AppOptions
		called bool
	)

	return deps{launch: func(_ context.Context, o cli.AppOptions) error {
		called = true
		opts = o
		return err
	}}, &opts, &called
}

func TestRun_NoArgsLaunchesApp(t *testing.T) {
	t.Parallel()

	d, opts, called := stubDeps(nil)
	if got := run(nil, d); got != 0 {
		t.Errorf("run(no args) = %d, want 0", got)
	}
	if !*called {
		t.Fatal("launch not called for bare golem")
	}
	if opts.Socket == "" {
		t.Error("AppOptions.Socket not wired")
	}
	if opts.Start == nil {
		t.Error("AppOptions.Start not wired")
	}
}

func TestRun_TuiSubcommandLaunchesApp(t *testing.T) {
	t.Parallel()

	d, _, called := stubDeps(nil)
	if got := run([]string{"tui"}, d); got != 0 {
		t.Errorf("run(tui) = %d, want 0", got)
	}
	if !*called {
		t.Error("launch not called for golem tui")
	}
}

func TestRun_TuiRejectsExtraArgs(t *testing.T) {
	t.Parallel()

	d, _, called := stubDeps(nil)
	if got := run([]string{"tui", "extra"}, d); got != 2 {
		t.Errorf("run(tui extra) = %d, want 2", got)
	}
	if *called {
		t.Error("launch called despite usage error")
	}
}

func TestRun_LaunchErrorExitsOne(t *testing.T) {
	t.Parallel()

	d, _, called := stubDeps(errors.New("no tty"))
	if got := run(nil, d); got != 1 {
		t.Errorf("run(no args, launch error) = %d, want 1", got)
	}
	if !*called {
		t.Error("launch not called")
	}
}

func TestRun_SetupInvokesWizard(t *testing.T) {
	t.Parallel()

	called := false

	d := deps{setup: func(_ cli.OnboardOptions) error {
		called = true
		return nil
	}}

	if got := run([]string{"setup"}, d); got != 0 {
		t.Errorf("run(setup) = %d, want 0", got)
	}
	if !called {
		t.Error("setup wizard not invoked")
	}
}

func TestRun_SetupErrorExitsOne(t *testing.T) {
	t.Parallel()

	d := deps{setup: func(_ cli.OnboardOptions) error { return errors.New("no tty") }}
	if got := run([]string{"setup"}, d); got != 1 {
		t.Errorf("run(setup, error) = %d, want 1", got)
	}
}

func TestRun_RejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "unknown subcommand", args: []string{"fly", "away"}, want: 2},
		{name: "ask without prompt", args: []string{"ask"}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := run(tt.args, defaultDeps()); got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
