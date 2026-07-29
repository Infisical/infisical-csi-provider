// Package kube serves pod lookups, from a watch when it can and from the API server otherwise.
//
// With rotation enabled the driver calls Mount for every pod on every poll, so a plain request per
// call is one API call per pod per interval per node. A shared informer collapses that to one watch,
// scoped by field selector to the node this DaemonSet pod runs on.
package kube

import (
	"context"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listers "k8s.io/client-go/listers/core/v1"
)

// Pods reads pods, preferring a watch of this node's pods when one is running.
type Pods struct {
	clientset kubernetes.Interface
	// lister is nil when no node name was available to scope a watch, in which case every read goes to
	// the API server.
	lister listers.PodLister
}

// NewPods watches the pods scheduled on nodeName.
//
// An empty nodeName leaves the watch unstarted and reads go straight to the API server. That keeps an
// image upgrade from breaking a deployment whose manifests do not set NODE_NAME yet, and avoids the
// alternative of caching every pod in the cluster on every node.
//
// It does not block on the initial sync. The driver publishes a volume very early in a pod's life, so
// a cold cache is a normal state rather than an error, and Get falls back on a miss.
func NewPods(ctx context.Context, clientset kubernetes.Interface, nodeName string) *Pods {
	if nodeName == "" {
		log.Print("NODE_NAME is not set, reading pods from the API server instead of a watch")
		return &Pods{clientset: clientset}
	}

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", nodeName).String()
		}))
	informer := factory.Core().V1().Pods()
	lister := informer.Lister()
	factory.Start(ctx.Done())

	return &Pods{clientset: clientset, lister: lister}
}

// Get returns a pod, preferring the cache.
//
// A cached object whose UID does not match is treated as a miss. Names get reused: deleting a pod and
// recreating it under the same name leaves the cache briefly holding the old object, whose start time
// belongs to a lifetime that has ended, and trusting it reports a long-closed window for a pod that
// just started.
//
// Only a miss falls back. Any other lister error means the watch is unhealthy, and silently reverting
// to a request per mount would undo the point of the watch without saying so.
func (p *Pods) Get(ctx context.Context, namespace, name, uid string) (*corev1.Pod, error) {
	if p.lister == nil {
		return p.fromApiServer(ctx, namespace, name, uid)
	}

	pod, err := p.lister.Pods(namespace).Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("pod watch is unhealthy: %w", err)
	}
	if err == nil && (uid == "" || string(pod.UID) == uid) {
		return pod, nil
	}
	return p.fromApiServer(ctx, namespace, name, uid)
}

// fromApiServer reads the pod live, and rejects a name that now resolves to a different incarnation.
//
// A name can be reused, so the pod the API server returns may be the replacement of the one the mount
// was created for. Evaluating the window against the replacement's fresh start time would serve
// secrets to a mount from a lifetime that has ended, so a UID mismatch is reported as not found and
// the caller fails closed, exactly as it does for a pod that is simply gone. This mirrors the UID
// check on the cache path; without it that guard is only as good as the teardown race that hides the
// gap.
func (p *Pods) fromApiServer(ctx context.Context, namespace, name, uid string) (*corev1.Pod, error) {
	pod, err := p.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if uid != "" && string(pod.UID) != uid {
		return nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
	}
	return pod, nil
}
