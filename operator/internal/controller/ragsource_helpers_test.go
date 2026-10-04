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
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestHelperBuildGitSourceEnv(t *testing.T) {
	t.Run("nil git spec", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{}
		env := buildGitSourceEnv(rs)
		if env != nil {
			t.Errorf("expected nil, got %v", env)
		}
	})

	t.Run("basic git source", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Git: &kubemootv1alpha1.GitSource{
						URL:    testRepoURL,
						Branch: testMain,
					},
				},
			},
		}
		env := buildGitSourceEnv(rs)
		if len(env) != 2 {
			t.Fatalf("expected 2 env vars, got %d", len(env))
		}
		if env[0].Value != testRepoURL {
			t.Errorf("unexpected git URL: %s", env[0].Value)
		}
	})

	t.Run("with paths", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Git: &kubemootv1alpha1.GitSource{
						URL:    testRepoURL,
						Branch: testMain,
						Paths:  []string{"docs", "content"},
					},
				},
			},
		}
		env := buildGitSourceEnv(rs)
		if len(env) != 3 {
			t.Fatalf("expected 3 env vars, got %d", len(env))
		}
		if env[2].Value != "docs,content" {
			t.Errorf("unexpected paths value: %s", env[2].Value)
		}
	})
}

func TestHelperBuildS3SourceEnv(t *testing.T) {
	t.Run("nil s3 spec", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{}
		env := buildS3SourceEnv(rs)
		if env != nil {
			t.Errorf("expected nil, got %v", env)
		}
	})

	t.Run("with endpoint", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					S3: &kubemootv1alpha1.S3Source{
						Bucket:   "my-bucket",
						Prefix:   "docs/",
						Region:   "us-east-1",
						Endpoint: "http://minio:9000",
					},
				},
			},
		}
		env := buildS3SourceEnv(rs)
		if len(env) != 4 {
			t.Fatalf("expected 4 env vars, got %d", len(env))
		}
	})

	t.Run("without endpoint", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					S3: &kubemootv1alpha1.S3Source{
						Bucket: "my-bucket",
						Prefix: "docs/",
						Region: "us-west-2",
					},
				},
			},
		}
		env := buildS3SourceEnv(rs)
		if len(env) != 3 {
			t.Fatalf("expected 3 env vars, got %d", len(env))
		}
	})
}

func TestHelperBuildNatsKVSourceEnv(t *testing.T) {
	t.Run("nil spec", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{}
		env := buildNatsKVSourceEnv(rs)
		if env != nil {
			t.Errorf("expected nil, got %v", env)
		}
	})

	t.Run("basic nats kv", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					NatsKV: &kubemootv1alpha1.NatsKVSource{
						Bucket: "kubemoot_agent_state",
						Key:    "resumes",
					},
				},
			},
		}
		env := buildNatsKVSourceEnv(rs)
		if len(env) < 3 {
			t.Fatalf("expected at least 3 env vars, got %d", len(env))
		}
	})

	t.Run("with content hash", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					NatsKV: &kubemootv1alpha1.NatsKVSource{
						Bucket:      "test",
						Key:         "data",
						ContentHash: "abc123",
					},
				},
			},
		}
		env := buildNatsKVSourceEnv(rs)
		hashFound := false
		for _, e := range env {
			if e.Name == "KUBEMOOT_NATS_KV_CONTENT_HASH" && e.Value == "abc123" {
				hashFound = true
			}
		}
		if !hashFound {
			t.Error("expected content hash env var")
		}
	})
}

func TestHelperBuildChunkingEnv(t *testing.T) {
	t.Run("nil chunking", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{}
		env := buildChunkingEnv(rs)
		if env != nil {
			t.Errorf("expected nil, got %v", env)
		}
	})

	t.Run("with chunking config", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Chunking: &kubemootv1alpha1.ChunkingConfig{
					ChunkSize:    512,
					ChunkOverlap: 64,
				},
			},
		}
		env := buildChunkingEnv(rs)
		if len(env) != 2 {
			t.Fatalf("expected 2 env vars, got %d", len(env))
		}
		if env[0].Value != "512" {
			t.Errorf("expected chunk size 512, got %s", env[0].Value)
		}
		if env[1].Value != "64" {
			t.Errorf("expected chunk overlap 64, got %s", env[1].Value)
		}
	})
}

func TestHelperBuildSourceTypeEnv(t *testing.T) {
	t.Run("git type dispatches correctly", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Type: kubemootv1alpha1.RAGSourceTypeGit,
					Git: &kubemootv1alpha1.GitSource{
						URL:    "https://example.com",
						Branch: testMain,
					},
				},
			},
		}
		env := buildSourceTypeEnv(rs)
		if len(env) == 0 {
			t.Error("expected env vars for git source")
		}
	})

	t.Run("unknown type returns nil", func(t *testing.T) {
		rs := &kubemootv1alpha1.RAGSource{
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Type: testUnknown,
				},
			},
		}
		env := buildSourceTypeEnv(rs)
		if env != nil {
			t.Errorf("expected nil for unknown type, got %v", env)
		}
	})
}

func assertParseJobNilAnnotations(t *testing.T) {
	job := &batchv1.Job{}
	status, checksum, docs, chunks := parseJobAnnotations(job)
	if status != "" || checksum != "" || docs != 0 || chunks != 0 {
		t.Error("expected all zero values")
	}
}

func assertParseJobFullAnnotations(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubemoot.ai/status":         "indexed",
				"kubemoot.ai/checksum":       "sha256:abc",
				"kubemoot.ai/document-count": "42",
				"kubemoot.ai/chunk-count":    "256",
			},
		},
	}
	status, checksum, docs, chunks := parseJobAnnotations(job)
	if status != "indexed" {
		t.Errorf("expected indexed, got %s", status)
	}
	if checksum != "sha256:abc" {
		t.Errorf("expected sha256:abc, got %s", checksum)
	}
	if docs != 42 {
		t.Errorf("expected 42 docs, got %d", docs)
	}
	if chunks != 256 {
		t.Errorf("expected 256 chunks, got %d", chunks)
	}
}

func assertParseJobInvalidCounts(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubemoot.ai/document-count": "not-a-number",
				"kubemoot.ai/chunk-count":    "also-bad",
			},
		},
	}
	_, _, docs, chunks := parseJobAnnotations(job)
	if docs != 0 || chunks != 0 {
		t.Error("expected zero for invalid counts")
	}
}

func TestHelperParseJobAnnotations(t *testing.T) {
	t.Run("nil annotations", assertParseJobNilAnnotations)
	t.Run("full annotations", assertParseJobFullAnnotations)
	t.Run("invalid count strings", assertParseJobInvalidCounts)
}

func TestHelperFindActiveJob(t *testing.T) {
	t.Run("no jobs", func(t *testing.T) {
		list := &batchv1.JobList{}
		if findActiveJob(list) != nil {
			t.Error("expected nil for empty list")
		}
	})

	t.Run("all completed", func(t *testing.T) {
		list := &batchv1.JobList{
			Items: []batchv1.Job{
				{
					Status: batchv1.JobStatus{
						Conditions: []batchv1.JobCondition{
							{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
						},
					},
				},
			},
		}
		if findActiveJob(list) != nil {
			t.Error("expected nil for completed jobs")
		}
	})

	t.Run("one active", func(t *testing.T) {
		list := &batchv1.JobList{
			Items: []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "active-job"},
					Status:     batchv1.JobStatus{},
				},
			},
		}
		active := findActiveJob(list)
		if active == nil {
			t.Fatal("expected active job")
		}
		if active.Name != "active-job" {
			t.Errorf("expected active-job, got %s", active.Name)
		}
	})
}

// The buildIndexerScriptVolumes cases are split into one test function each
// (rather than t.Run closures in a single function) so no single function
// accumulates the branches of all three cases - each stays well under the
// cognitive-complexity gate while asserting exactly what it did before.

func TestHelperBuildIndexerScriptVolumesNoScriptConfig(t *testing.T) {
	rs := &kubemootv1alpha1.RAGSource{}
	vols, mounts, env := buildIndexerScriptVolumes(rs)
	if vols != nil || mounts != nil || env != nil {
		t.Error("expected nil for no script config")
	}
}

func TestHelperBuildIndexerScriptVolumesInlineScript(t *testing.T) {
	rs := &kubemootv1alpha1.RAGSource{
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Indexer: &kubemootv1alpha1.IndexerConfig{
				Script: &kubemootv1alpha1.IndexerScriptSource{
					Inline: "print('hello')",
				},
			},
		},
	}
	vols, mounts, env := buildIndexerScriptVolumes(rs)
	if len(vols) != 0 || len(mounts) != 0 {
		t.Error("inline script should not add volumes")
	}
	if len(env) != 1 || env[0].Name != "KUBEMOOT_INLINE_SCRIPT" {
		t.Errorf("expected KUBEMOOT_INLINE_SCRIPT, got %v", env)
	}
}

func TestHelperBuildIndexerScriptVolumesConfigMapScript(t *testing.T) {
	rs := &kubemootv1alpha1.RAGSource{
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Indexer: &kubemootv1alpha1.IndexerConfig{
				Script: &kubemootv1alpha1.IndexerScriptSource{
					ConfigMapRef: &corev1.LocalObjectReference{Name: "my-scripts"},
					ScriptKey:    "custom.py",
				},
			},
		},
	}
	vols, mounts, env := buildIndexerScriptVolumes(rs)
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(vols))
	}
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}
	if len(env) != 1 || env[0].Value != "custom.py" {
		t.Errorf("expected KUBEMOOT_SCRIPT_KEY=custom.py, got %v", env)
	}
}
