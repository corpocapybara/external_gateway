package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
)

func main() {
	token := flag.String("token", "", "Token to hash (will prompt if empty)")
	flag.Parse()

	if *token == "" {
		fmt.Print("Enter token to hash: ")
		fmt.Scanln(token)
	}

	if *token == "" {
		fmt.Fprintln(os.Stderr, "Token required")
		os.Exit(1)
	}

	hash := sha256.Sum256([]byte(*token))
	sha := hex.EncodeToString(hash[:])

	fmt.Println("Token SHA256:", sha)
	fmt.Println()
	fmt.Println("Add this to config.yaml under agents[].token_sha256")
}
