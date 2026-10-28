package main

import (
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"

	"crypto/x509"
)

// Subcommands: `roadie token` and `roadie tls status`. Anything starting
// with a dash is the server's own flags.
func runCommand(name string, args []string) int {
	switch name {
	case "token":
		return tokenCommand(args)
	case "tls":
		return tlsCommand(args)
	case "enroll":
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

// parseFirstPEMCert reads the first certificate in a PEM file.
func parseFirstPEMCert(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no certificate found")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}
