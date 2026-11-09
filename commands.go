package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

// Subcommands: `roadie token` and `roadie tls status`. Anything starting
// with a dash is the server's own flags.
func runCommand(name string, args []string) int {
	switch name {
	case "token":
		if code := serviceAccountHint(name); code != 0 {
			return code
		}
		return tokenCommand(args)
	case "tls":
		return tlsCommand(args)
	case "enroll":
		if code := serviceAccountHint(name); code != 0 {
			return code
		}
		return enrollCommand(args)
	case "help":
		fmt.Println("usage: roadie [flags]            run the server (roadie -h for the flags)\n" +
			"       roadie token [--rotate]  show the local admin token (--auth token)\n" +
			"       roadie tls status --tls-cert FILE [--tls-key FILE]\n" +
			"       roadie enroll [URL] --code CODE   join a Vibium Station (roadie enroll -h)")
		return 0
	}
	fmt.Fprintf(os.Stderr, "roadie: unknown command %q (try roadie help)\n", name)
	return 2
}

// serviceAccountHint stops a command that reads or writes the config
// directory when roadie.service runs as another account, since the
// token or enrollment would land in the wrong home: it says which
// account to run as instead. Without systemd, or a service, it is quiet.
func serviceAccountHint(cmd string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "roadie.service", "-p", "User", "--value").Output()
	if err != nil {
		return 0
	}
	svc := strings.TrimSpace(string(out))
	me, uerr := user.Current()
	if svc == "" || uerr != nil || svc == me.Username {
		return 0
	}
	fmt.Fprintf(os.Stderr, "roadie %s: roadie.service runs as %s, and this would use %s's files instead; run:\n  sudo -u %s roadie %s ...\n", cmd, svc, me.Username, svc, cmd)
	return 1
}

// tokenCommand prints the local admin token, making one on first use, or
// replaces it with --rotate.
func tokenCommand(args []string) int {
	fs := flag.NewFlagSet("roadie token", flag.ContinueOnError)
	rotate := fs.Bool("rotate", false, "replace the token with a new one; everyone signs in again")
	showPath := fs.Bool("path", false, "print where the token file is")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := tokenPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *showPath {
		fmt.Println(path)
		return 0
	}
	var token string
	if *rotate {
		token, err = writeNewToken(path)
	} else {
		token, err = loadOrCreateToken(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(token)
	return 0
}

// tlsCommand: `roadie tls status` shows the certificate's names and expiry.
func tlsCommand(args []string) int {
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(os.Stderr, "usage: roadie tls status --tls-cert FILE [--tls-key FILE]")
		return 2
	}
	fs := flag.NewFlagSet("roadie tls status", flag.ContinueOnError)
	cert := fs.String("tls-cert", "", "certificate file (PEM)")
	key := fs.String("tls-key", "", "private key file (PEM), to check it matches")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *cert == "" {
		fmt.Fprintln(os.Stderr, "roadie tls status: --tls-cert is required")
		return 2
	}
	if err := tlsStatus(os.Stdout, *cert, *key); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
