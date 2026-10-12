package main

import (
	"testing"
)

func TestRun_RejectsMissingOrUnknownSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no args", args: []string{}, want: 2},
		{name: "unknown subcommand", args: []string{"fly", "away"}, want: 2},
		{name: "ask without prompt", args: []string{"ask"}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args); got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
