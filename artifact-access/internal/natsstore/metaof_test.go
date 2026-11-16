package natsstore

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestMetaOfDecodesEncodedHeaderValuesAndKeepsOthers(t *testing.T) {
	info := &jetstream.ObjectInfo{
		ObjectMeta: jetstream.ObjectMeta{
			Name: "ns/c/t1/a/001-pods_list",
			Headers: nats.Header{
				"gen_ai.tool.call.arguments": {"utf8-pct:{\"q\":\"caf%C3%A9 %F0%9F%9A%80\"}"},
				"gen_ai.tool.name":           {"pods_list"},
				"kubemoot.rows":              {},
			},
		},
		Size: 3,
	}

	m := metaOf(info)

	if got, want := m.Headers["gen_ai.tool.call.arguments"], "{\"q\":\"café \U0001F680\"}"; got != want {
		t.Errorf("arguments = %q, want %q", got, want)
	}
	if got := m.Headers["gen_ai.tool.name"]; got != "pods_list" {
		t.Errorf("an unencoded value changed: %q", got)
	}
	if _, ok := m.Headers["kubemoot.rows"]; ok {
		t.Error("a header with no value is kept")
	}
}
