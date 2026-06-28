// Note: this file must have no imports and all values must be concrete.

@if(!debug)

package main

// Operator overrides go here. All fields are optional  -  omitted fields
// take the defaults defined in templates/config.cue.
//
// Example overrides:
//   values: image: tag: "v1.2.3"
//   values: resources: limits: memory: "2Gi"
//   values: server: shieldMode: "log"
values: {}
