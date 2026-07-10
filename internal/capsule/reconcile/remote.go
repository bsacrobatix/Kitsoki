package reconcile

import (
	"context"
	"fmt"
	"strings"
)

// LocalBareRemotePublisher is the credential-free publish provider used by
// tests and local development. It publishes to a local/bare Git remote only
// after checking the live remote ref still matches the plan's expected target.
type LocalBareRemotePublisher struct {
	Remote string
}

func (p LocalBareRemotePublisher) Publish(ctx context.Context, plan Plan, refs ObservedRefs) (ApplyResult, error) {
	if plan.Operation != Publish {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: local bare publisher cannot apply %q", plan.Operation)
	}
	remote := strings.TrimSpace(p.Remote)
	if remote == "" {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: local bare remote is required")
	}
	target := publishTargetRef(plan.TargetRef, remote)
	live, err := git(ctx, plan.Workspace, "ls-remote", remote, target)
	if err != nil {
		return ApplyResult{}, err
	}
	liveOID := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(live), target))
	if fields := strings.Fields(live); len(fields) > 0 {
		liveOID = fields[0]
	}
	if liveOID != refs.Target {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: stale remote ref %s", target)
	}
	if _, err := git(ctx, plan.Workspace, "push", remote, plan.Candidate+":"+target); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{PlanDigest: plan.Digest, OldTarget: refs.Target, NewTarget: plan.Candidate, Applied: true}, nil
}

func publishTargetRef(target, remote string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "refs/") {
		return target
	}
	prefix := remote + "/"
	if strings.HasPrefix(target, prefix) {
		target = strings.TrimPrefix(target, prefix)
	}
	return "refs/heads/" + strings.TrimPrefix(target, "heads/")
}
