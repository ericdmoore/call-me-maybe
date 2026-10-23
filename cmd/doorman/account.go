package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/config"
)

func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	state := fs.String("state", "/var/lib/doorman", "state directory (run as the backup service user)")
	envPath := fs.String("env", "/opt/call-me-maybe/.env", "where BOX_NAME is read from when the environment does not set it")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	client := account.New(*state)
	client.Name = suggestedBoxName(*envPath)
	before, _ := client.Status()
	result, err := client.Login(ctx, func(code string) {
		fmt.Println("Open", account.ApprovalURL)
		fmt.Println("Approve this device with code", code)
		fmt.Println("Choose the house to back up. Waiting for approval…")
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Signed in: device %s, house %s.\n", result.Box, result.House)
	forgetRemovedBackup(*state)
	fmt.Println("Credentials saved privately. Enable BACKUP_CLOUD=true in .env to upload encrypted backups.")
	if before.Removed {
		fmt.Println("This box was removed from its old account; hosted sync stopped then. Start it again: sudo systemctl start doorman-sync")
	}
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
		} else if status.Removed {
			fmt.Println(removedLine(status))
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

// suggestedBoxName is the name the box offers its account: BOX_NAME from the
// environment, else from the .env the units read (an interactive login does
// not load it), else the hostname. Only ever sent to the account; never
// printed or logged.
func suggestedBoxName(envPath string) string {
	dotenv := loadDotEnv(envPath)
	return config.BoxName(func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return dotenv[key]
	}, os.Hostname)
}

// removedLine says what happened, that the phone is unaffected, and the one
// command that reconnects the box, without any identifier or credential.
func removedLine(status account.Status) string {
	return "Account: this box was removed from its account on " + status.RemovedAt.Format(time.RFC3339) +
		". Hosted sync and cloud backups have stopped; calls still work here. Run doorman login to connect it to an account again."
}

// describeAccount is check's account line. Without enrolment it says nothing:
// most boxes never sign in, and check must not create account state to ask.
// It never fails the check, because no account state can stop a call.
func describeAccount(stateDir string) string {
	if _, err := os.Stat(filepath.Join(stateDir, "account", "tokens.bin")); err != nil {
		return ""
	}
	status, err := account.New(stateDir).Status()
	switch {
	case err != nil:
		return "Account: state not readable here (" + err.Error() + "); run doorman account status as the doorman user"
	case status.Removed:
		return removedLine(status)
	case status.NeedsLogin:
		return "Account: signed out; run doorman login."
	}
	return fmt.Sprintf("Account: device %s, house %s; capabilities %v.", status.Box, status.House, status.Capabilities)
}
