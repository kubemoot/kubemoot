/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRecordToolIndexEmbedding_LookupFailureIsUnknown(t *testing.T) {
	boom := errors.New("api server unavailable")
	cl := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
	}).Build()
	r := &MCPGatewayReconciler{Client: cl, ConfigCache: NewConfigCache()}
	gw := toolIndexGateway(gwEmbedPilotModel, "")
	// A stale True must not survive a failed lookup.
	gw.Status.Conditions = []metav1.Condition{{Type: conditionTypeToolIndexEmbedding, Status: metav1.ConditionTrue, Reason: reasonEmbeddingResolved}}

	r.recordToolIndexEmbedding(context.Background(), gw)

	if c := gatewayCondition(t, gw); c.Status != metav1.ConditionUnknown || c.Reason != reasonEmbeddingLookupFailed || !strings.Contains(c.Message, boom.Error()) {
		t.Errorf("condition = %+v, want Unknown naming the error", c)
	}
}

func TestReconcileGatewayPreReqs_RecordsTheConditionWithoutRegistries(t *testing.T) {
	gw := toolIndexGateway(gwEmbedPilotModel, "")
	gw.Spec.Registries = nil
	r := gatewayEmbeddingReconciler(t, pilotEmbedding())

	if _, early := r.reconcileGatewayPreReqs(context.Background(), gw); early {
		t.Fatal("a gateway with no registries has nothing to wait for")
	}

	if c := gatewayCondition(t, gw); c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v, want resolved", c)
	}
}

func TestFindEmbeddingModel(t *testing.T) {
	r := gatewayEmbeddingReconciler(t, pilotEmbedding())
	ctx := context.Background()
	if em, err := findEmbeddingModel(ctx, r.Client, gwEmbedNS, gwEmbedPilotModel); err != nil || em == nil {
		t.Errorf("same namespace: %v %v", em, err)
	}
	if em, err := findEmbeddingModel(ctx, r.Client, "other-ns", gwEmbedPilotModel); err != nil || em == nil || em.Namespace != gwEmbedNS {
		t.Errorf("cluster-wide fallback: %v %v", em, err)
	}
	if em, err := findEmbeddingModel(ctx, r.Client, gwEmbedNS, "absent-embed"); err != nil || em != nil {
		t.Errorf("absent: %v %v, want nil without an error", em, err)
	}
}
