package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func publishLocalProfile(ctx context.Context, home string, local client.EngineOptions, profile, run string) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, wingdclient.RequestTimeout)
	defer cancel()
	state, err := client.ReadLocalPublication(ctx, local, run)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Join(home, "data"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	reader, err := client.NewLogReader(local)
	if err != nil {
		return err
	}
	options, closeConnection, err := controllerOptions(ctx, profile)
	if err != nil {
		return err
	}
	defer closeConnection()
	_, err = client.PublishLocalRun(ctx, options, client.PublicationInput{State: state, Logs: reader, OpenBlob: func(ctx context.Context, digest blob.Digest) (io.ReadCloser, int64, error) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		key, err := blob.ObjectKey(string(state.Run.Team), digest)
		if err != nil {
			return nil, 0, err
		}
		body, err := root.Open(key)
		if err != nil {
			return nil, 0, err
		}
		info, err := body.Stat()
		if err != nil {
			return nil, 0, errors.Join(err, body.Close())
		}
		return body, info.Size(), nil
	}})
	return err
}
