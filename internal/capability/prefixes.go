package capability

// CredentialPrefixes lists every bearer prefix this product issues, followed by
// those earlier releases issued, which callers may still hold and present.
var credentialPrefixes = []string{ControllerPrefix, NodeHostPrefix, "swr_", "swu_", "swc_", "sws_", "swt_"}
