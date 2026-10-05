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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// providerStateStore is the slice of the NATS publisher the sweep uses.
type providerStateStore interface {
	ListKVKeys(bucket string) ([]string, error)
	DeleteKVKey(bucket, key string) error
}

// ProviderStateSweeper runs once at operator start and deletes entries in the
// provider-state bucket that no ModelProvider owns, such as those left by
// providers deleted while the operator was down.
type ProviderStateSweeper struct {
	Reader client.Reader
	Store  providerStateStore
}

// Start sweeps once, then returns; the manager keeps running.
func (s *ProviderStateSweeper) Start(ctx context.Context) error {
	if err := s.Sweep(ctx); err != nil {
		logf.FromContext(ctx).Error(err, "Provider state sweep failed")
	}
	return nil
}

// NeedLeaderElection keeps the sweep to the leader so two operators never race.
func (s *ProviderStateSweeper) NeedLeaderElection() bool { return true }

// Sweep deletes every bucket key whose name is not a ModelProvider.
func (s *ProviderStateSweeper) Sweep(ctx context.Context) error {
	providers := &aiv1alpha1.ModelProviderList{}
	if err := s.Reader.List(ctx, providers); err != nil {
		return fmt.Errorf("listing ModelProviders: %w", err)
	}
	owned := make(map[string]struct{}, len(providers.Items))
	for i := range providers.Items {
		owned[providers.Items[i].Name] = struct{}{}
	}
	keys, err := s.Store.ListKVKeys(ProviderStateBucket)
	if err != nil {
		return fmt.Errorf("listing provider state keys: %w", err)
	}
	for _, key := range keys {
		if _, ok := owned[key]; ok {
			continue
		}
		if err := s.Store.DeleteKVKey(ProviderStateBucket, key); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to delete orphaned provider state", "key", key)
		}
	}
	return nil
}
