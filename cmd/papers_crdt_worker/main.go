//go:build linux

// This private worker only accepts a root-created inherited Unix listener.
// It has no database connection, web routes, credentials or mutable storage.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"atoman/internal/platform/paperscrdt"
)

func run() error {
	var node, script, account string
	flag.StringVar(&node, "node", "/usr/bin/node", "trusted absolute Node executable")
	flag.StringVar(&script, "runtime", "/opt/atoman-papers-crdt/runtime/cli.mjs", "trusted absolute runtime script")
	flag.StringVar(&account, "client-user", "atoman", "authorized local backend account")
	flag.Parse()
	if flag.NArg() != 0 || os.Geteuid() == 0 || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
		return paperscrdt.ErrUnavailable
	}
	u, err := user.Lookup(account)
	if err != nil {
		return paperscrdt.ErrUnavailable
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil || uid == 0 || uint32(os.Geteuid()) == uint32(uid) {
		return paperscrdt.ErrUnavailable
	}
	f := os.NewFile(3, "systemd-unix-listener")
	if f == nil {
		return paperscrdt.ErrUnavailable
	}
	listener, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return paperscrdt.ErrUnavailable
	}
	defer listener.Close()
	unix, ok := listener.(*net.UnixListener)
	if !ok || unix.Addr().Network() != "unix" {
		return paperscrdt.ErrUnavailable
	}
	// The socket belongs to systemd, never unlink it on worker shutdown.
	unix.SetUnlinkOnClose(false)
	engine, err := paperscrdt.New(node, script)
	if err != nil {
		return err
	}
	server, err := paperscrdt.NewSocketServer(engine, uint32(uid))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return server.Serve(ctx, unix)
}
func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "Papers CRDT worker startup or execution failed")
		os.Exit(1)
	}
}
