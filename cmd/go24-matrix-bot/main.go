// Command go24-matrix-bot creates the go24 bot on the Matrix server (a server admin, via the shared registration
// secret) and prints its access token — once, at installation: make matrix-bot writes it into deploy/.env.
package main

import (
	"context"
	"fmt"
	"os"

	"go24/internal/matrix"
)

func main() {
	base, secret := os.Getenv("MATRIX_URL"), os.Getenv("SYNAPSE_REGISTRATION_SECRET")
	if base == "" || secret == "" {
		fmt.Fprintln(os.Stderr, "ошибка: нужны MATRIX_URL и SYNAPSE_REGISTRATION_SECRET")
		os.Exit(1)
	}
	tok, err := matrix.RegisterAdmin(context.Background(), base, secret, "go24", "go24")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	fmt.Println(tok) // only to stdout, which make redirects into deploy/.env
}
