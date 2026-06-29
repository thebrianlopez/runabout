package templates

import (
	corev1 "k8s.io/api/core/v1"
	timoniv1 "timoni.sh/core/v1alpha1"
)

// #BackupPVC is the durable storage volume for backup snapshots.
//
// The sidecar process (F6) writes snapshots here via `linkari db backup --interval`.
// Separate from #DataPVC to provide failure domain isolation: one disk failure
// cannot lose both the live DB and all backups.
//
// Uses the backup storageClass (default local-path). The operator can override
// to point backups at a different disk/provisioner.
#BackupPVC: corev1.#PersistentVolumeClaim & {
	#config:    #Config
	apiVersion: "v1"
	kind:       "PersistentVolumeClaim"
	metadata: timoniv1.#MetaComponent & {
		#Meta:      #config.metadata
		#Component: "backup"
	}
	spec: corev1.#PersistentVolumeClaimSpec & {
		storageClassName: #config.backupStorageClass
		accessModes: ["ReadWriteOnce"]
		resources: requests: storage: #config.backupStorage
	}
}
