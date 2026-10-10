// Command wiresize prints how big the same message is as Protobuf and as JSON.
//
// It exists so the "binary encoding is smaller" claim can be MEASURED on stage
// rather than asserted on a slide. It encodes the actual request and response
// types this system uses, so the numbers are this system's numbers.
//
//	make wire-size
package main

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	orderv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/order/v1"
	productv1 "github.com/lakhansamani/grpc-ecs-demo/gen/go/product/v1"
)

func row(name string, m proto.Message) {
	pb, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	// protojson is the canonical proto3 JSON mapping - the same shape the
	// generated REST gateway puts on the wire.
	js, err := protojson.Marshal(m)
	if err != nil {
		panic(err)
	}
	fmt.Printf("  %-32s %5d B   %5d B   %.1fx\n", name, len(pb), len(js),
		float64(len(js))/float64(len(pb)))
}

func main() {
	fmt.Println()
	fmt.Printf("  %-32s %7s   %7s   %s\n", "MESSAGE", "PROTOBUF", "JSON", "RATIO")
	fmt.Printf("  %-32s %7s   %7s   %s\n", "-------", "--------", "----", "-----")

	row("CreateOrderRequest, 1 item", &orderv1.CreateOrderRequest{
		Items:          []*orderv1.RequestedItem{{ProductId: "p-1001", Quantity: 1}},
		IdempotencyKey: "cart-7f3a9c21",
	})
	row("CreateOrderRequest, 3 items", &orderv1.CreateOrderRequest{
		Items: []*orderv1.RequestedItem{
			{ProductId: "p-1001", Quantity: 1},
			{ProductId: "p-1010", Quantity: 2},
			{ProductId: "p-1005", Quantity: 1},
		},
		IdempotencyKey: "cart-7f3a9c21",
	})
	row("Product, one catalogue entry", sampleProduct("p-1001"))

	var ps []*productv1.Product
	for i := 0; i < 20; i++ {
		ps = append(ps, sampleProduct(fmt.Sprintf("p-10%02d", i)))
	}
	row("SearchProductsResponse, 20", &productv1.SearchProductsResponse{
		Products: ps, TotalMatches: 20,
	})

	fmt.Println(`
  Protobuf drops the field NAMES - a field is a number and a wire type - and
  packs numbers as varints instead of decimal text. JSON repeats every key on
  every object, which is why the gap widens with repeated fields.

  Worth saying out loud: on a browse-heavy read path this is real, and it is
  still usually not why teams adopt gRPC. Your database will cost you more
  than your encoder.`)
}

func sampleProduct(id string) *productv1.Product {
	return &productv1.Product{
		Id: id, Sku: "SONY-WH1000XM5",
		Title: "Wireless Noise Cancelling Headphones",
		Brand: "Sony", Category: "audio",
		PriceMinor: 2999900, Currency: "INR", Stock: 42,
	}
}
