package reactor

import (
	"context"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	fakekubetesting "k8s.io/client-go/testing"
	"math"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func Test_PodWatcher_RequestTimeout(t *testing.T) {
	g := NewWithT(t)
	ctx := context.TODO()

	clientset := fake.NewSimpleClientset()

	pw, err := NewPodWatcher(ctx, time.Second, clientset, metav1.NamespaceDefault)
	g.Expect(err).To(BeNil())
	called := false

	pw.WithTimeoutPodFn(func(msg string) {
		called = true
	})

	pw.Start(metav1.ListOptions{})
	g.Expect(called).To(BeTrue())
}

func Test_PodWatcher_ContextTimeout(t *testing.T) {
	g := NewWithT(t)
	ctx := context.TODO()
	ctxWithDeadline, cancel := context.WithDeadline(ctx, time.Now().Add(time.Second))
	defer cancel()

	clientset := fake.NewSimpleClientset()

	pw, err := NewPodWatcher(ctxWithDeadline, math.MaxInt64, clientset, metav1.NamespaceDefault)
	g.Expect(err).To(BeNil())
	called := false

	pw.WithTimeoutPodFn(func(msg string) {
		called = true
	})

	pw.Start(metav1.ListOptions{})
	g.Expect(called).To(BeTrue())
}

func Test_PodWatcher_NotCalledYet(t *testing.T) {
	// we separate this test out from the other events given the
	// lazy check we have for not getting pod events
	g := NewWithT(t)
	ctx := context.TODO()

	clientset := fake.NewSimpleClientset()

	pw, err := NewPodWatcher(ctx, math.MaxInt64, clientset, metav1.NamespaceDefault)
	g.Expect(err).To(BeNil())

	eventsCh := make(chan bool, 1)
	eventsDoneCh := make(chan bool, 1)

	called := false
	pw.WithNoPodEventsYetFn(func(podList *corev1.PodList) {
		called = true
		eventsCh <- true
	})

	// executing the event loop in the background, and waiting for the stop channel before inspecting
	// for errors
	go func() {
		_, err := pw.Start(metav1.ListOptions{})
		<-pw.stopCh
		g.Expect(err).To(BeNil())
		eventsDoneCh <- true
	}()

	<-eventsCh
	pw.Stop()
	<-eventsDoneCh

	if !called {
		t.Fatal("called still false")
	}
}

func Test_PodWatcher_NotCalledYet_AllEventsBeforeWatchStart(t *testing.T) {
	// we separate this test out from the other events given the
	// lazy check we have for not getting pod events
	g := NewWithT(t)
	ctx := context.TODO()

	clientset := fake.NewSimpleClientset()
	// set up lister that will return pod, but we don't create/update a Pod, thus we do not trigger
	// events on the watch; mimics a Pod completing before the watch is established.
	listReactorFunc := func(action fakekubetesting.Action) (handled bool, ret kruntime.Object, err error) {
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: metav1.NamespaceDefault,
				Name:      "pod",
			},
		}
		return true, &corev1.PodList{Items: []corev1.Pod{pod}}, nil
	}
	clientset.PrependReactor("list", "pods", listReactorFunc)

	pw, err := NewPodWatcher(ctx, math.MaxInt64, clientset, metav1.NamespaceDefault)
	g.Expect(err).To(BeNil())

	eventsCh := make(chan bool, 1)
	eventsDoneCh := make(chan bool, 1)

	noEventsCalled := false
	podListProvided := false
	pw.WithNoPodEventsYetFn(func(podList *corev1.PodList) {
		noEventsCalled = true
		if podList != nil {
			podListProvided = true
		}
		eventsCh <- true
	})

	// executing the event loop in the background, and waiting for the stop channel before inspecting
	// for errors
	go func() {
		_, err := pw.Start(metav1.ListOptions{})
		<-pw.stopCh
		g.Expect(err).To(BeNil())
		eventsDoneCh <- true
	}()

	<-eventsCh
	pw.Stop()
	<-eventsDoneCh

	if !noEventsCalled {
		t.Fatal("noEventsCalled still false")
	}
	if !podListProvided {
		t.Fatal("podListProvided still false")
	}
}

func Test_PodWatcherEvents(t *testing.T) {
	g := NewWithT(t)
	ctx := context.TODO()

	clientset := fake.NewSimpleClientset()

	pw, err := NewPodWatcher(ctx, math.MaxInt64, clientset, metav1.NamespaceDefault)
	g.Expect(err).To(BeNil())

	eventsCh := make(chan string, 5)
	eventsDoneCh := make(chan bool, 1)

	skipPODFn := "SkipPodFn"
	onPodAddedFn := "OnPodAddedFn"
	onPodDeletedFn := "OnPodDeletedFn"
	onPodModifiedFn := "OnPodModifiedFn"

	// adding functions to be triggered on all types of events, and sending the function name over
	// the events channel
	pw.WithSkipPodFn(func(pod *corev1.Pod) bool {
		eventsCh <- skipPODFn
		return false
	}).WithOnPodAddedFn(func(pod *corev1.Pod) error {
		eventsCh <- onPodAddedFn
		return nil
	}).WithOnPodDeletedFn(func(pod *corev1.Pod) error {
		eventsCh <- onPodDeletedFn
		return nil
	}).WithOnPodModifiedFn(func(pod *corev1.Pod) error {
		eventsCh <- onPodModifiedFn
		return nil
	})

	// executing the event loop in the background, and waiting for the stop channel before inspecting
	// for errors
	go func() {
		_, err := pw.Start(metav1.ListOptions{})
		<-pw.stopCh
		g.Expect(err).To(BeNil())
		eventsDoneCh <- true
	}()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "pod",
		},
	}

	// making modifications in the pod, making sure all events are exercised, thus the events channel
	// should be populated
	podClient := clientset.CoreV1().Pods(metav1.NamespaceDefault)

	t.Run("pod-is-added", func(t *testing.T) {
		var err error
		pod, err = podClient.Create(ctx, pod, metav1.CreateOptions{})
		g.Expect(err).To(BeNil())
	})

	t.Run("pod-is-modified", func(t *testing.T) {
		pod.SetLabels(map[string]string{"label": "value"})

		var err error
		pod, err = podClient.Update(ctx, pod, metav1.UpdateOptions{})
		g.Expect(err).To(BeNil())
	})

	t.Run("pod-is-deleted", func(t *testing.T) {
		err := podClient.Delete(ctx, pod.GetName(), metav1.DeleteOptions{})
		g.Expect(err).To(BeNil())
	})

	// stopping event-loop running in the background, after waiting for events to arrive on events
	// channel, and before running assertions, it waits for eventsDoneCh as well
	<-eventsCh
	pw.Stop()
	<-eventsDoneCh

	// asserting that all events have been exercised, by inspecting the function names sent over the
	// events channel
	g.Eventually(eventsCh).Should(Receive(&skipPODFn))
	g.Eventually(eventsCh).Should(Receive(&onPodAddedFn))
	g.Eventually(eventsCh).Should(Receive(&onPodModifiedFn))
	// sometimes it is slow to get these when running go test with race detection
	g.Eventually(eventsCh, 10*time.Second).Should(Receive(&onPodDeletedFn))
}
