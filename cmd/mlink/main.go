package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"mlink/internal/cli"
	"mlink/internal/provider/server"
	"mlink/internal/provider/tencentdb"
)

func main() {
	dependencies, err := defaultDependencies(os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "mlink startup failed")
		os.Exit(1)
	}
	dependencies.ServeTencentDB = func(ctx context.Context) error {
		return server.New(tencentdb.NewServerHandler()).Serve(ctx, os.Stdin, os.Stdout)
	}
	os.Exit(cli.Run(context.Background(), os.Args[1:], dependencies))
}

func runDependencies(stdout, stderr io.Writer, serveTencentDB func(context.Context) error) cli.Dependencies {
	return cli.Dependencies{
		Stdin:          os.Stdin,
		Stdout:         stdout,
		Stderr:         stderr,
		ServeTencentDB: serveTencentDB,
	}
}
