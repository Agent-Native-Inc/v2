// sign writes <file>.sig: the raw ed25519 signature of file, made with the base64 private key in keyfile.
// Usage: go run ./scripts/sign <keyfile> <file>
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fail(errors.New("usage: sign <keyfile> <file>"))
	}
	k, err := os.ReadFile(os.Args[1])
	fail(err)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(k)))
	fail(err)
	if len(key) != ed25519.PrivateKeySize {
		fail(fmt.Errorf("%s isn't a base64 ed25519 private key", os.Args[1]))
	}
	msg, err := os.ReadFile(os.Args[2])
	fail(err)
	fail(os.WriteFile(os.Args[2]+".sig", ed25519.Sign(key, msg), 0o644))
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
