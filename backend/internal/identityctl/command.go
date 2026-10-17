// Package identityctl implements the operator-only recovery command. It is not
// reachable through the HTTP or RPC services.
package identityctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/identity"
)

func canonicalOrigin(raw string) (string, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "http" && origin.Scheme != "https" || origin.Hostname() == "" ||
		origin.User != nil || origin.Opaque != "" || origin.Path != "" || origin.RawQuery != "" ||
		origin.ForceQuery || origin.Fragment != "" || origin.RawFragment != "" ||
		strings.TrimSpace(raw) != raw || origin.Host != strings.ToLower(origin.Host) ||
		strings.ContainsAny(origin.Host, "%\\") || origin.String() != raw {
		return "", errors.New("APP_ORIGIN must be a canonical http(s) origin without a path, query, fragment, or credentials")
	}
	if port := origin.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("APP_ORIGIN has an invalid port")
		}
	}
	return raw, nil
}

// Run emits only the intentionally requested recovery URL and expiry on
// success. Internal errors never echo a database URL, SQL argument or token.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "password-reset-link" {
		fmt.Fprintln(stderr, "Usage: identityctl password-reset-link --email <account>")
		return 2
	}
	options := flag.NewFlagSet("password-reset-link", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	email := options.String("email", "", "Account email address")
	if err := options.Parse(args[1:]); err != nil || options.NArg() != 0 || strings.TrimSpace(*email) == "" {
		fmt.Fprintln(stderr, "Usage: identityctl password-reset-link --email <account>")
		return 2
	}
	origin, err := canonicalOrigin(getenv("APP_ORIGIN"))
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	flags := config.Flags{Path: getenv("FEATURE_FLAGS_FILE")}
	if err = flags.Validate(); err != nil {
		fmt.Fprintln(stderr, "FEATURE_FLAGS_FILE is missing or invalid")
		return 1
	}
	if !flags.Load().Flags["auth.password_reset"] {
		fmt.Fprintln(stderr, "Password reset link generation is disabled")
		return 1
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(stderr, "DATABASE_URL is required")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store, err := identity.Open(ctx, databaseURL, flags, "")
	if err != nil {
		fmt.Fprintln(stderr, "Identity database could not be opened")
		return 1
	}
	defer store.DB.Close()
	reset, token, err := store.OperatorPasswordResetLink(ctx, *email)
	if err != nil {
		var typed *api.Error
		if errors.As(err, &typed) {
			fmt.Fprintln(stderr, typed.Message)
		} else {
			fmt.Fprintln(stderr, "Password reset link could not be generated")
		}
		return 1
	}
	result := struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{URL: origin + "/reset-password#token=" + token, ExpiresAt: reset.ExpiresAt}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "Password reset output could not be written")
		return 1
	}
	return 0
}
