package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

var (
	serverAddr = flag.String("server", "https://127.0.0.1:8443", "Gateway server address")
	token      = flag.String("token", "", "Agent bearer token")
	workspace  = flag.String("workspace", "", "Workspace name")
	operation  = flag.String("op", "", "Operation to execute")
	params     = flag.String("params", "{}", "JSON params for the operation")
	cmd        string
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd = os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	flag.Parse()

	switch cmd {
	case "healthz":
		doHealthz()
	case "whoami":
		doWhoAmI()
	case "capabilities":
		doCapabilities()
	case "exec", "run", "invoke":
		invokeOp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: egwctl <command> [flags]\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  healthz                           Check gateway health\n")
	fmt.Fprintf(os.Stderr, "  whoami                            Show agent identity\n")
	fmt.Fprintf(os.Stderr, "  capabilities                      Show available operations\n")
	fmt.Fprintf(os.Stderr, "  exec|run|invoke                   Execute an operation\n")
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	fmt.Fprintf(os.Stderr, "  -server <addr>    Gateway address (default: https://127.0.0.1:8443)\n")
	fmt.Fprintf(os.Stderr, "  -token <val>      Agent bearer token\n")
	fmt.Fprintf(os.Stderr, "  -workspace <name> Workspace name\n")
	fmt.Fprintf(os.Stderr, "  -op <id>          Operation ID to execute\n")
	fmt.Fprintf(os.Stderr, "  -params <json>    JSON params (default: {})\n")
}

func doHealthz() {
	req, err := http.NewRequest("GET", *serverAddr+"/healthz", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

func doWhoAmI() {
	req, err := http.NewRequest("GET", *serverAddr+"/whoami", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		os.Exit(1)
	}

	if *token == "" {
		fmt.Fprintln(os.Stderr, "token is required for whoami")
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+*token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

func doCapabilities() {
	if *workspace == "" {
		fmt.Fprintln(os.Stderr, "workspace is required for capabilities")
		os.Exit(1)
	}

	url := fmt.Sprintf("%s/capabilities?workspace=%s", *serverAddr, *workspace)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		os.Exit(1)
	}

	if *token == "" {
		fmt.Fprintln(os.Stderr, "token is required for capabilities")
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+*token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}

func invokeOp() {
	url := fmt.Sprintf("%s/v1/workspaces/%s/op/%s", *serverAddr, *workspace, *operation)
	httpToken := *token

	var paramsJSON []byte
	if *params != "{}" {
		paramsJSON = []byte(*params)
	} else {
		paramsJSON = []byte("{}")
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(paramsJSON))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Authorization", "Bearer "+httpToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to execute request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "Request failed with status %d: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	fmt.Println(string(body))
}
