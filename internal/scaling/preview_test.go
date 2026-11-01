package scaling

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestWouldChangeCountsReplicaChangesWithoutMakingThem(t *testing.T) {
	ctx := context.Background()
	running := func(name string, replicas int32, recorded string) *appsv1.Deployment {
		d := &appsv1.Deployment{Name: name, Namespace: "shop", Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
		if recorded != "" {
			d.Annotations = map[string]string{OriginalReplicasAnnotation: recorded}
		}
		return d
	}
	e := hibernateEngine(t, nil, nil,
		running("api", 3, ""),
		running("web", 2, "2"),
		running("stopped-with-record", 0, "4"),
		running("stopped-without-record", 0, ""),
		running("partly-started", 1, "3"),
		running("excluded", 5, ""),
	)
	exclusions := []string{"excluded"}

	if n, err := e.WouldChange(ctx, "shop", false, exclusions); err != nil || n != 3 {
		t.Errorf("WouldChange(down) = %d, %v; want api, web and partly-started, the running ones", n, err)
	}
	if n, err := e.WouldChange(ctx, "shop", true, exclusions); err != nil || n != 3 {
		t.Errorf("WouldChange(up) = %d, %v; want both stopped workloads (back to 4 and to the default 1) and partly-started (back to 3)", n, err)
	}
	if got := *readDeployment(t, e, "api").Spec.Replicas; got != 3 {
		t.Errorf("a dry run changed api to %d replicas", got)
	}

	failing := hibernateEngine(t, nil, &interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("apiserver unavailable")
	}})
	if n, err := failing.WouldChange(ctx, "shop", true, nil); err == nil || n != 0 {
		t.Errorf("WouldChange with a failing list = %d, %v", n, err)
	}
}
