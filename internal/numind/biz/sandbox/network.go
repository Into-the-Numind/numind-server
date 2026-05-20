package sandbox

// NetworkPolicyForBackend resolves a NetworkPolicy enum value into the
// concrete --network flag string the Docker CLI expects.
//
// v1 only fully supports NetworkPolicyNone (--network=none); Allowlist
// returns ErrAllowlistNotImplemented (stub for #14 e2e-rollout).
func NetworkPolicyForBackend(p NetworkPolicy) (string, error) {
	switch p {
	case NetworkPolicyNone:
		return "none", nil
	case NetworkPolicyAllowlist:
		return "", ErrAllowlistNotImplemented
	default:
		// Unknown policy: degrade to the most-isolated value (none) so the
		// sandbox stays safe by default. Caller can still detect unknown
		// values by checking p against the known enum values.
		return "none", nil
	}
}
