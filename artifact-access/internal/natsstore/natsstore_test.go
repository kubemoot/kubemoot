package natsstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func TestOpenRetainsConfigWithoutConnecting(t *testing.T) {
	s := Open("nats://example:4222", "my-bucket")
	if s == nil {
		t.Fatal("Open returned nil")
	}
	// Open must NOT dial - it only holds config; connections are per-Get.
	if s.url != "nats://example:4222" || len(s.buckets) != 1 || s.buckets[0] != "my-bucket" {
		t.Errorf("Open did not retain config: url=%q buckets=%q", s.url, s.buckets)
	}
}

func TestGetReturnsConnectErrorOnUnreachableServer(t *testing.T) {
	// Port 1 refuses connections, so the initial nats.Connect fails fast and Get
	// returns the wrapped "connect" error - and must not leak a reader.
	s := Open("nats://127.0.0.1:1", "bucket")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := s.Get(ctx, "crew/conv/thread/agent/tool-1")
	if rc != nil {
		_ = rc.Close()
		t.Fatal("no reader should be returned on a failed connect")
	}
	if err == nil {
		t.Fatal("expected a connect error against an unreachable server")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("expected a wrapped connect error, got %v", err)
	}
}

const (
	newBucket = "new"
	oldBucket = "old"
)

func TestTheFirstBucketThatHasTheObjectAnswers(t *testing.T) {
	var tried []string
	got, err := inFirstBucket([]string{newBucket, oldBucket}, func(b string) (string, error) {
		tried = append(tried, b)
		return "from " + b, nil
	})
	if err != nil || got != "from "+newBucket || len(tried) != 1 {
		t.Errorf("got %q, %v, tried %v", got, err, tried)
	}
}

func TestAnObjectOrBucketMissingFromTheFirstFallsThroughToTheNext(t *testing.T) {
	for name, miss := range map[string]error{
		"no such bucket": fmt.Errorf("object store %q: %w", "new", jetstream.ErrBucketNotFound),
		"no such object": jetstream.ErrObjectNotFound,
	} {
		got, err := inFirstBucket([]string{newBucket, oldBucket}, func(b string) (string, error) {
			if b == newBucket {
				return "", miss
			}
			return "from " + b, nil
		})
		if err != nil || got != "from old" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestOtherErrorsStopTheSearch(t *testing.T) {
	boom := errors.New("connection reset")
	var tried []string
	_, err := inFirstBucket([]string{newBucket, oldBucket}, func(b string) (string, error) {
		tried = append(tried, b)
		return "", boom
	})
	if !errors.Is(err, boom) || len(tried) != 1 {
		t.Errorf("err = %v, tried %v: a real failure must not be read as a miss", err, tried)
	}
}

func TestAnObjectInNoBucketReportsTheFirstBucketsError(t *testing.T) {
	_, err := inFirstBucket([]string{newBucket, oldBucket}, func(b string) (string, error) {
		return "", fmt.Errorf("%s: %w", b, jetstream.ErrObjectNotFound)
	})
	if !errors.Is(err, jetstream.ErrObjectNotFound) || !strings.Contains(err.Error(), "new") {
		t.Errorf("err = %v", err)
	}
	if _, err := inFirstBucket(nil, func(string) (string, error) { return "", nil }); err == nil {
		t.Error("a store with no bucket must say so")
	}
}
