package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/external_gateway/internal/secrets"
)

func main() {
	secret, err := secrets.GetResolver().Resolve("globex", "bitbucket")
	if err != nil {
		fmt.Printf("ERROR reading secret: %v\n", err)
		return
	}
	fmt.Printf("Secret length: %d bytes\n", len(secret))
	fmt.Printf("First 20 chars: %s\n", string(secret[:min(20, len(secret))]))

	req, _ := http.NewRequest("GET", "https://api.bitbucket.org/2.0/user", nil)
	req.SetBasicAuth("you@example.com", string(secret))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %d\n", resp.StatusCode)
	fmt.Printf("Body: %s\n", string(respBody[:min(200, len(respBody))]))
}
