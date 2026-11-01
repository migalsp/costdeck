package controller

import (
	"reflect"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestScalingStagesTakeOnlyMembersAndCloudTargetsFromTheSequence(t *testing.T) {
	group := &finopsv1.ScalingGroup{Spec: finopsv1.ScalingGroupSpec{
		Namespaces: []string{"db", "api", "web"},
		Sequence:   []string{"db kube-system", "ext:orders-db", "stranger", "api"},
	}}
	r := &ScalingGroupReconciler{}
	for _, tt := range []struct {
		name string
		skip []string
		want [][]string
	}{
		{"names outside the group are ignored, members left out are started last", nil, [][]string{{"db"}, {"ext:orders-db"}, {"api"}, {"web"}}},
		{"a namespace an older group owns is never scaled", []string{"api"}, [][]string{{"db"}, {"ext:orders-db"}, {"web"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.getScalingStages(group, true, tt.skip); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stages = %v, want %v", got, tt.want)
			}
		})
	}
}
