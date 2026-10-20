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

// serverStateStore is the slice of the NATS publisher the sweep uses.
type serverStateStore interface {
	ListKVKeys(bucket string) ([]string, error)
	DeleteKVKey(bucket, key string) error
}

// ServerStateSweeper runs once at operator start and deletes entries in the
// server-state bucket that no ModelServer owns, such as those left by
// servers deleted while the operator was down.
type ServerStateSweeper struct {
	Reader client.Reader
	Store  serverStateStore
}

// Start sweeps once, then returns; the manager keeps running.
func (s *ServerStateSweeper) Start(ctx context.Context) error {
	if err := s.Sweep(ctx); err != nil {
		logf.FromContext(ctx).Error(err, "Server state sweep failed")
	}
	return nil
}

// NeedLeaderElection keeps the sweep to the leader so two operators never race.
func (s *ServerStateSweeper) NeedLeaderElection() bool { return true }

// Sweep deletes every bucket key whose name is not a ModelServer.
func (s *ServerStateSweeper) Sweep(ctx context.Context) error {
	servers := &aiv1alpha1.ModelServerList{}
	if err := s.Reader.List(ctx, servers); err != nil {
		return fmt.Errorf("listing ModelServers: %w", err)
	}
	owned := make(map[string]struct{}, len(servers.Items))
	for i := range servers.Items {
		owned[servers.Items[i].Name] = struct{}{}
	}
	keys, err := s.Store.ListKVKeys(ServerStateBucket)
	if err != nil {
		return fmt.Errorf("listing server state keys: %w", err)
	}
	for _, key := range keys {
		if _, ok := owned[key]; ok {
			continue
		}
		if err := s.Store.DeleteKVKey(ServerStateBucket, key); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to delete orphaned server state", "key", key)
		}
	}
	return nil
}
