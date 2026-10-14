package usage_test

import (
	"context"
	"fmt"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/usage"
)

func ExampleNewReporter() {
	hub := circlexotest.NewServer("demo")
	defer hub.Close()
	reporter := usage.NewReporter(circlexo.NewClient(hub.Config(), nil), 10, 1)
	// Close drains queued reports before the hub is closed.
	defer func() {
		if err := reporter.Close(context.Background()); err != nil {
			panic(err)
		}
	}()
	report := circlexo.UsageReport{OrgID: "org-1", Feature: "api_calls", Qty: 2, IdempotencyKey: "example:request:1"}
	first, err := reporter.Send(context.Background(), report)
	if err != nil {
		panic(err)
	}
	replay, err := reporter.Send(context.Background(), report)
	if err != nil {
		panic(err)
	}
	fmt.Println("quantity:", first.Qty)
	fmt.Println("same work replayed:", replay.Replayed)
	// Output:
	// quantity: 2
	// same work replayed: true
}
