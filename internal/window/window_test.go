package window

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const volume = "secrets"

// apiPods is the trivial PodSource the tests use; production serves it from a watch.
type apiPods struct{ clientset kubernetes.Interface }

func (a apiPods) Get(ctx context.Context, namespace, name, _ string) (*corev1.Pod, error) {
	return a.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

type container struct {
	name       string
	mounts     bool
	startedAgo time.Duration
	waiting    bool
	init       bool
}

func buildPod(startedAgo time.Duration, containers ...container) *corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-startedAgo))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "a-pod", Namespace: "ns", CreationTimestamp: start},
		Status:     corev1.PodStatus{StartTime: &start},
	}
	for _, c := range containers {
		spec := corev1.Container{Name: c.name}
		if c.mounts {
			spec.VolumeMounts = []corev1.VolumeMount{{Name: volume, MountPath: "/mnt"}}
		}
		status := corev1.ContainerStatus{Name: c.name}
		if c.waiting {
			status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
		} else {
			status.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{
				StartedAt: metav1.NewTime(time.Now().Add(-c.startedAgo)),
			}}
		}
		if c.init {
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, spec)
			pod.Status.InitContainerStatuses = append(pod.Status.InitContainerStatuses, status)
		} else {
			pod.Spec.Containers = append(pod.Spec.Containers, spec)
			pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, status)
		}
	}
	return pod
}

func isOpen(t *testing.T, p *corev1.Pod, duration time.Duration) bool {
	t.Helper()
	target := Pod{Namespace: "ns", Name: "a-pod", Volume: volume}
	open, err := IsOpen(context.Background(), apiPods{clientset: fake.NewClientset(p)}, target, duration)
	if err != nil {
		t.Fatalf("IsOpen: %v", err)
	}
	return open
}

func TestOpenRightAfterStart(t *testing.T) {
	p := buildPod(2*time.Second, container{name: "app", mounts: true, startedAgo: 2 * time.Second})
	if !isOpen(t, p, time.Minute) {
		t.Fatal("expected an open window")
	}
}

func TestClosedOnceTheWindowElapsed(t *testing.T) {
	p := buildPod(5*time.Minute, container{name: "app", mounts: true, startedAgo: 5 * time.Minute})
	if isOpen(t, p, time.Minute) {
		t.Fatal("expected a closed window")
	}
}

func TestOpenAtMountTimeBeforeAnyContainerRuns(t *testing.T) {
	// The driver publishes the volume before the containers start, so keying only on container start
	// would report a closed window exactly when the secrets are needed.
	p := buildPod(2*time.Second, container{name: "app", mounts: true, waiting: true})
	if !isOpen(t, p, time.Minute) {
		t.Fatal("expected an open window at mount time")
	}
}

func TestARestartReopensTheWindow(t *testing.T) {
	// The kubelet restarts a container in place and replays nothing, so the application comes back
	// needing its secrets. The fresh start time is what reopens the window, on a pod whose own start
	// time is long past.
	restarted := buildPod(5*time.Minute, container{name: "app", mounts: true, startedAgo: 2 * time.Second})
	if !isOpen(t, restarted, time.Minute) {
		t.Fatal("expected the window to reopen after the restart")
	}
}

func TestACrashLoopingNonConsumerDoesNotHoldTheWindowOpen(t *testing.T) {
	// The exposure this whole feature bounds. A sidecar that does not mount the volume restarting every
	// few seconds used to keep the window open for the pod's entire life, and code running in the pod
	// could trigger that deliberately.
	p := buildPod(1*time.Hour,
		container{name: "app", mounts: true, startedAgo: 1 * time.Hour},
		container{name: "metrics", mounts: false, startedAgo: 1 * time.Second},
	)
	if isOpen(t, p, time.Minute) {
		t.Fatal("a container that does not mount the volume must not reopen the window")
	}
}

func TestARestartedNativeSidecarConsumerReopensTheWindow(t *testing.T) {
	// Restartable init containers, the native sidecars of 1.29 and later, report through
	// InitContainerStatuses. A consumer there is as legitimate as one in Containers.
	p := buildPod(1*time.Hour,
		container{name: "app", mounts: false, startedAgo: 1 * time.Hour},
		container{name: "proxy", mounts: true, startedAgo: 2 * time.Second, init: true},
	)
	if !isOpen(t, p, time.Minute) {
		t.Fatal("a restarted native sidecar consumer must reopen the window")
	}
}

func TestNoIdentifiableConsumerFallsBackToPodStart(t *testing.T) {
	// Failing closed: without a volume name we cannot tell consumers apart, so the window rides the
	// pod's start time and shuts on schedule rather than being held open by something unrelated.
	p := buildPod(1*time.Hour, container{name: "app", mounts: true, startedAgo: 1 * time.Second})
	target := Pod{Namespace: "ns", Name: "a-pod"}
	open, err := IsOpen(context.Background(), apiPods{clientset: fake.NewClientset(p)}, target, time.Minute)
	if err != nil {
		t.Fatalf("IsOpen: %v", err)
	}
	if open {
		t.Fatal("expected the window to ride the pod start time when no consumer can be identified")
	}
}
