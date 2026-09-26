package controller

import (
	"context"
	"testing"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mapRequests drives an EventHandler with a create event and collects the requests it enqueues.
func mapRequests(t *testing.T, h handler.EventHandler, obj client.Object) []reconcile.Request {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue[reconcile.Request](workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	h.Create(context.Background(), event.CreateEvent{Object: obj}, q)
	var reqs []reconcile.Request
	for q.Len() > 0 {
		r, _ := q.Get()
		reqs = append(reqs, r)
		q.Done(r)
	}
	return reqs
}
