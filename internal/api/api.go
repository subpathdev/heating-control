// Package api holds the HTTP contract between the backend and the page. Its Go
// code is generated from openapi.yaml.
package api

import _ "embed"

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2 -config config.yaml openapi.yaml

// Spec is the OpenAPI document.
//
//go:embed openapi.yaml
var Spec []byte
