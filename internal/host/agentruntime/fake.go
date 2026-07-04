package agentruntime

import (
	"context"
	"fmt"
)

type Fake struct {
	Backend      string
	Strength     Strength
	LaunchResult Result
	LaunchError  error
	Seen         []LaunchSpec
	EnforceFS    bool
}

func NewFake(strength Strength) *Fake {
	if strength == "" {
		strength = StrengthFSConfined
	}
	return &Fake{Backend: "fake", Strength: strength}
}

func (f *Fake) Name() string {
	if f.Backend != "" {
		return f.Backend
	}
	return "fake"
}

func (f *Fake) Probe(context.Context) Capabilities {
	st := f.Strength
	if st == "" {
		st = StrengthFSConfined
	}
	return Capabilities{Backend: f.Name(), Strength: st, Features: []string{"fake"}}
}

func (f *Fake) Launch(ctx context.Context, spec LaunchSpec) (*Running, AppliedPolicy, error) {
	f.Seen = append(f.Seen, spec)
	if f.LaunchError != nil {
		return nil, AppliedPolicy{}, f.LaunchError
	}
	policy := AppliedPolicy{
		Backend:     f.Name(),
		Strength:    f.Probe(ctx).Strength,
		MinStrength: spec.EffectiveMin(),
		Repo:        spec.EffectiveRepo(),
		RW:          append([]string(nil), spec.RW...),
		Hidden:      append([]string(nil), spec.Hidden...),
		Network:     spec.EffectiveNetwork(),
		Degrade:     spec.EffectiveDegrade(),
	}
	if !policy.Strength.Satisfies(policy.MinStrength) {
		policy.Degraded = append(policy.Degraded, fmt.Sprintf("fake strength %s below min %s", policy.Strength, policy.MinStrength))
	}
	return NewRunning(policy, func(context.Context) (Result, error) {
		return f.LaunchResult, nil
	}), policy, nil
}
