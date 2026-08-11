package livechainacceptance

import (
	"fmt"
	"reflect"
	"strings"
)

// ProductionDependenciesConfig names every authority needed to run a durable
// acceptance session. None has a local, in-memory, or no-op default.
type ProductionDependenciesConfig struct {
	Prefix        string
	Objects       DistributedSessionObjectStore
	KMSSigner     KMSSignerAPI
	KMSVerifier   KMSVerifierAPI
	KMSKeyARN     string
	EffectAdapter IdempotentEffectAdapter
	Authority     AuthorityVerifier
	Production    ProductionSource
	Artifacts     ArtifactVerifier
	Health        HealthProbe
	Faults        FaultController
	Clock         Clock
}

// NewProductionDependencies assembles the only production-capable durable
// session path from explicit external authorities.
func NewProductionDependencies(config ProductionDependenciesConfig) (DurableDependencies, error) {
	if strings.Trim(config.Prefix, "/") == "" {
		return DurableDependencies{}, fmt.Errorf("production acceptance prefix is required")
	}
	required := []struct {
		name  string
		value any
	}{
		{name: "objects", value: config.Objects},
		{name: "KMS signer", value: config.KMSSigner},
		{name: "KMS verifier", value: config.KMSVerifier},
		{name: "effect adapter", value: config.EffectAdapter},
		{name: "authority", value: config.Authority},
		{name: "production", value: config.Production},
		{name: "artifacts", value: config.Artifacts},
		{name: "health", value: config.Health},
		{name: "faults", value: config.Faults},
		{name: "clock", value: config.Clock},
	}
	for _, dependency := range required {
		if nilInterface(dependency.value) {
			return DurableDependencies{}, fmt.Errorf("production acceptance %s is required", dependency.name)
		}
	}
	if strings.TrimSpace(config.KMSKeyARN) == "" {
		return DurableDependencies{}, fmt.Errorf("production acceptance KMS key is required")
	}
	sessions, err := NewCASSessionStore(config.Prefix, config.Objects)
	if err != nil {
		return DurableDependencies{}, err
	}
	effects, err := NewDurableEffectExecutor(config.Prefix, config.Objects, config.EffectAdapter)
	if err != nil {
		return DurableDependencies{}, err
	}
	signer, verifier, err := NewKMSSessionAuthority(config.KMSSigner, config.KMSVerifier, config.KMSKeyARN, config.Clock)
	if err != nil {
		return DurableDependencies{}, err
	}
	return DurableDependencies{
		Authority: config.Authority, Production: config.Production, Artifacts: config.Artifacts,
		Health: config.Health, Faults: config.Faults, Sessions: sessions, Effects: effects,
		Clock: config.Clock, StateSigner: signer, StateVerifier: verifier,
	}, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
