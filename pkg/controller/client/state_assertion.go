package client

import "github.com/sparkwing-dev/sparkwing/pkg/storage"

var _ storage.StateStoreOld = (*Client)(nil)
