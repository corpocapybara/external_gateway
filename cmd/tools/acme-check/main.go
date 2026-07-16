package main

import (
	"fmt"

	"github.com/external_gateway/internal/secrets"
)

func main() {
	for _, name := range []string{"jira", "corpo-jira", "gitlab", "pg-dev", "pg-stg", "pg-prod"} {
		secret, err := secrets.GetResolver().Resolve("acme", name)
		if err != nil {
			fmt.Printf("acme/%s: NOT FOUND\n", name)
			continue
		}
		fmt.Printf("acme/%s: %d bytes\n", name, len(secret))
	}
}
