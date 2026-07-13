// Package exporters bundles the resources needed to deploy the metrics
// exporter stack (node_exporter + dcgm-exporter) onto a Spark node.
package exporters

import _ "embed"

// ComposeYAML is the docker-compose definition for the exporter stack,
// embedded at build time so the binary is self-contained.
//
//go:embed docker-compose.yml
var ComposeYAML []byte
