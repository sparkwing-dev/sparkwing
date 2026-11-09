package nodehost

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type protocolAdapter struct {
	request func([]byte) ([]byte, error)
	frame   func([]byte) ([]byte, error)
}

// Older shapes are upgraded before the current codec and host policy see them.
// Remove an adapter when the signed mapping has no SDK using its version.
var protocolAdapters = map[int]protocolAdapter{
	nodeproto.Version: {},
}

func (s *protocolSession) upgradeProtocolFrame(line []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	adapter := s.adapter
	if s.sdkProtocol == 0 {
		var envelope struct {
			ID     string `json:"reply"`
			OK     bool   `json:"ok"`
			Result struct {
				Protocol int `json:"protocol"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return nil, err
		}
		call, pending := s.pending[envelope.ID]
		if pending && call.request.Op == nodeproto.OpHello && envelope.OK {
			var supported bool
			adapter, supported = protocolAdapters[envelope.Result.Protocol]
			if !supported {
				return nil, protocolSessionError(apierr.ProtocolMismatch, "unsupported SDK protocol; upgrade the SDK pin and rebuild .sparkwing")
			}
			s.sdkProtocol, s.adapter = envelope.Result.Protocol, adapter
		}
	}
	if adapter.frame != nil {
		return adapter.frame(line)
	}
	return line, nil
}

func (s *protocolSession) encodeProtocolRequest(request nodeproto.Request) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	adapter := s.adapter
	s.mu.Unlock()
	if adapter.request != nil {
		return adapter.request(data)
	}
	return data, nil
}
