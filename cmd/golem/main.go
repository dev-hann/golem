// Command golem is the golem agent server and CLI.
//
// Subcommands: serve (unix-socket agent server), chat (line REPL client),
// ask (one-shot prompt). See docs/ROADMAP.md for implementation status.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/golem-agent/golem/internal/cli"
	anthropicgo "github.com/golem-agent/golem/internal/llm/anthropic"
)

// defaultModel is used when GOLEM_MODEL is unset (config file lands in
// roadmap phase 5).
const defaultModel = "claude-opus-5"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run executes the CLI and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "ask":
		return runAsk(ctx, args[1:])
	case "serve":
		return runServe(ctx)
	case "chat":
		return runChat(ctx)
	default:
		usage(os.Stderr)
		return 2
	}
}

// runAsk runs the one-shot prompt subcommand.
func runAsk(ctx context.Context, rest []string) int {
	if len(rest) == 0 {
		usage(os.Stderr)
		return 2
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem: resolving cwd: %v\n", err)
		return 1
	}

	provider := anthropicgo.NewProvider(os.Getenv("ANTHROPIC_API_KEY"), "")
	if err := cli.Ask(ctx, cli.AskOptions{
		Provider: provider,
		Model:    model(),
		Root:     root,
		Prompt:   strings.Join(rest, " "),
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	return 0
}

// runServe runs the agent server subcommand.
func runServe(ctx context.Context) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem: resolving cwd: %v\n", err)
		return 1
	}

	if err := cli.Serve(ctx, cli.ServeOptions{
		Provider: anthropicgo.NewProvider(os.Getenv("ANTHROPIC_API_KEY"), ""),
		Model:    model(),
		Root:     root,
		Socket:   cli.DefaultSocketPath(),
		Sessions: cli.DefaultSessionsDir(),
		OnReady: func(addr string) {
			fmt.Fprintf(os.Stderr, "golem serving on %s\n", addr)
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	return 0
}

// runChat runs the line REPL subcommand.
func runChat(ctx context.Context) int {
	if err := cli.Chat(ctx, cli.ChatOptions{
		Socket: cli.DefaultSocketPath(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	return 0
}

// model resolves the model override.
func model() string {
	if m := os.Getenv("GOLEM_MODEL"); m != "" {
		return m
	}

	return defaultModel
}

// usage prints the CLI help.
func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: golem <command> [args]")
	_, _ = fmt.Fprintln(w, "  golem serve            start the agent server (unix socket)")
	_, _ = fmt.Fprintln(w, "  golem chat             line REPL against the server")
	_, _ = fmt.Fprintln(w, "  golem ask <prompt>     one-shot prompt")
	_, _ = fmt.Fprintln(w, "env: ANTHROPIC_API_KEY, GOLEM_MODEL")
}
