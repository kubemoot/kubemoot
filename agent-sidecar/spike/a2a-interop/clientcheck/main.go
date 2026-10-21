// Command clientcheck sends one turn with the official SDK client over the HTTP+JSON binding (no gRPC
// package imported) to measure what adopting the client instead of the hand-written one costs in binary size.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

func main() {
	root := flag.String("card-url", "http://127.0.0.1:9000", "base URL of the agent card server")
	flag.Parse()
	ctx := context.Background()
	card, err := agentcard.DefaultResolver.Resolve(ctx, *root)
	if err != nil {
		log.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		log.Fatal(err)
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("which nodes have a GPU?"))
	resp, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%+v\n", resp)
}
