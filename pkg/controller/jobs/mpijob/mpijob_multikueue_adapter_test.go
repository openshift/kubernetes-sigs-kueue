/*
Copyright 2024 The Kubernetes Authors.

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

package mpijob

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

const (
	TestNamespace = "ns"
)

func TestMultikueueAdapter(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	mpiJobBuilder := utiltestingmpijob.MakeMPIJob("mpijob1", TestNamespace)

	cases := map[string]struct {
		managersJobSets []kfmpi.MPIJob
		workerJobSets   []kfmpi.MPIJob

		operation func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error

		wantError           error
		wantManagersJobSets []kfmpi.MPIJob
		wantWorkerJobSets   []kfmpi.MPIJob
	}{

		"sync creates missing remote mpijob": {
			managersJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "mpijob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.DeepCopy(),
			},
			wantWorkerJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote mpijob": {
			managersJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.DeepCopy(),
			},
			workerJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(kfmpi.JobCondition{Type: kfmpi.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "mpijob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.Clone().
					StatusConditions(kfmpi.JobCondition{Type: kfmpi.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(kfmpi.JobCondition{Type: kfmpi.JobSucceeded, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote mpijob is deleted": {
			workerJobSets: []kfmpi.MPIJob{
				*mpiJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "mpijob1", Namespace: TestNamespace})
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(kfmpi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&kfmpi.MPIJobList{Items: tc.managersJobSets})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersJobSets, func(w *kfmpi.MPIJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(kfmpi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&kfmpi.MPIJobList{Items: tc.workerJobSets})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multikueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersJobSets := &kfmpi.MPIJobList{}
			if err := managerClient.List(ctx, gotManagersJobSets); err != nil {
				t.Errorf("unexpected list manager's mpijobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersJobSets, gotManagersJobSets.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's mpijobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerJobSets := &kfmpi.MPIJobList{}
			if err := workerClient.List(ctx, gotWorkerJobSets); err != nil {
				t.Errorf("unexpected list worker's mpijobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerJobSets, gotWorkerJobSets.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's mpijobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}