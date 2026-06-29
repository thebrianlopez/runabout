package templates

import (
	timoniv1 "timoni.sh/core/v1alpha1"
)

// #PodDisruptionBudget makes the single-writer invariant explicit.
//
// With maxUnavailable: 0 + the existing Recreate strategy, the cluster
// structurally prevents accidental scale-up. This guards against:
//   - kubectl scale --replicas=2 (blocked by PDB)
//   - node drain during replacement (blocked until manually handled)
#PodDisruptionBudget: {
	#config:    #Config
	apiVersion: "policy/v1"
	kind:       "PodDisruptionBudget"
	metadata: timoniv1.#MetaComponent & {
		#Meta:      #config.metadata
		#Component: "pdb"
	}
	spec: {
		maxUnavailable: 0
		selector: matchLabels: #config.selector.labels
	}
}
