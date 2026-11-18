package bdd

import "github.com/cucumber/godog"

// The registry of wallet/spec/bdd/format.yml is implemented in four files, and
// these four functions are the only wiring between them. A step that is not
// registered by one of them does not exist.
//
//	steps_infra.go    run migration · in PostgreSQL: · in PostgreSQL query returns
//	steps_http.go     the HTTP request steps and every response assertion
//	steps_provider.go the payment provider, and the webhooks only a harness sends
//	steps_ops.go      the reconciler, service failure injection, settling
//
// RegisterInfraSteps and RegisterHTTPSteps are owned by HARNESS-A,
// RegisterProviderSteps and RegisterOpsSteps by HARNESS-B.
func register(sc *godog.ScenarioContext, w *World) {
	RegisterInfraSteps(sc, w)
	RegisterHTTPSteps(sc, w)
	RegisterProviderSteps(sc, w)
	RegisterOpsSteps(sc, w)
}
