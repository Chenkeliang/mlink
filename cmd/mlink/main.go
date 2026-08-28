package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"mlink/internal/provider/server"
	"mlink/internal/provider/tencentdb"
)

func main() {
	err := dispatch(os.Args[1:], func() error {
		return server.New(tencentdb.NewServerHandler()).Serve(context.Background(), os.Stdin, os.Stdout)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mlink provider command failed")
		os.Exit(1)
	}
}

func dispatch(args []string, serveTencentDB func() error) error {
	if !slices.Equal(args, []string{"provider", "run", "tencentdb"}) {
		return errors.New("unsupported MLink command")
	}
	if serveTencentDB == nil {
		return errors.New("TencentDB Provider Server is unavailable")
	}
	return serveTencentDB()
}
