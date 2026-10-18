//go:build qatrust

package releaseauth

// QAPublicKey verifies QA pre-releases from https://qa-releases.sparkwing.dev.
// Its signing seed is SSM SecureString /kikd-prod/sparkwing/qa-release-signing-key.
// A production build never carries the qatrust tag.
const QAPublicKey = "S37ijVuRXJjKzLlFAkPYR8YFXmayaiH21BHzbndcPFU="

var qaPublicKeys = []string{QAPublicKey}
