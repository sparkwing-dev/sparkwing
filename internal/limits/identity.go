package limits

import "time"

const (
	// maxInvitationsPerDay bounds invitation issuance per team.
	maxInvitationsPerDay int = 100
	// maxOpenInvitations bounds outstanding invitations per team.
	maxOpenInvitations int = 50
	// maxGitHubRunnerBindings bounds GitHub runner bindings per team.
	maxGitHubRunnerBindings int = 20
	// maxGitHubRunnerCredentials bounds active GitHub runner credentials.
	maxGitHubRunnerCredentials int = 20
	// invitationTTL bounds invitation lifetime.
	invitationTTL time.Duration = 7 * 24 * time.Hour
	// GitHubRunnerCredentialTTL bounds GitHub runner credential lifetime.
	GitHubRunnerCredentialTTL time.Duration = time.Hour
)
