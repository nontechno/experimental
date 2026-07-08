// slack-check is a small diagnostic tool that verifies:
//  1. Network reachability to slack.com (TCP + TLS)
//  2. That a Slack API token is valid and authenticates (auth.test)
//
// Usage:
//
//	export SLACK_TOKEN=xoxb-your-token-here
//	go run main.go
//
// or build a binary:
//
//	go build -o slack-check
//	./slack-check
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	slackHost    = "slack.com:443"
	slackAuthURL = "https://slack.com/api/auth.test"
)

type authTestResponse struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url"`
	Team    string `json:"team"`
	User    string `json:"user"`
	TeamID  string `json:"team_id"`
	UserID  string `json:"user_id"`
	BotID   string `json:"bot_id"`
	Error   string `json:"error"`
	Warning string `json:"warning"`
}

func main() {
	timeout := flag.Duration("timeout", 10*time.Second, "timeout for each check")
	tokenFlag := flag.String("token", "", "Slack token (overrides SLACK_TOKEN env var)")
	flag.Parse()

	fmt.Println("== Slack connectivity check ==")
	if err := checkReachability(*timeout); err != nil {
		fmt.Printf("[FAIL] network reachability: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[ OK ] TCP+TLS connection to slack.com:443 established")

	token := *tokenFlag
	if token == "" {
		token = os.Getenv("SLACK_TOKEN")
	}
	if token == "" {
		fmt.Println("[SKIP] no token provided (set SLACK_TOKEN or -token) — reachability only")
		return
	}

	fmt.Println("\n== Slack auth check (auth.test) ==")
	resp, err := checkAuth(*timeout, token)
	if err != nil {
		fmt.Printf("[FAIL] auth.test request: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Printf("[FAIL] auth.test returned error: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Println("[ OK ] token is valid")
	fmt.Printf("       team:    %s (%s)\n", resp.Team, resp.TeamID)
	fmt.Printf("       user:    %s (%s)\n", resp.User, resp.UserID)
	if resp.BotID != "" {
		fmt.Printf("       bot_id:  %s\n", resp.BotID)
	}
	if resp.Warning != "" {
		fmt.Printf("       warning: %s\n", resp.Warning)
	}
}

// checkReachability opens a raw TLS connection to slack.com to confirm
// the network path (DNS, TCP, TLS handshake) works, independent of any API call.
func checkReachability(timeout time.Duration) error {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", slackHost, &tls.Config{ServerName: "slack.com"})
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

// checkAuth calls Slack's auth.test endpoint, which is the standard way
// to verify a token is valid and see what identity/workspace it belongs to.
func checkAuth(timeout time.Duration, token string) (*authTestResponse, error) {
	client := &http.Client{Timeout: timeout}

	req, err := http.NewRequest(http.MethodPost, slackAuthURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed authTestResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("unexpected response body: %s", body)
	}
	return &parsed, nil
}
