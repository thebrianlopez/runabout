package templates

import (
	corev1 "k8s.io/api/core/v1"
	timoniv1 "timoni.sh/core/v1alpha1"
)

// #DataPVC is the durable storage volume for the linkari server.
//
// Contains:
//   queue.db   - SQLite queue/archive/feedback database
//   tsnet/     - Tailscale node state (persisted so the node doesn't re-register
//                on every pod restart, consuming tsnet auth key quota)
//
// Uses K3S's default local-path provisioner. Data survives pod restarts but
// not node replacement  -  acceptable for single-node self-hosted K3S.
#DataPVC: corev1.#PersistentVolumeClaim & {
	#config:    #Config
	apiVersion: "v1"
	kind:       "PersistentVolumeClaim"
	metadata: timoniv1.#MetaComponent & {
		#Meta:      #config.metadata
		#Component: "data"
	}
	spec: corev1.#PersistentVolumeClaimSpec & {
		storageClassName: "local-path"
		accessModes: ["ReadWriteOnce"]
		resources: requests: storage: #config.dataStorage
	}
}
