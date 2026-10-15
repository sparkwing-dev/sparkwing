package client

import "github.com/sparkwing-dev/sparkwing/internal/blob"

// NewDataClient returns the shared authenticated signing and record transports.
// It uses EngineOptions' HTTP/api.sock transport and private bearer provider.
// Authority and upload identity in interface arguments are selectors to compare;
// the server derives both from current authentication and persisted state.
// Object transfer clients receive signed headers only, never the controller bearer.
func NewDataClient(options EngineOptions) (blob.DataSigner, blob.RecordStore, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, nil, err
	}
	data := &dataClient{transport: c}
	return data, data, nil
}
