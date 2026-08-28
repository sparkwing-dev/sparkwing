package client

import "github.com/sparkwing-dev/sparkwing/pkg/storage"

var _ storage.StateStore = (*Client)(nil)
