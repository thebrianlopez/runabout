package templates

import (
	corev1 "k8s.io/api/core/v1"
	timoniv1 "timoni.sh/core/v1alpha1"
)

// #ServerConfigMap holds the linkari config.toml (TOML format).
//
// Key differences from the host ~/.config/linkari/config.toml:
//   queue_db:        /var/lib/linkari/queue.db   (PVC-backed)
//   tsnet_state_dir: /var/lib/linkari/tsnet       (PVC-backed, survives restarts)
//   log_file:        ""                           (stdout only  -  K3S log aggregation)
//
// Secretsmanager refs use ${secretsmanager:name} syntax, resolved at pod startup
// by expandConfigRefs via the EC2 instance role (hostNetwork = IMDS reachable).
#ServerConfigMap: corev1.#ConfigMap & {
	#config:    #Config
	apiVersion: "v1"
	kind:       "ConfigMap"
	metadata: timoniv1.#MetaComponent & {
		#Meta:      #config.metadata
		#Component: "config"
	}
	data: "config.toml": """
		[server]
		google_client_id     = "${secretsmanager:\(#config.server.smPrefix)/google-client-id}"
		google_client_secret = "${secretsmanager:\(#config.server.smPrefix)/google-client-secret}"
		invite_codes         = ["8182980568", "7734260323"]
		token                = "${secretsmanager:\(#config.server.smPrefix)/bearer-token}"
		tsnet                = true
		tsnet_authkey        = "${secretsmanager:\(#config.server.smPrefix)/tsnet-authkey}"
		tsnet_hostname       = "\(#config.server.tsnetHostname)"
		tsnet_state_dir      = "/var/lib/linkari/tsnet"
		firebase_sa          = "secretsmanager://\(#config.server.smPrefix)/firebase-sa"
		notify_min_score     = \(#config.server.notifyMinScore)
		notify_on_prefilter_skip = true
		atlassian_email           = "${secretsmanager:\(#config.server.smPrefix)/jira-webhook#ATLASSIAN_EMAIL}"
		atlassian_api_token       = "${secretsmanager:\(#config.server.smPrefix)/jira-webhook#ATLASSIAN_API_TOKEN}"
		atlassian_confluence_token = "${secretsmanager:\(#config.server.smPrefix)/jira-webhook#ATLASSIAN_API_TOKEN}"
		jira_domain               = "${secretsmanager:\(#config.server.smPrefix)/jira-webhook#JIRA_DOMAIN}"
		pagerduty_token           = "${secretsmanager:\(#config.server.smPrefix)/jira-webhook#PAGERDUTY_API_TOKEN}"
		port       = \(#config.service.port)
		server_url = ""
		queue_db   = "/var/lib/linkari/queue.db"
		log_file   = ""
		debug      = false

		[server.shield]
		mode = "\(#config.server.shieldMode)"
		"""
}
