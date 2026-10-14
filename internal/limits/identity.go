package limits

import "time"

const (
	// MaxInvitationsPerDay bounds invitation issuance per team.
	MaxInvitationsPerDay int = 100
	// MaxOpenInvitations bounds outstanding invitations per team.
	MaxOpenInvitations int = 50
	// MaxSecrets bounds secrets per team.
	MaxSecrets int = 100
	// MaxTokensPerTeam bounds tokens per team.
	MaxTokensPerTeam int = 100
	// MaxTokensPerMember bounds tokens per member.
	MaxTokensPerMember int = 10
	// MaxTeamsPerUser bounds teams per user.
	MaxTeamsPerUser int = 3
	// MaxGitHubRunnerBindings bounds GitHub runner bindings per team.
	MaxGitHubRunnerBindings int = 20
	// MaxGitHubRunnerCredentials bounds active GitHub runner credentials.
	MaxGitHubRunnerCredentials int = 20
	// SecretBytes bounds individual secret.
	SecretBytes int64 = 128 << 10
	// RootCapabilityTTL bounds default idle lifetime of runner and CLI root capabilities.
	RootCapabilityTTL time.Duration = 90 * 24 * time.Hour
	// InvitationTTL bounds invitation lifetime.
	InvitationTTL time.Duration = 7 * 24 * time.Hour
	// SessionIdleTTL bounds session idle lifetime.
	SessionIdleTTL time.Duration = 7 * 24 * time.Hour
	// SessionMaxTTL bounds session maximum lifetime.
	SessionMaxTTL time.Duration = 30 * 24 * time.Hour
	// GitHubRunnerCredentialTTL bounds GitHub runner credential lifetime.
	GitHubRunnerCredentialTTL time.Duration = time.Hour
)
