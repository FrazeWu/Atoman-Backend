//go:build ignore

// Run a local reviewed operator binary with protected dotenv files, without a
// shell and without printing the environment.
package main

import (
	"fmt"
	"github.com/joho/godotenv"
	"os"
	"os/exec"
)

func main() {
	if os.Geteuid() != 0 || len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Explicit root operator command required")
		os.Exit(1)
	}
	for _, p := range []string{"/etc/atoman/backend.env", "/etc/atoman-mail/backend.env"} {
		i, e := os.Lstat(p)
		if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
			fmt.Fprintln(os.Stderr, "Protected environment unavailable")
			os.Exit(1)
		}
		values, e := godotenv.Read(p)
		if e != nil {
			fmt.Fprintln(os.Stderr, "Environment parse failed")
			os.Exit(1)
		}
		for k, v := range values {
			if os.Setenv(k, v) != nil {
				os.Exit(1)
			}
		}
	}
	args := os.Args[1:]
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(1)
	}
	c := exec.Command(args[0], args[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if e := c.Run(); e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			os.Exit(x.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Operator command could not start")
		os.Exit(1)
	}
}
