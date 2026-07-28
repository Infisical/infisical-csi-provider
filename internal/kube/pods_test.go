package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func pod(uid, startedAt string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "a-pod", Namespace: "ns", UID: apitypes.UID(uid),
		Annotations: map[string]string{"startedAt": startedAt},
	}}
}

func listerHolding(t *testing.T, pods ...*corev1.Pod) listers.PodLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, p := range pods {
		if err := indexer.Add(p); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	return listers.NewPodLister(indexer)
}

// A pod name can be reused: the cache may still hold the previous incarnation, whose start time
// belongs to a lifetime that has ended. Serving it would report a long-closed window for a pod that
// just started, so a UID mismatch has to reach the API server instead.
func TestStaleCacheEntryFallsBackToTheApiServer(t *testing.T) {
	cached := pod("old-uid", "yesterday")
	fresh := pod("new-uid", "now")

	p := &Pods{clientset: fake.NewClientset(fresh), lister: listerHolding(t, cached)}

	got, err := p.Get(context.Background(), "ns", "a-pod", "new-uid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.UID) != "new-uid" {
		t.Fatalf("expected the fresh pod from the API server, got UID %q", got.UID)
	}
}

func TestCacheHitWhenTheUidMatches(t *testing.T) {
	cached := pod("same-uid", "now")
	// The API server deliberately holds nothing, so a hit can only come from the cache.
	p := &Pods{clientset: fake.NewClientset(), lister: listerHolding(t, cached)}

	got, err := p.Get(context.Background(), "ns", "a-pod", "same-uid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations["startedAt"] != "now" {
		t.Fatalf("expected the cached pod, got %+v", got.Annotations)
	}
}

// Without a node name there is no watch to scope, so every read goes to the API server rather than
// caching every pod in the cluster on every node.
func TestWithoutANodeNameEveryReadHitsTheApiServer(t *testing.T) {
	fresh := pod("only-uid", "now")
	p := NewPods(context.Background(), fake.NewClientset(fresh), "")

	got, err := p.Get(context.Background(), "ns", "a-pod", "only-uid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.UID) != "only-uid" {
		t.Fatalf("expected the pod from the API server, got UID %q", got.UID)
	}
}
