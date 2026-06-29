package templates

import (
	timoniv1 "timoni.sh/core/v1alpha1"
)

// #Config defines the schema and defaults for the linkari Timoni instance.
#Config: {
	// Required by Timoni  -  injected at apply time.
	kubeVersion!:  string
	moduleVersion!: string

	// Minimum K8s version: 1.20 (hostNetwork, local-path provisioner).
	clusterVersion: timoniv1.#SemVer & {#Version: kubeVersion, #Minimum: "1.20.0"}

	// Standard Timoni metadata and selector.
	metadata: timoniv1.#Metadata & {#Version: moduleVersion}
	metadata: labels: timoniv1.#Labels
	metadata: annotations?: timoniv1.#Annotations
	selector: timoniv1.#Selector & {#Name: metadata.name}

	// Container image. pullPolicy is fixed to Never: the image is loaded into
	// K3S containerd via nerdctl and never exists in a remote registry.
	image: timoniv1.#Image & {
		repository: *"linkari" | string
		tag:        *"latest" | string
		digest:     *"" | string
		pullPolicy: "Never"
	}

	// Resource requirements. CPU limit is high-millicore to absorb claude CLI bursts.
	resources: timoniv1.#ResourceRequirements & {
		requests: {
			cpu:    *"200m" | timoniv1.#CPUQuantity
			memory: *"256Mi" | timoniv1.#MemoryQuantity
		}
		limits: {
			cpu:    *"2000m" | timoniv1.#CPUQuantity
			memory: *"1Gi" | timoniv1.#MemoryQuantity
		}
	}

	// HTTP port the linkari server listens on.
	service: port: *8080 | int & >0 & <=65535

	// AWS region for Secrets Manager resolution.
	awsRegion: *"us-east-2" | string

	// Host paths for the Claude CLI.
	// claudeBinDir:   the directory on the host containing the `claude` binary.
	// claudeCredsDir: the ~/.claude/ OAuth2 credential store on the host.
	claudeBinDir:   *"/usr/bin" | string
	claudeCredsDir: *"/root/.claude" | string

	// PVC size for the durable data volume (queue.db + tsnet state).
	dataStorage: *"10Gi" | string

	// Backup PVC storage size and class (for sidecar snapshots).
	backupStorage:      *"10Gi" | string
	backupStorageClass: *"local-path" | string
	backupPath:         *"/var/lib/linkari-backup/queue.db" | string
	backupInterval:     *"6h" | string

	// Linkari server.yaml knobs  -  surfaced here so operators can override
	// a single field without replacing the entire ConfigMap content.
	server: {
		// AWS Secrets Manager path prefix for all linkari secrets.
		smPrefix: *"linkari" | string
		// Tailscale node hostname shown in the tailnet admin panel.
		tsnetHostname: *"linkari" | string
		// Minimum FCM score threshold (0 = all scores pushed).
		notifyMinScore: *0 | int & >=0
		// Shield middleware mode: "enforce" | "log" | "disabled".
		shieldMode: *"enforce" | string
	}
}

// #Instance wires config into Kubernetes objects.
#Instance: {
	config: #Config

	objects: {
		pvc:      #DataPVC & {#config: config}
		backupPvc: #BackupPVC & {#config: config}
		pdb:      #PodDisruptionBudget & {#config: config}
		cfgmap:   #ServerConfigMap & {#config: config}
		deploy:   #Deployment & {#config: config}
		service:  #Service & {#config: config}
	}
}
