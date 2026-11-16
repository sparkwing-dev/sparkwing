package clisurface

// safety: an entry only names the verb in a did-you-mean and never runs it,
// so it is not an alias for the old spelling.
var verbSynonyms = map[string]string{
	"remove": "delete",
}
