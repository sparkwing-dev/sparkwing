package sdkserve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// Helper sends a typed operation to the bound host.
func Helper(ctx context.Context, operation string, args []any, result any) error {
	RequireExecution(ctx, operation)
	service, ok := ServicesFrom(ctx).(interface {
		Helper(context.Context, nodeproto.HelperRequest, any) error
	})
	if !ok {
		return fmt.Errorf("host helper service unavailable")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	raw, err := EncodeServiceRequest(args)
	if err != nil {
		return err
	}
	return service.Helper(ctx, nodeproto.HelperRequest{Operation: operation, Dir: dir, Env: os.Environ(), Args: raw}, result)
}

func (s *routeServices) Helper(ctx context.Context, request nodeproto.HelperRequest, out any) error {
	var response nodeproto.HelperResponse
	if err := s.call(ctx, "POST", "/node/v1/helpers", request, &response); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(response.Result, out); err != nil {
			return err
		}
	}
	if response.Error != "" {
		return &HelperError{Code: response.Code, Message: response.Error}
	}
	return nil
}

// HelperError preserves host error classes across the transport.
type HelperError struct{ Code, Message string }

func (e *HelperError) Error() string        { return e.Message }
func (e *HelperError) Is(target error) bool { return e.Code != "" && e.Code == target.Error() }
