package main

import (
	"tool/cli"
	"encoding/yaml"
	"text/tabwriter"
)

_resources: timoni.apply.app

// Build: print all K8s manifests as multi-doc YAML.
// Usage: cue cmd -t debug -t name=linkari -t namespace=linkari -t mv=0.1.0 -t kv=1.36.0 build
command: build: {
	task: print: cli.Print & {
		text: yaml.MarshalStream(_resources)
	}
}

// List: print a table of resource kind/namespace/name and apiVersion.
// Usage: cue cmd -t debug -t name=linkari -t namespace=linkari -t mv=0.1.0 -t kv=1.36.0 ls
command: ls: {
	task: print: cli.Print & {
		text: tabwriter.Write([
			"RESOURCE \tAPI VERSION",
			for r in _resources {
				if r.metadata.namespace == _|_ {
					"\(r.kind)/\(r.metadata.name) \t\(r.apiVersion)"
				}
				if r.metadata.namespace != _|_ {
					"\(r.kind)/\(r.metadata.namespace)/\(r.metadata.name)  \t\(r.apiVersion)"
				}
			},
		])
	}
}
