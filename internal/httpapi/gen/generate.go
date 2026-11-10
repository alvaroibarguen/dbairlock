// Package gen holds the code generated from api/openapi.yaml. Do not edit
// gen.go by hand; change the contract and run `make generate`.
package gen

//go:generate go tool oapi-codegen -config config.yaml ../../../api/openapi.yaml
