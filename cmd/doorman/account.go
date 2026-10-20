package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"callmemaybe/internal/account"
)

func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	state := fs.String("state", "/var/lib/doorman", "state directory (run as the backup service user)")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	result, err := account.New(*state).Login(ctx, func(code string) {
		fmt.Println("Open", account.ApprovalURL)
		fmt.Println("Approve this device with code", code)
		fmt.Println("Choose the house to back up. Waiting for approval…")
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Signed in: device %s, house %s.\n", result.Box, result.House)
	fmt.Println("Credentials saved privately. Enable BACKUP_CLOUD=true in .env to upload encrypted backups.")
	return 0
}

func runAccount(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doorman account status|rotate [--state directory] [--capability backup|sync]")
		return 2
	}
	fs := flag.NewFlagSet("account "+args[0], flag.ContinueOnError)
	state := fs.String("state", "/var/lib/doorman", "state directory")
	asJSON := fs.Bool("json", false, "print IDs and capability names only")
	capability := fs.String("capability", "backup", "token capability to rotate")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	client := account.New(*state)
	switch args[0] {
	case "status":
		status, err := client.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if *asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(status)
		} else if status.NeedsLogin {
			fmt.Println("Account: not signed in; run doorman login.")
		} else {
			fmt.Printf("Account: device %s, house %s; enrolled %s.\n", status.Box, status.House, status.EnrolledAt.Format(time.RFC3339))
			fmt.Printf("Capabilities: %v. Cloud backup upload is write-only.\n", status.Capabilities)
			if status.PendingUpload != "" {
				fmt.Println("Pending upload:", status.PendingUpload)
			}
		}
		if status.NeedsLogin {
			return 1
		}
		return 0
	case "rotate":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := client.Rotate(ctx, *capability); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("Successor saved. Its next use commits rotation; the predecessor will not be reused.")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: doorman account status|rotate")
		return 2
	}
}
