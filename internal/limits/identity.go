package limits

import "time"

const (
	// maxInvitationsPerDay bounds invitation issuance per team.
	maxInvitationsPerDay int = 100
	// maxOpenInvitations bounds outstanding invitations per team.
	maxOpenInvitations int = 50
	// maxSecrets bounds secrets per team.
	maxSecrets int = 100
	// maxTokensPerTeam bounds tokens per team.
	maxTokensPerTeam int = 100
	// maxTokensPerMember bounds tokens per member.
	maxTokensPerMember int = 10
	// maxTeamsPerUser bounds teams per user.
	maxTeamsPerUser int = 3
	// maxGitHubRunnerBindings bounds GitHub runner bindings per team.
	maxGitHubRunnerBindings int = 20
	// maxGitHubRunnerCredentials bounds active GitHub runner credentials.
	maxGitHubRunnerCredentials int = 20
	// secretBytes bounds individual secret.
	secretBytes int64 = 128 << 10
	// invitationTTL bounds invitation lifetime.
	invitationTTL time.Duration = 7 * 24 * time.Hour
	// sessionIdleTTL bounds session idle lifetime.
	sessionIdleTTL time.Duration = 7 * 24 * time.Hour
	// sessionMaxTTL bounds session maximum lifetime.
	sessionMaxTTL time.Duration = 30 * 24 * time.Hour
	// GitHubRunnerCredentialTTL bounds GitHub runner credential lifetime.
	GitHubRunnerCredentialTTL time.Duration = time.Hour
)
