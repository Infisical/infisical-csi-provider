package window

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Runs the window evaluation against a real cluster, on a pod that really restarts. The unit tests
// use fabricated statuses; this checks the assumptions behind them, in particular that a container
// restarted in place keeps the pod UID, increments restartCount, and gets a fresh startedAt.
//
//	WINDOW_IT_CONTEXT=<kube context> [WINDOW_IT_NAMESPACE=<ns>] go test ./internal/window/ -run Integration -v
func TestIntegrationWindowFollowsARealRestart(t *testing.T) {
	kubeContext := os.Getenv("WINDOW_IT_CONTEXT")
	if kubeContext == "" {
		t.Skip("set WINDOW_IT_CONTEXT to run against a cluster")
	}
	namespace := os.Getenv("WINDOW_IT_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	clientset := clientsetFor(t, kubeContext)
	ctx := context.Background()
	const duration = 15 * time.Second

	name := "window-it"
	_ = clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	waitGone(t, clientset, namespace, name)

	// Exits non-zero shortly after starting, so the kubelet restarts it in place and we observe a
	// second container generation without a new pod.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "app",
				Image:   "public.ecr.aws/docker/library/busybox:1.36",
				Command: []string{"sh", "-c", "sleep 30; exit 1"},
			}},
		},
	}
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	pods := apiPods{clientset: clientset}
	var sawOpen, sawClosed, sawReopened bool

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) && !sawReopened {
		open, err := IsOpen(ctx, pods, Pod{Namespace: namespace, Name: name}, duration)
		if err != nil {
			t.Logf("IsOpen: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		restarts := restartCount(t, clientset, namespace, name)
		t.Logf("open=%-5v restarts=%d", open, restarts)

		switch {
		case open && !sawOpen:
			sawOpen = true
		case !open && sawOpen:
			sawClosed = true
		case open && sawClosed && restarts > 0:
			sawReopened = true
		}
		time.Sleep(3 * time.Second)
	}

	if !sawOpen {
		t.Fatal("never saw an open window after the pod started")
	}
	if !sawClosed {
		t.Fatal("window never closed while the container kept running")
	}
	if !sawReopened {
		t.Fatal("window never reopened after the container restarted")
	}
}

func clientsetFor(t *testing.T, kubeContext string) kubernetes.Interface {
	t.Helper()
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		t.Fatalf("kube config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	return clientset
}

func restartCount(t *testing.T, clientset kubernetes.Interface, namespace, name string) int32 {
	t.Helper()
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return -1
	}
	var restarts int32
	for _, status := range pod.Status.ContainerStatuses {
		restarts += status.RestartCount
	}
	return restarts
}

func waitGone(t *testing.T, clientset kubernetes.Interface, namespace, name string) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if _, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("pod %s/%s did not go away", namespace, name)
}
