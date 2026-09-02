// Package authwire holds the auth values the controller publishes on the
// wire and other services compare against, so both ends move together.
package authwire

const (
	// AnonymousPrincipal is the principal name the controller's
	// /api/v1/auth/whoami answers when no token authenticated the request.
	AnonymousPrincipal = "unauthed"

	// AnonymousKind is the principal kind that answer carries. A service
	// resolving caller tokens against the controller must read this pair as
	// "not a caller", never as a successful authentication.
	AnonymousKind = "none"
)
