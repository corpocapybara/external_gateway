package main

import (
	"fmt"

	"github.com/external_gateway/internal/secrets"
)

func main() {
	for _, name := range []string{"pg-stg", "pg-stg-globex"} {
		secret, err := secrets.GetResolver().Resolve("globex", name)
		if err != nil {
			fmt.Printf("globex/%s: ERROR - %v\n", name, err)
			continue
		}
		fmt.Printf("globex/%s: %d bytes, first 10: %s\n", name, len(secret), string(secret[:min(10, len(secret))]))
	}
}
