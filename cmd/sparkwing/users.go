// `sparkwing users` subcommand (FOLLOWUPS #2 phase 2). Seeds admin
// credentials in the controller's users table, used by the web pod's
// login flow. v1 ships a single-admin model; multi-user support is a
// later session.
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
		// term.ReadPassword disables terminal echo so the password
		// doesn't appear on-screen or land in shell history via copy-
		// paste. Only works when stdin is a TTY; when stdin is a pipe
		// (CI feeding a password in), fall back to a raw read.
		fmt.Fprintf(os.Stderr, "password for %q: ", *name)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			buf, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			fmt.Fprintln(os.Stderr) // line break after the hidden entry
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
	body := map[string]string{
		"name":     *name,
		"password": password,
	}
	if _, err := tokensPost(prof.Controller, prof.Token, "/api/v1/users", body); err != nil {
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
	resp, err := tokensGet(prof.Controller, prof.Token, "/api/v1/users")
	if err != nil {
		return err
	}
	var out struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return err
	}
	if len(out.Users) == 0 {
		fmt.Println("(no users)")
		return nil
	}
	fmt.Printf("%-20s %-20s %s\n", "NAME", "CREATED", "LAST_LOGIN")
	for _, u := range out.Users {
		fmt.Printf("%-20s %-20v %v\n", u["name"], u["created_at"], u["last_login_at"])
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
	if _, err := tokensDelete(prof.Controller, prof.Token, "/api/v1/users/"+*name); err != nil {
		return err
	}
	fmt.Printf("deleted user %q\n", *name)
	return nil
}
