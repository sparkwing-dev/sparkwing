//go:build qatrust

package releaseauth

// QAPublicKey verifies QA pre-releases from https://qa-releases.sparkwing.dev.
// Its signing seed is the SSM SecureString that bin/qa-release.sh reads.
// A production build never carries the qatrust tag.
const QAPublicKey = "S37ijVuRXJjKzLlFAkPYR8YFXmayaiH21BHzbndcPFU="

func qaPublicKeys() []string { return []string{QAPublicKey} }
