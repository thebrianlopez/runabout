package main

// EPIC-038 M8: Network policy per container executor.
//
// Defines ContainerNetworkPolicy and the oci.SpecOpts that enforce it.
// ffmpeg and whisper run with PolicyNone (isolated net namespace; no outbound
// access possible). The claude CLI runs with PolicyHost (shares the host
// network namespace so it can reach api.anthropic.com).
//
// OCI network isolation: "none" = private net namespace (OCI default, no
// extra interfaces beyond lo). "host" = WithHostNamespace(NetworkNamespace).
// gVisor enforces this at the sentry layer.

import (
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// ContainerNetworkPolicy controls the network namespace a container receives.
type ContainerNetworkPolicy int

const (
	// PolicyNone gives the container a private network namespace with no
	// external interfaces. Used for ffmpeg and whisper (no network needed).
	PolicyNone ContainerNetworkPolicy = iota

	// PolicyHost shares the host network namespace. Used for the claude CLI
	// subprocess which must reach api.anthropic.com.
	PolicyHost
)

// networkSpecOpts returns the oci.SpecOpts slice that installs the policy.
// PolicyNone returns nil — private net namespace is the OCI default and
// requires no additional spec options.
// PolicyHost adds WithHostNamespace(NetworkNamespace) to share host routes.
func (p ContainerNetworkPolicy) networkSpecOpts() []oci.SpecOpts {
	if p == PolicyHost {
		return []oci.SpecOpts{oci.WithHostNamespace(specs.NetworkNamespace)}
	}
	return nil
}
