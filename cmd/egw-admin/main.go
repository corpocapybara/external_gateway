package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var (
	workspace = flag.String("workspace", "", "Workspace name")
	name      = flag.String("name", "", "Secret name")
	value     = flag.String("value", "", "Secret value (reads stdin if empty)")
	action    = flag.String("action", "set", "Action: set, delete")
	adminAddr = flag.String("admin", "http://127.0.0.1:8443/admin", "Admin API address")
	token     = flag.String("token", "", "Admin bearer token (required if admin_token_sha256 is configured)")
)

// newAdminRequest builds a request to the admin API, attaching the bearer
// token when one was supplied. The gateway rejects admin calls with 401 when
// admin_token_sha256 is set and no matching token is presented.
func newAdminRequest(method, url string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	return req, nil
}

func main() {
	flag.Parse()

	if *action == "set" {
		if *workspace == "" || *name == "" {
			fmt.Fprintln(os.Stderr, "workspace and name required for set action")
			flag.Usage()
			os.Exit(1)
		}

		val := strings.TrimSpace(*value)
		if val == "" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to read stdin: %v\n", err)
				os.Exit(1)
			}
			val = strings.TrimSpace(string(data))
		}

		if val == "" {
			fmt.Fprintln(os.Stderr, "No value provided (use --value or pipe via stdin)")
			os.Exit(1)
		}

		body, _ := json.Marshal(map[string]string{
			"workspace": *workspace,
			"name":      *name,
			"value":     val,
		})
		req, err := newAdminRequest(http.MethodPost, *adminAddr+"/secrets", body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build request: %v\n", err)
			os.Exit(1)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to set secret: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Failed to set secret (HTTP %d): %s\n", resp.StatusCode, string(respBody))
			os.Exit(1)
		}
		fmt.Printf("Secret %s/%s set successfully\n", *workspace, *name)

	} else if *action == "delete" {
		if *workspace == "" || *name == "" {
			fmt.Fprintln(os.Stderr, "workspace and name required for delete action")
			flag.Usage()
			os.Exit(1)
		}

		body, _ := json.Marshal(map[string]string{
			"workspace": *workspace,
			"name":      *name,
		})
		req, err := newAdminRequest(http.MethodDelete, *adminAddr+"/secrets", body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build request: %v\n", err)
			os.Exit(1)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to delete secret: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Failed to delete secret (HTTP %d): %s\n", resp.StatusCode, string(respBody))
			os.Exit(1)
		}
		fmt.Printf("Secret %s/%s deleted successfully\n", *workspace, *name)

	} else {
		fmt.Fprintf(os.Stderr, "Unknown action: %s (use 'set' or 'delete')\n", *action)
		flag.Usage()
		os.Exit(1)
	}
}
