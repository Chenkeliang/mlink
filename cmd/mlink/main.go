package main

import (
	"context"
	"io"
	"os"

	"mlink/internal/cli"
	"mlink/internal/provider/server"
	"mlink/internal/provider/tencentdb"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], runDependencies(os.Stdout, os.Stderr, func(ctx context.Context) error {
		return server.New(tencentdb.NewServerHandler()).Serve(ctx, os.Stdin, os.Stdout)
	})))
}

func runDependencies(stdout, stderr io.Writer, serveTencentDB func(context.Context) error) cli.Dependencies {
	return cli.Dependencies{
		Stdout:         stdout,
		Stderr:         stderr,
		ServeTencentDB: serveTencentDB,
	}
}
