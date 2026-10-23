package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

const secretConnectTimeout = 10 * time.Second

func runSecret(inv clisurface.Invocation) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing secrets" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	if err := validateSecretInvocation(inv); err != nil {
		return err
	}
	ctx := context.Background()
	connectCtx, cancel := context.WithTimeout(ctx, secretConnectTimeout)
	options, closeTransport, err := controllerOptions(connectCtx, inv.Value("profile"))
	cancel()
	if err != nil {
		return err
	}
	defer closeTransport()
	transport := http.Client{}
	if options.HTTP != nil {
		transport = *options.HTTP
	}
	transport.Timeout = secretOperationTimeout(inv)
	options.HTTP = &transport
	manager, err := client.NewSecretManager(options)
	if err != nil {
		return err
	}
	return manageSecrets(ctx, inv, manager, os.Stdin, os.Stdout, os.Stderr, inv.Value("profile") == "")
}

func validateSecretInvocation(inv clisurface.Invocation) error {
	if inv.Has("secret") {
		if err := secretname.Validate(inv.Value("secret")); err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
	}
	return nil
}

func manageSecrets(ctx context.Context, inv clisurface.Invocation, manager controllerwire.SecretManager, in io.Reader, out, diagnostics io.Writer, local bool) error {
	if err := validateSecretInvocation(inv); err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	selector := controllerwire.SecretSelector{Name: inv.Value("secret"), Pipeline: inv.Value("pipeline")}
	var input controllerwire.SecretWrite
	if inv.Command.Path == "sparkwing secrets set" {
		value, err := readSecretValue(inv, in)
		if err != nil {
			return err
		}
		input = controllerwire.SecretWrite{Name: selector.Name, Pipeline: selector.Pipeline, Value: value, Masked: !inv.Bool("variable"), Shared: inv.Bool("shared") || local && selector.Pipeline == "", OnlyOn: inv.Value("only-on"), IdempotencyKey: rand.Text()}
	}
	ctx, cancel := context.WithTimeout(ctx, secretOperationTimeout(inv))
	defer cancel()
	switch inv.Command.Path {
	case "sparkwing secrets set":
		result, err := manager.WriteSecret(ctx, input)
		if err != nil {
			return err
		}
		if input.Masked && !secrets.Maskable(input.Value) {
			result.Warning = secrets.ShortValueWarning
		}
		if result.Warning != "" {
			if _, err := fmt.Fprintln(diagnostics, "warning: "+result.Warning); err != nil {
				return err
			}
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		if format == "plain" {
			_, err = fmt.Fprintln(out, result.Secret.Name)
			return err
		}
		_, err = fmt.Fprintf(out, "secret %q set (pipeline: %s, masked=%t, only-on: %s)\n", result.Secret.Name, secretScope(result.Secret), result.Secret.Masked, dashIfEmpty(result.Secret.OnlyOn))
		return err
	case "sparkwing secrets get":
		result, err := manager.ReadSecret(ctx, selector)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		_, err = io.WriteString(out, result.Value)
		return err
	case "sparkwing secrets list":
		result, err := manager.ListSecrets(ctx)
		if err != nil {
			return err
		}
		return renderSecrets(out, result.Secrets, inv.Value("search"), format)
	case "sparkwing secrets delete":
		result, err := manager.DeleteSecret(ctx, selector)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		if format == "plain" {
			_, err = fmt.Fprintln(out, result.Name)
			return err
		}
		state := "already absent"
		if result.Deleted {
			state = "deleted"
		}
		_, err = fmt.Fprintf(out, "secret %q %s\n", result.Name, state)
		return err
	case "sparkwing secrets rotate":
		result, err := manager.RotateSecrets(ctx, controllerwire.SecretRotationRequest{IdempotencyKey: rand.Text()})
		if err != nil {
			return err
		}
		return renderSecretRotation(out, result, format)
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown secret command"}
}

func secretOperationTimeout(inv clisurface.Invocation) time.Duration {
	if inv.Command.Path == "sparkwing secrets rotate" {
		return 5 * time.Minute
	}
	return 30 * time.Second
}

func readSecretValue(inv clisurface.Invocation, in io.Reader) (string, error) {
	if inv.Has("value") {
		return inv.Value("value"), nil
	}
	if inv.Has("file") {
		path := inv.Value("file")
		file, err := fssecure.OpenCredential(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		data, readErr := io.ReadAll(file)
		err = errors.Join(readErr, file.Close())
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		return string(data), nil
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read secret value: %w", err)
	}
	return string(data), nil
}

func renderSecrets(out io.Writer, rows []controllerwire.SecretMetadata, search, format string) error {
	selected := make([]controllerwire.SecretMetadata, 0, len(rows))
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Name), strings.ToLower(search)) {
			selected = append(selected, row)
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Name == selected[j].Name {
			return selected[i].Pipeline < selected[j].Pipeline
		}
		return selected[i].Name < selected[j].Name
	})
	if format == "json" || format == "plain" {
		for _, row := range selected {
			if format == "json" {
				if err := json.NewEncoder(out).Encode(row); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(out, row.Name); err != nil {
				return err
			}
		}
		return nil
	}
	if len(selected) == 0 {
		_, err := fmt.Fprintln(out, "(no secrets)")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPIPELINE\tMASKED\tBOUND\tONLY_ON\tPRINCIPAL\tCREATED\tUPDATED")
	for _, row := range selected {
		fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%s\t%s\t%s\t%s\n", row.Name, secretScope(row), row.Masked, row.Bound, dashIfEmpty(row.OnlyOn), row.Principal, secretTimestamp(row.CreatedAt), secretTimestamp(row.UpdatedAt))
	}
	return tw.Flush()
}

func secretScope(secret controllerwire.SecretMetadata) string {
	if secret.Pipeline != "" {
		return secret.Pipeline
	}
	if secret.Shared {
		return "every pipeline"
	}
	return "admin only"
}

func secretTimestamp(seconds int64) string {
	if seconds == 0 {
		return "-"
	}
	return time.Unix(seconds, 0).UTC().Format("2006-01-02 15:04")
}

func renderSecretRotation(out io.Writer, result controllerwire.SecretRotation, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if format == "plain" {
		if _, err := fmt.Fprintf(out, "rotated\t%d\n", result.Rotated); err != nil {
			return err
		}
		for _, row := range result.Skipped {
			if _, err := fmt.Fprintf(out, "skipped\t%s\t%s\n", row.Name, row.Pipeline); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%d secret(s) re-encrypted under the current key\n", result.Rotated)
	if len(result.Skipped) == 0 {
		fmt.Fprintln(tw, "the previous key can now be dropped")
	} else {
		fmt.Fprintln(tw, "The previous key is still needed for skipped rows:")
		for _, row := range result.Skipped {
			fmt.Fprintf(tw, "%s\t%s\n", row.Name, dashIfEmpty(row.Pipeline))
		}
	}
	return tw.Flush()
}
