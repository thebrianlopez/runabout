package templates

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// #Deployment runs the linkari HTTP server as a single replica.
//
// hostNetwork: true  -  the pod shares the host network namespace so that:
//   1. EC2 IMDS at 169.254.169.254 is reachable without bumping the IMDSv2 hop limit.
//   2. Tailscale tsnet outbound connections use the host routing table.
//   3. Port 8080 binds directly on the node IP.
//
// Claude CLI access (scoreAsync subprocess):
//   /usr/bin (hostPath dir) → /usr/local/bin/claude via subPath mount.
//   /root/.claude (hostPath dir) → /root/.claude OAuth2 session tokens.
//   Mirrors the Dockerfile.claude-sandbox ARCHIVED decision: the claude CLI
//   runs as a host-access subprocess, not inside a sandboxed container.
#Deployment: appsv1.#Deployment & {
	#config:    #Config
	apiVersion: "apps/v1"
	kind:       "Deployment"
	metadata:   #config.metadata
	spec:       appsv1.#DeploymentSpec & {
		replicas: 1
		// hostNetwork binds the HTTP port on the node, so rolling updates cannot
		// create a surge pod before the old pod exits. Recreate guarantees at most
		// one linkari pod is running/binding port 8080 at any time.
		strategy: type:        "Recreate"
		selector: matchLabels: #config.selector.labels
		template: {
			metadata: labels: #config.selector.labels
			spec: corev1.#PodSpec & {
				// Share host network namespace: IMDS + tsnet outbound work without extra config.
				hostNetwork: true
				dnsPolicy:   "ClusterFirstWithHostNet"

				initContainers: [{
					name:            #config.metadata.name + "-db-restore"
					image:           #config.image.reference
					imagePullPolicy: #config.image.pullPolicy
					command: [
						"sh",
						"-ceu",
						"if [ -f /var/lib/linkari/queue.db ]; then\n" +
						"\techo \"skipping restore\"\n" +
						"\texit 0\n" +
						"fi\n" +
						"if [ ! -d /var/lib/linkari-backup ]; then\n" +
						"\techo \"IR-002 k8s_backup_mount_missing\"\n" +
						"\texit 1\n" +
						"fi\n" +
						"if [ ! -f /var/lib/linkari-backup/queue.db ]; then\n" +
						"\techo \"IR-003 restore_seed_absent\"\n" +
						"\texit 0\n" +
						"fi\n" +
						"cp /var/lib/linkari-backup/queue.db /var/lib/linkari/queue.db\n" +
						"echo \"IR-001 seed complete\"\n",
					]
					volumeMounts: [
						{
							name:      "linkari-data"
							mountPath: "/var/lib/linkari"
						},
						{
							name:      "linkari-backup"
							mountPath: "/var/lib/linkari-backup"
							readOnly:  true
						},
					]
				}]
				containers: [{
					name:            #config.metadata.name
					image:           #config.image.reference
					imagePullPolicy: #config.image.pullPolicy

					ports: [{
						name:          "http"
						containerPort: #config.service.port
						protocol:      "TCP"
					}]

					env: [{
						name:  "AWS_DEFAULT_REGION"
						value: #config.awsRegion
					}]

					volumeMounts: [
						// Claude CLI binary  -  the host /usr/bin dir is mounted; subPath
						// selects only the `claude` file, not the entire directory.
						{
							name:      "claude-bin"
							mountPath: "/usr/local/bin/claude"
							subPath:   "claude"
							readOnly:  true
						},
						// Claude OAuth2 credential store  -  the CLI may write refreshed tokens.
						{
							name:      "claude-creds"
							mountPath: "/root/.claude"
						},
						// config.toml from ConfigMap; subPath keeps the parent dir writable.
						{
							name:      "linkari-config"
							mountPath: "/root/.config/linkari/config.toml"
							subPath:   "config.toml"
							readOnly:  true
						},
						// Durable data: queue.db (SQLite) + tsnet/ state directory.
						{
							name:      "linkari-data"
							mountPath: "/var/lib/linkari"
						},
						// Firebase SA JSON  -  re-fetched from AWS SM at startup; emptyDir is fine.
						{
							name:      "linkari-cache"
							mountPath: "/root/.cache/linkari"
						},
						// XDG state dir  -  recreated on each start; emptyDir is fine.
						{
							name:      "linkari-state"
							mountPath: "/root/.local/state/linkari"
						},
					]

					// exec probe: when tsnetEnabled the server binds 127.0.0.1:8080 only;
					// tcpSocket probes in hostNetwork mode check the host IP, which misses
					// the loopback-only listener. nc -z 127.0.0.1 8080 checks explicitly.
					livenessProbe: {
						exec: command: ["sh", "-c", "nc -z 127.0.0.1 8080 < /dev/null"]
						initialDelaySeconds: 20
						periodSeconds:       30
						failureThreshold:    3
					}
					readinessProbe: {
						exec: command: ["sh", "-c", "nc -z 127.0.0.1 8080 < /dev/null"]
						initialDelaySeconds: 10
						periodSeconds:       10
						failureThreshold:    3
					}

					if #config.resources != _|_ {
						resources: #config.resources
					}
				},
					// Backup sidecar: runs linkari db backup --interval in a loop.
					// Opens queue.db read-only (WAL reader); writes to /var/lib/linkari-backup (RW).
					// Separate process preserves single-writer invariant; failed cycles are non-fatal.
					{
						name:            #config.metadata.name + "-backup"
						image:           #config.image.reference
						imagePullPolicy: #config.image.pullPolicy
						command: [
							"/usr/local/bin/linkari",
							"db",
							"backup",
							"--queue-db", "/var/lib/linkari/queue.db",
							"--dest", #config.backupPath,
							"--interval", #config.backupInterval,
							"--overwrite",
						]
						env: [{
							name:  "AWS_DEFAULT_REGION"
							value: #config.awsRegion
						}]
						volumeMounts: [
							// Read-only mount of the live DB directory
							{
								name:      "linkari-data"
								mountPath: "/var/lib/linkari"
								readOnly:  true
							},
							// Read-write mount of the backup PVC
							{
								name:      "linkari-backup"
								mountPath: "/var/lib/linkari-backup"
							},
						]
						// Restart on failure; logs go to pod/container logs
						restartPolicy: "Always"
					}]

				volumes: [
					// Host claude binary directory. subPath in the mount above picks `claude`.
					{
						name: "claude-bin"
						hostPath: {
							path: #config.claudeBinDir
							type: "Directory"
						}
					},
					// Host claude OAuth2 credential store.
					{
						name: "claude-creds"
						hostPath: {
							path: #config.claudeCredsDir
							type: "Directory"
						}
					},
					// K3S-adapted server.yaml ConfigMap.
					{
						name: "linkari-config"
						configMap: name: #config.metadata.name + "-config"
					},
					// Durable PVC: queue.db + tsnet state persist across pod restarts.
					{
						name: "linkari-data"
						persistentVolumeClaim: claimName: #config.metadata.name + "-data"
					},
					// Ephemeral cache: Firebase SA JSON re-fetched from AWS SM on each start.
					{
						name: "linkari-cache"
						emptyDir: {}
					},
					// Ephemeral XDG state dir.
					{
						name: "linkari-state"
						emptyDir: {}
					},
					// Backup PVC: sidecar (F6) writes snapshots here.
					{
						name: "linkari-backup"
						persistentVolumeClaim: claimName: #config.metadata.name + "-backup"
					},
				]
			}
		}
	}
}
