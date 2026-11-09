package client

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// DeviceLogin drives one device sign-in. It carries no bearer: the device code is its only proof.
type DeviceLogin struct{ client *engineClient }

// NewDeviceLogin refuses a bearer so a saved credential never travels with a sign-in request.
func NewDeviceLogin(options EngineOptions) (*DeviceLogin, error) {
	if options.Bearer != nil {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "device sign-in sends no bearer"}
	}
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	return &DeviceLogin{client: c}, nil
}

// Start asks the controller for a device code and the user code to type in the browser.
func (d *DeviceLogin) Start(ctx context.Context, request controllerwire.StartDeviceLoginRequest) (controllerwire.DeviceLoginStarted, error) {
	var out controllerwire.DeviceLoginStarted
	err := d.client.request(ctx, "POST", "/api/v1/auth/device", "/api/v1/auth/device", "", request, &out)
	return out, err
}

// Cancel withdraws the request, so a code shown on screen stops working once the command exits.
func (d *DeviceLogin) Cancel(ctx context.Context, deviceCode string) error {
	return d.client.request(ctx, "POST", "/api/v1/auth/device/cancel", "/api/v1/auth/device/cancel", "", controllerwire.DeviceCodeRequest{DeviceCode: deviceCode}, nil)
}

// Wait polls at the controller's interval, slowing down when told to, until the request is approved or refused.
func (d *DeviceLogin) Wait(ctx context.Context, started controllerwire.DeviceLoginStarted, sleep func(context.Context, time.Duration) error) (controllerwire.CreatedToken, error) {
	interval := time.Duration(max(started.Interval, 1)) * time.Second
	for {
		if err := sleep(ctx, interval); err != nil {
			return controllerwire.CreatedToken{}, err
		}
		var poll controllerwire.DeviceLoginPoll
		if err := d.client.request(ctx, "POST", "/api/v1/auth/device/token", "/api/v1/auth/device/token", "", controllerwire.DeviceCodeRequest{DeviceCode: started.DeviceCode}, &poll); err != nil {
			return controllerwire.CreatedToken{}, err
		}
		if poll.Interval > 0 {
			interval = time.Duration(poll.Interval) * time.Second
		}
		switch poll.Status {
		case controllerwire.DevicePending, controllerwire.DeviceSlowDown:
		case controllerwire.DeviceApproved:
			if poll.Credential == nil || poll.Credential.Capability == "" {
				return controllerwire.CreatedToken{}, &apierr.Error{Code: apierr.ProtocolError, Message: "the controller approved the sign-in but returned no credential"}
			}
			return *poll.Credential, nil
		default:
			return controllerwire.CreatedToken{}, &apierr.Error{Code: apierr.ProtocolError, Message: "the controller returned an unknown sign-in status " + poll.Status}
		}
	}
}

// SleepContext waits for d or until ctx ends.
func SleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
