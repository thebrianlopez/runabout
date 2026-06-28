package templates

import (
	corev1 "k8s.io/api/core/v1"
)

// #Service exposes linkari within the cluster.
//
// With hostNetwork: true the port is already bound on the node IP directly.
// This ClusterIP lets other in-cluster workloads reach linkari at
// linkari.linkari.svc.cluster.local:8080 without going through the node IP.
//
// External access: Tailscale tsnet handles HTTPS exposure to Android and Chrome.
// No LoadBalancer or Ingress is needed for the primary share path.
#Service: corev1.#Service & {
	#config:    #Config
	apiVersion: "v1"
	kind:       "Service"
	metadata:   #config.metadata
	spec: corev1.#ServiceSpec & {
		type:     corev1.#ServiceTypeClusterIP
		selector: #config.selector.labels
		ports: [{
			name:       "http"
			port:       #config.service.port
			targetPort: "http"
			protocol:   "TCP"
		}]
	}
}
