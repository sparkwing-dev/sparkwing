package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/term"
)

func runUsers(args []string) error {
	if handleParentHelp(cmdUsers, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdUsers, os.Stderr)
		return fmt.Errorf("users: subcommand required (add|list|delete)")
	}
	switch args[0] {
	case "add":
		return runUsersAdd(args[1:])
	case "list":
		return runUsersList(args[1:])
	case "delete":
		return runUsersDelete(args[1:])
	default:
		PrintHelp(cmdUsers, os.Stderr)
		return fmt.Errorf("users: unknown subcommand %q", args[0])
	}
}

func runUsersAdd(args []string) error {
	fs := flag.NewFlagSet(cmdUsersAdd.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	name := fs.String("name", "", "dashboard username")
	passwordFlag := fs.String("password", "", "password (empty prompts on stdin)")
	scopes := fs.String("scope", "", "comma-separated scopes (empty grants admin)")
	if err := parseAndCheck(cmdUsersAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "users add"); err != nil {
		return err
	}
	password := *passwordFlag
	if password == "" {
		fmt.Fprintf(os.Stderr, "password for %q: ", *name)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			buf, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			fmt.Fprintln(os.Stderr)
			password = string(buf)
		} else {
			var line string
			if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			password = strings.TrimRight(line, "\r\n")
		}
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	body := map[string]any{
		"name":     *name,
		"password": password,
	}
	if requested := splitCSV(*scopes); len(requested) > 0 {
		body["scopes"] = requested
	}
	if _, err := tokensPost(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/users", body); err != nil {
		return err
	}
	fmt.Printf("created user %q\n", *name)
	return nil
}

func runUsersList(args []string) error {
	fs := flag.NewFlagSet(cmdUsersList.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdUsersList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "users list"); err != nil {
		return err
	}
	resp, err := tokensGet(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/users")
	if err != nil {
		return err
	}
	var out struct {
		Users []struct {
			Name        string   `json:"name"`
			Scopes      []string `json:"scopes"`
			CreatedAt   int64    `json:"created_at"`
			LastLoginAt *int64   `json:"last_login_at"`
		} `json:"users"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return err
	}
	if len(out.Users) == 0 {
		fmt.Println("(no users)")
		return nil
	}
	fmt.Printf("%-20s %-16s %-20s %s\n", "NAME", "SCOPES", "CREATED", "LAST_LOGIN")
	for _, u := range out.Users {
		lastLogin := "never"
		if u.LastLoginAt != nil {
			lastLogin = fmt.Sprintf("%d", *u.LastLoginAt)
		}
		fmt.Printf("%-20s %-16s %-20d %s\n",
			u.Name, formatScopes(u.Scopes), u.CreatedAt, lastLogin)
	}
	return nil
}

func runUsersDelete(args []string) error {
	fs := flag.NewFlagSet(cmdUsersDelete.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	name := fs.String("name", "", "dashboard username to remove")
	if err := parseAndCheck(cmdUsersDelete, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "users delete"); err != nil {
		return err
	}
	if _, err := tokensDelete(prof.ControllerURL(), prof.ControllerToken(), "/api/v1/users/"+*name); err != nil {
		return err
	}
	fmt.Printf("deleted user %q\n", *name)
	return nil
}
