package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/external_gateway/internal/secrets"
)

func main() {
	// Read Jira token from WinCred
	secret, err := secrets.GetResolver().Resolve("globex", "jira")
	if err != nil {
		fmt.Printf("ERROR reading secret: %v\n", err)
		return
	}
	fmt.Printf("Secret length: %d bytes\n", len(secret))
	fmt.Printf("First 5 chars: %s\n", secret[:min(5, len(secret))])

	body := `{"jql":"project = MD","maxResults":5,"fields":["summary","status"]}`

	req, _ := http.NewRequest("POST", "https://your-globex-tenant.atlassian.net/rest/api/3/search/jql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
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
	fmt.Printf("Body (first 500): %s\n", string(respBody[:min(500, len(respBody))]))
}
