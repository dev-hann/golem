#!/usr/bin/env python3
"""Unified golem TUI drive runner for E2E checks.

Replaces the ad-hoc .tmp/tui_drive*.py scripts. The isolation and cleanup
contracts are built in so they cannot be lost by rewriting the driver:

  * every run gets a fresh temp dir: XDG_DATA_HOME=<tmp>/data and
    XDG_RUNTIME_DIR=<tmp>/run, so the production socket
    (~/.local/share/golem/golem.sock) is never touched
  * golem is built into the temp dir each run (go build), so no shared
    test binary can drift or be reused unisolated
  * cleanup always runs: the TUI child is killed, the detached
    `golem serve` spawned behind the TUI is killed via the isolated
    pidfile (SIGTERM, then SIGKILL), and the temp dir is removed

Scenario format (stdin or --scenario FILE; one step per line, a line
starting with '#' is a comment):

    sleep <seconds>          # wait for that long, capturing output
    keys <python-bytes-lit>  # send raw bytes, e.g. keys \\x03 (ctrl+c)
    type <text>              # send the rest of the line verbatim

With no scenario (and a tty stdin) the runner boots the TUI, waits for
output to settle, dumps the screen, and quits.

Example - probe the ctrl+c exit ladder:

    printf 'sleep 3\\nkeys \\x03\\nsleep 1\\nkeys \\x03\\nkeys \\x03\\n' \\
        | scripts/tui-drive.py

Fidelity note (pre-existing, same as the ad-hoc scripts this replaces):
under a driven pty the exit ladder absorbs one extra ctrl+c - plan for
three presses when you expect two. The terminal-query responses sent at
boot can also land in the composer as text; the first ctrl+c clears it.
"""

import argparse
import ast
import fcntl
import os
import pty
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time


def repo_root() -> str:
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def build_golem(dest: str) -> None:
    subprocess.run(
        ["go", "build", "-o", dest, "./cmd/golem"],
        cwd=repo_root(),
        check=True,
    )


def parse_keys(value: str) -> bytes:
    try:
        parsed = ast.literal_eval("b'" + value + "'")
    except (ValueError, SyntaxError):
        return value.encode("utf-8")
    return parsed if isinstance(parsed, bytes) else value.encode("utf-8")


def parse_scenario(text: str) -> list:
    steps = []
    for lineno, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split(None, 1)
        op = parts[0]
        arg = parts[1] if len(parts) > 1 else ""
        if op == "sleep":
            try:
                steps.append(("sleep", float(arg)))
            except ValueError:
                raise ValueError(f"line {lineno}: sleep wants seconds, got {arg!r}")
        elif op == "keys":
            if not arg:
                raise ValueError(f"line {lineno}: keys wants a bytes literal")
            steps.append(("keys", parse_keys(arg)))
        elif op == "type":
            if not arg:
                raise ValueError(f"line {lineno}: type wants text")
            steps.append(("keys", arg.encode("utf-8")))
        else:
            raise ValueError(f"line {lineno}: unknown directive {op!r}")
    return steps


def load_scenario(args) -> list:
    if args.scenario:
        with open(args.scenario, encoding="utf-8") as f:
            return parse_scenario(f.read())
    if not sys.stdin.isatty():
        return parse_scenario(sys.stdin.read())
    # No scenario given: boot, let output settle (the final drain does it).
    return []


def capture(fd, captured, seconds) -> None:
    """Capture output for exactly `seconds`, stopping early on EOF."""
    end = time.time() + seconds
    while True:
        remain = end - time.time()
        if remain <= 0:
            return
        ready, _, _ = select.select([fd], [], [], min(remain, 0.1))
        if not ready:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            return
        if not chunk:
            return
        captured.append(chunk)


def drain(fd, captured, quiet, limit) -> None:
    """Capture until `quiet` idle seconds pass, capped at `limit`."""
    end = time.time() + limit
    last = time.time()
    while time.time() < end:
        ready, _, _ = select.select([fd], [], [], 0.1)
        if ready:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            captured.append(chunk)
            last = time.time()
        elif time.time() - last > quiet:
            break


def kill_leaked_serve(run_dir: str) -> None:
    """Stop the detached `golem serve` the drive spawned, via its pidfile."""
    pidfile = os.path.join(run_dir, "golem.sock.pid")
    try:
        with open(pidfile, encoding="utf-8") as f:
            spid = int(f.read().strip())
    except (OSError, ValueError):
        return
    if spid <= 1 or spid == os.getpid():
        return
    try:
        os.kill(spid, signal.SIGTERM)
    except ProcessLookupError:
        return
    deadline = time.time() + 3.0
    while time.time() < deadline:
        try:
            os.kill(spid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.1)
    try:
        os.kill(spid, signal.SIGKILL)
    except ProcessLookupError:
        pass


def child_exit_status(pid: int) -> None:
    try:
        wpid, status = os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        print("=== tui: already reaped", file=sys.stderr)
        return
    if wpid == 0:
        print("=== tui: still alive at scenario end (killed by cleanup)", file=sys.stderr)
    elif os.WIFEXITED(status):
        print(f"=== tui: exited with code {os.WEXITSTATUS(status)}", file=sys.stderr)
    elif os.WIFSIGNALED(status):
        print(f"=== tui: killed by signal {os.WTERMSIG(status)}", file=sys.stderr)


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Drive the golem TUI in an isolated pty for E2E checks.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "scenario directives (stdin or --scenario FILE):\n"
            "  sleep <seconds>          wait, capturing output\n"
            "  keys <python-bytes-lit>  send raw bytes, e.g. keys \\x03\n"
            "  type <text>              send the rest of the line verbatim\n"
        ),
    )
    ap.add_argument("--scenario", help="scenario file (default: stdin when piped)")
    ap.add_argument("--rows", type=int, default=30, help="pty rows (default 30)")
    ap.add_argument("--cols", type=int, default=100, help="pty cols (default 100)")
    ap.add_argument("--quiet", type=float, default=0.8,
                    help="idle seconds that end the final drain (default 0.8)")
    ap.add_argument("--limit", type=float, default=10.0,
                    help="cap in seconds for the final drain (default 10)")
    ap.add_argument("--repr", action="store_true",
                    help="dump captured bytes as python repr instead of decoded text")
    args = ap.parse_args()

    try:
        steps = load_scenario(args)
    except ValueError as e:
        print(f"scripts/tui-drive: {e}", file=sys.stderr)
        return 2

    tmpdir = tempfile.mkdtemp(prefix="golem-drive-")
    data_dir = os.path.join(tmpdir, "data")
    run_dir = os.path.join(tmpdir, "run")
    try:
        os.makedirs(data_dir)
        os.makedirs(run_dir)
        print(f"=== isolated data: {data_dir}", file=sys.stderr)
        print(f"=== isolated run:  {run_dir}", file=sys.stderr)
        print(f"=== serve log:     {os.path.join(data_dir, 'golem', 'serve.log')}",
              file=sys.stderr)
        print(f"=== socket:        {os.path.join(run_dir, 'golem.sock')}", file=sys.stderr)

        exe = os.path.join(tmpdir, "golem")
        try:
            build_golem(exe)
        except subprocess.CalledProcessError as e:
            print(f"scripts/tui-drive: go build failed (exit {e.returncode})",
                  file=sys.stderr)
            return 1

        # Isolation contract: both XDG vars must point inside this run's
        # temp dir. DefaultSocketPath() resolves the socket from
        # XDG_RUNTIME_DIR (falling back to the XDG_DATA_HOME dir), so with
        # both set the drive can never touch the production socket.
        env = dict(os.environ, XDG_DATA_HOME=data_dir, XDG_RUNTIME_DIR=run_dir)
        for var, val in (("XDG_DATA_HOME", data_dir), ("XDG_RUNTIME_DIR", run_dir)):
            if not os.path.abspath(val).startswith(tmpdir + os.sep):
                raise RuntimeError(f"isolation violated: {var}={val} escapes {tmpdir}")

        pid, fd = pty.fork()
        if pid == 0:
            os.execve(exe, [exe], env)
            os._exit(127)  # exec failed

        captured = []
        try:
            fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", args.rows, args.cols, 0, 0))
            time.sleep(0.3)
            # Answer the terminal queries the TUI sends at startup
            # (device attributes, background color, cursor position).
            os.write(fd, b"\x1b[?1;2c")
            os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
            os.write(fd, b"\x1b[1;1R")

            for step in steps:
                if step[0] == "sleep":
                    capture(fd, captured, step[1])
                else:
                    os.write(fd, step[1])
            drain(fd, captured, args.quiet, args.limit)

            child_exit_status(pid)

            data = b"".join(captured)
            print(f"=== captured {len(data)} bytes ===")
            if args.repr:
                print(repr(data))
            else:
                sys.stdout.write(data.decode("utf-8", "replace") + "\n")
            return 0
        finally:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                os.waitpid(pid, 0)
            except ChildProcessError:
                pass
            try:
                os.close(fd)
            except OSError:
                pass
            kill_leaked_serve(run_dir)
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
