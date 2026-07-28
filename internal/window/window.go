// Package window bounds how long a pod may read its mounted secrets.
//
// A pod that can read its secrets for its whole life hands the same access to anything that later
// achieves code execution inside it. Serving the secrets only while the containers are freshly
// started shrinks that to a window around startup, which is when the application actually needs
// them.
//
// The window is evaluated from the pod's own status in the API server, which the pod cannot forge.
package window

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodSource reads a pod by namespace and name. Implemented by internal/kube.
//
// The uid identifies which incarnation of that name is wanted, and may be empty. It matters because a
// pod can be replaced by a new one with the same name, and a cache still holding the old object would
// otherwise answer with a start time from the previous pod's lifetime.
type PodSource interface {
	Get(ctx context.Context, namespace, name, uid string) (*corev1.Pod, error)
}

// Pod identifies the mount being evaluated.
type Pod struct {
	Namespace string
	Name      string
	UID       string
	// Volume is the CSI volume this mount publishes. The window follows the containers that mount it,
	// so an unrelated container restarting cannot reopen it. When empty, or when nothing mounts it, the
	// window rides the pod's own start time and closes on schedule rather than risking being held open
	// by something else.
	Volume string
}

// IsOpen reports whether the containers consuming this volume entered their current run less than
// duration ago.
//
// The reference is the most recent of the pod's start time and the start of a consuming container.
// The pod's start time is what matters at mount time, because the driver publishes the volume before
// any container runs, so keying only on container start would report a closed window exactly when the
// secrets are needed. A consumer restarted in place gets a fresh start time, and that is what reopens
// the window for it: the kubelet replays nothing on its behalf, so the application comes back needing
// its secrets again.
//
// Only consumers count. A sidecar that does not mount the volume and happens to crash loop would
// otherwise hold the window open for the pod's whole life, which is the exact exposure this is meant
// to bound, and something code running in the pod could trigger deliberately.
func IsOpen(ctx context.Context, pods PodSource, target Pod, duration time.Duration) (bool, error) {
	pod, err := pods.Get(ctx, target.Namespace, target.Name, target.UID)
	if err != nil {
		return false, fmt.Errorf("unable to read pod %s/%s: %w", target.Namespace, target.Name, err)
	}

	// Very early in a pod's life the status is not populated yet.
	startedAt := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil {
		startedAt = pod.Status.StartTime.Time
	}
	// Whichever consumer started last carries the window, so a pod whose containers come up at slightly
	// different times still gets one window covering all of them.
	if consumer := latestConsumerStart(pod, target.Volume); consumer.After(startedAt) {
		startedAt = consumer
	}

	return time.Since(startedAt) < duration, nil
}

// latestConsumerStart returns the most recent start time among running containers that mount volume,
// or the zero time when none do.
//
// The spec and status slices are walked in step rather than concatenated: they belong to an object the
// informer cache shares with every caller, and appending one onto the other would write into its
// backing array.
func latestConsumerStart(pod *corev1.Pod, volume string) time.Time {
	var latest time.Time
	consider := func(containers []corev1.Container, statuses []corev1.ContainerStatus) {
		for _, container := range containers {
			if !mountsVolume(container, volume) {
				continue
			}
			for _, status := range statuses {
				if status.Name != container.Name {
					continue
				}
				if running := status.State.Running; running != nil && running.StartedAt.After(latest) {
					latest = running.StartedAt.Time
				}
				break
			}
		}
	}

	consider(pod.Spec.Containers, pod.Status.ContainerStatuses)
	// Restartable init containers, the native sidecars of 1.29 and later, run alongside the app and
	// report separately, so a consumer can legitimately live there.
	consider(pod.Spec.InitContainers, pod.Status.InitContainerStatuses)
	return latest
}

func mountsVolume(container corev1.Container, volume string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == volume {
			return true
		}
	}
	return false
}
