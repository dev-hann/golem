// Command golem is the golem agent server and CLI.
//
// Bare `golem` (or `golem tui`) starts the server if needed and runs the
// TUI. Other subcommands: serve, chat, ask. See docs/ROADMAP.md for status.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/config"
)

// deps injects the app launcher and setup wizard so the TUI path is
// testable without a terminal.
type deps struct {
	launch func(ctx context.Context, opts cli.AppOptions) error
	setup  func(opts cli.OnboardOptions) error
}

func main() {
	os.Exit(run(os.Args[1:], defaultDeps()))
}

// defaultDeps wires production dependencies.
func defaultDeps() deps {
	return deps{launch: cli.RunApp, setup: cli.RunOnboard}
}

// run executes the CLI and returns the process exit code.
func run(args []string, d deps) int {
	if len(args) == 0 || args[0] == "tui" {
		var rest []string
		if len(args) > 1 {
			rest = args[1:]
		}

		return launchApp(rest, d)
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
	case "setup":
		return runSetup(d)
	default:
		usage(os.Stderr)
		return 2
	}
}

// runSetup runs the provider setup wizard.
func runSetup(d deps) int {
	if err := d.setup(cli.OnboardOptions{ConfigPath: config.DefaultPath()}); err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	return 0
}

// launchApp is the bare-`golem` path: ensure the server is running, then
// start the TUI against it.
func launchApp(rest []string, d deps) int {
	if len(rest) > 0 {
		usage(os.Stderr)
		return 2
	}

	if err := d.launch(context.Background(), cli.AppOptions{
		Socket:     cli.DefaultSocketPath(),
		ConfigPath: config.DefaultPath(),
		Start: func() error {
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("golem: resolving executable: %w", err)
			}

			return cli.SpawnServe(exe)
		},
		Stderr: os.Stderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	return 0
}

// providerFor resolves the configured provider for this process.
func providerFor() (cli.ProviderChoice, error) {
	choice, err := cli.ResolveProvider(config.DefaultPath(), os.Stderr)
	if err != nil {
		return choice, err
	}

	fmt.Fprintf(os.Stderr, "golem provider: %s (model %s)\n", choice.ID, choice.Model) //nolint:errcheck // terminal output

	return choice, nil
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

	choice, err := providerFor()
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	if err := cli.Ask(ctx, cli.AskOptions{
		Provider: choice.Provider,
		Model:    choice.Model,
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

	choice, err := providerFor()
	if err != nil {
		fmt.Fprintf(os.Stderr, "golem: %v\n", err)
		return 1
	}

	if err := cli.Serve(ctx, cli.ServeOptions{
		Provider:   choice.Provider,
		ProviderID: choice.ID,
		Model:      choice.Model,
		Root:       root,
		Socket:     cli.DefaultSocketPath(),
		Sessions:   cli.DefaultSessionsDir(),
		OnReady: func(addr string) {
			fmt.Fprintf(os.Stderr, "golem serving on %s\n", addr) //nolint:errcheck // terminal output
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

// usage prints the CLI help.
func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage: golem [command]")
	_, _ = fmt.Fprintln(w, "  golem                 start the TUI (starts the server if needed)")
	_, _ = fmt.Fprintln(w, "  golem tui             same as bare golem")
	_, _ = fmt.Fprintln(w, "  golem setup           choose a provider and write the config")
	_, _ = fmt.Fprintln(w, "  golem serve           start the agent server (unix socket)")
	_, _ = fmt.Fprintln(w, "  golem chat            line REPL against the server")
	_, _ = fmt.Fprintln(w, "  golem ask <prompt>    one-shot prompt")
	_, _ = fmt.Fprintln(w, "env: ANTHROPIC_API_KEY, ZAI_API_KEY, OPENAI_API_KEY, GOLEM_PROVIDER, GOLEM_MODEL")
}
