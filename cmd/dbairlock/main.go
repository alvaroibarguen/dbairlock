// Command dbairlock runs the dbairlock server. It parses flags and dispatches
// subcommands; everything else lives in internal/app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/alvaroibarguen/dbairlock/internal/app"
	"github.com/alvaroibarguen/dbairlock/internal/platform/logx"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

const usage = `Usage: dbairlock [command] [flags]

Commands:
  serve          run the server (default)
  audit verify   check the audit log's hash chain
  rotate-key     replace the master key and re-encrypt every secret (stop the server first)
  version        print the version and exit

Run "dbairlock <command> -h" for flags.
`

// Exit codes of "dbairlock audit verify".
const (
	exitVerifyOK     = 0 // the chain is intact
	exitVerifyBroken = 1 // the chain is broken: an event was modified or deleted
	exitUsage        = 2 // bad command line
	exitVerifyError  = 3 // the check could not run (no database, I/O error)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args, stderr)
	case "audit":
		if len(args) == 0 || args[0] != "verify" {
			_, _ = fmt.Fprintf(stderr, "usage: dbairlock audit verify [--data-dir DIR]\n")
			return exitUsage
		}
		return auditVerify(args[1:], stdout, stderr)
	case "rotate-key":
		return rotateKey(args, stdout, stderr)
	case "version":
		if len(args) > 0 {
			_, _ = fmt.Fprintf(stderr, "version takes no arguments\n")
			return 2
		}
		_, _ = fmt.Fprintln(stdout, resolveVersion())
		return 0
	case "help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

type serveFlags struct {
	dataDir string
	listen  string
	dev     bool
}

func parseServeFlags(args []string, stderr io.Writer) (serveFlags, error) {
	var f serveFlags
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.dataDir, "data-dir", "./data", "directory for the database and master key")
	fs.StringVar(&f.listen, "listen", ":8080", "address to listen on")
	fs.BoolVar(&f.dev, "dev", false, "development mode (loopback only, verbose logs)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return f, nil
}

func serve(args []string, stderr io.Writer) int {
	f, err := parseServeFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	logger := logx.New(stderr, f.dev)
	a, err := app.New(app.Options{
		DataDir: f.dataDir,
		Listen:  f.listen,
		Dev:     f.dev,
		Version: resolveVersion(),
		Logger:  logger,
	})
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	defer func() {
		if err := a.Close(); err != nil {
			logger.Error("closing the store failed", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Serve(ctx); err != nil {
		logger.Error("server stopped with error", "error", err)
		return 1
	}
	return 0
}

func auditVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "./data", "directory that holds the database")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "\nExit codes: %d intact, %d broken, %d usage error, %d could not check\n",
				exitVerifyOK, exitVerifyBroken, exitUsage, exitVerifyError)
			return exitVerifyOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := app.VerifyAudit(ctx, *dataDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "audit verify: %v\n", err)
		return exitVerifyError
	}
	if !res.OK() {
		_, _ = fmt.Fprintf(stdout, "audit log BROKEN at seq %d: %s (%d events verified before it)\n",
			res.Problem.Seq, res.Problem.Reason, res.Checked)
		return exitVerifyBroken
	}
	_, _ = fmt.Fprintf(stdout, "audit log intact: %d events verified\n", res.Checked)
	return exitVerifyOK
}

func rotateKey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "./data", "directory that holds the database and master.key")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "\nStop the server before rotating: a running server keeps the old key in memory.\n"+
				"The previous key is kept as master.key.old until the next successful start.\n")
			return 0
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := app.RotateKey(ctx, *dataDir, logx.New(stderr, false))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "rotate-key: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "master key rotated to version %d: %d secrets re-encrypted; the previous key is kept as master.key.old until the next successful start\n",
		res.KeyVersion, res.Secrets)
	return 0
}

// resolveVersion returns the version set at link time, or the module version
// recorded by the Go toolchain, or "dev".
func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
