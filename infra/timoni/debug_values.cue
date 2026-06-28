@if(debug)

package main

// Debug-time overrides used by debug_tool.cue.
// These are NOT applied in production  -  only active when -t debug is passed.
//
// Usage: cue cmd -t debug -t name=linkari -t namespace=linkari -t mv=0.1.0 -t kv=1.36.0 build
values: {
	image: {
		repository: "linkari"
		tag:        "latest"
		digest:     ""
		pullPolicy: "Never"
	}

	resources: {
		requests: {
			cpu:    "200m"
			memory: "256Mi"
		}
		limits: {
			cpu:    "2000m"
			memory: "1Gi"
		}
	}

	server: {
		smPrefix:       "linkari"
		tsnetHostname:  "linkari"
		notifyMinScore: 0
		shieldMode:     "enforce"
	}
}
